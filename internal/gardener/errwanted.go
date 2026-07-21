package gardener

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// Tool-error thresholds: the same error returned to agents again and again is
// demand for a surface fix (a better tool description, a param alias, a schema
// change, a doc). The pass mirrors memory-wanted over two surfaces: failed
// tool.call events (surface "tool") and swallowed hook failures (surface
// "hook"). The window must stay inside ToolEventRetentionDays (both kinds are
// pruned transport events) or the pass would silently see a shorter horizon
// than it claims. Errors are noisier than recall misses, so a count floor
// applies on top of the session floor; the session floor is waived for hooks,
// whose failures are often unattributed (a failed session-start has no bound
// session yet).
const (
	toolErrorWindow      = 14 * 24 * time.Hour
	toolErrorMinSessions = 2 // distinct sessions that hit the same signature (tool surface only)
	toolErrorMinCount    = 3 // occurrences before a pattern is worth a task
	toolErrorMaxPerRun   = 5 // proposal flood control per pass
	toolErrorMaxExamples = 5 // raw error/args examples kept as proposal evidence
	toolErrorTitleRunes  = 80
	// toolErrorFreshWindow is the liveness tail: a group whose last occurrence
	// is older than this has plausibly been fixed already, so it is not
	// proposed (the errors-equivalent of memory-wanted's FTS probe).
	toolErrorFreshWindow = 3 * 24 * time.Hour
)

// Masking for tool-error signatures, applied in order: quoted literals first
// (they may contain ids or digits), then ULIDs, then digit runs.
var (
	errQuotedRe = regexp.MustCompile(`"[^"]*"`)
	errULIDRe   = regexp.MustCompile(`\b[0-9a-hjkmnp-tv-z]{26}\b`)
	errDigitsRe = regexp.MustCompile(`[0-9]+`)
	errSpaceRe  = regexp.MustCompile(`\s+`)
)

// normalizeErrorSignature reduces a tool error message to its template: the
// leading "<tool>: " prefix stripped, lowercased, with caller-specific literals
// (quoted values, ULIDs, numbers) masked and whitespace collapsed. Rephrasings
// of the same failure -- every typo'd parameter, every bad id -- group
// together, so the granularity that recurs is the tool + error template. The
// masked literals stay available to the reader as raw examples in the proposal
// evidence. The template is the stable identity of an error pattern; the
// proposal key derives from it and nothing else, which is what lets a
// dismissal hold forever.
func normalizeErrorSignature(tool, msg string) string {
	msg = strings.TrimSpace(msg)
	if rest, ok := strings.CutPrefix(msg, tool+":"); ok {
		msg = rest
	}
	msg = strings.ToLower(msg)
	msg = errQuotedRe.ReplaceAllString(msg, `<v>`)
	msg = errULIDRe.ReplaceAllString(msg, `<id>`)
	msg = errDigitsRe.ReplaceAllString(msg, `<n>`)
	msg = errSpaceRe.ReplaceAllString(msg, " ")
	return strings.TrimSpace(msg)
}

// benignErrorSubstrings match errors that are legitimate control flow rather
// than a surface defect: a lost claim race (tasks_claim answering "already
// claimed" is the contract working) and not-found probe reads. Matched errors
// are dropped before grouping. Deliberately conservative -- extend it when a
// pattern proves benign, rather than guessing up front.
var benignErrorSubstrings = []string{
	"already claimed",
	"not found",
}

// benignError reports whether msg is expected control flow, not a defect.
func benignError(msg string) bool {
	lower := strings.ToLower(msg)
	for _, sub := range benignErrorSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// errGroup accumulates the window's agent errors that share one (project,
// surface, key, template) identity.
type errGroup struct {
	project, surface, key, template string

	sessions    map[string]struct{}
	examples    []string         // distinct raw error messages, oldest first
	args        []map[string]any // example arguments (tool surface only)
	count       int
	first, last time.Time
}

// errProposalKey derives the dismissal-stable key for a group. The identity
// hashes to keep the key compact and free of separator collisions; it depends
// only on the group identity, never on membership or counts, so a dismissed
// pattern stays dismissed as evidence accumulates.
func errProposalKey(g *errGroup) string {
	sum := sha256.Sum256([]byte(g.surface + "\x00" + g.key + "\x00" + g.template))
	return "tool_error:" + g.project + ":" + hex.EncodeToString(sum[:8])
}

// proposeToolError proposes fixing the errors agents keep hitting. It unions
// the window's failed tool.call events and hook.error events, groups them by
// (project, surface, key, masked template), and proposes each group that
// recurred enough -- unless the error stopped occurring (the liveness tail) or
// it matches the benign-control-flow list. Hook groups waive the session floor:
// a recurring hook failure is a real defect regardless of session spread,
// and hook errors are usually unattributed anyway.
func (s *Service) proposeToolError(ctx context.Context, seen map[string]struct{}) (int, error) {
	now := s.now().UTC()
	since := now.Add(-toolErrorWindow)
	toolErrs, err := store.ToolErrorsSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	hookErrs, err := store.HookErrorsSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	all := append(toolErrs, hookErrs...)
	if len(all) == 0 {
		return 0, nil
	}

	groups := map[string]*errGroup{}
	for _, e := range all {
		if benignError(e.Error) {
			continue
		}
		// Hook stages are already stable curated labels; tool errors mask their
		// caller-specific literals into a template.
		template := ""
		if e.Surface == "tool" {
			if template = normalizeErrorSignature(e.Key, e.Error); template == "" {
				continue
			}
		}
		gk := e.Project + "\x00" + e.Surface + "\x00" + e.Key + "\x00" + template
		g := groups[gk]
		if g == nil {
			g = &errGroup{
				project: e.Project, surface: e.Surface, key: e.Key, template: template,
				sessions: map[string]struct{}{}, first: e.TS,
			}
			groups[gk] = g
		}
		g.count++
		g.last = e.TS
		// An empty session id buckets every unattributed error together, so
		// unattributed errors alone can never satisfy the session floor.
		g.sessions[e.SessionID] = struct{}{}
		if !slices.Contains(g.examples, e.Error) {
			g.examples = append(g.examples, e.Error)
		}
		if len(e.Args) > 0 && len(g.args) < toolErrorMaxExamples {
			g.args = append(g.args, e.Args)
		}
	}

	ordered := make([]*errGroup, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].count != ordered[j].count {
			return ordered[i].count > ordered[j].count
		}
		if ordered[i].project != ordered[j].project {
			return ordered[i].project < ordered[j].project
		}
		if ordered[i].key != ordered[j].key {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].template < ordered[j].template
	})

	windowDays := int(toolErrorWindow.Hours() / 24)
	created := 0
	for _, g := range ordered {
		if g.count < toolErrorMinCount {
			continue
		}
		if g.surface == "tool" && len(g.sessions) < toolErrorMinSessions {
			continue
		}
		// Liveness tail: an error that stopped recurring may already be fixed.
		if g.last.Before(now.Add(-toolErrorFreshWindow)) {
			continue
		}
		key := errProposalKey(g)
		if _, dup := seen[key]; dup {
			continue
		}
		var title, signature, reason string
		if g.surface == "hook" {
			title = "hook: " + g.key
			signature = g.key
			reason = fmt.Sprintf("recurring hook failure: stage %q failed %dx in %dd, swallowed fail-open",
				g.key, g.count, windowDays)
		} else {
			title = g.key + ": " + g.template
			signature = g.template
			reason = fmt.Sprintf("recurring tool error: %s returned this error %dx across %d sessions in %dd",
				g.key, g.count, len(g.sessions), windowDays)
		}
		// "name" carries the tool name / stage label; "key" is reserved for the
		// proposal key createProposal stamps onto every payload.
		payload := map[string]any{
			"project": g.project, "surface": g.surface, "name": g.key,
			"signature":   signature,
			"examples":    recentQueries(g.examples, toolErrorMaxExamples),
			"error_count": g.count, "session_count": len(g.sessions),
			"first_seen_at":   core.FormatTime(g.first),
			"last_seen_at":    core.FormatTime(g.last),
			"suggested_title": title,
			"reason":          reason,
		}
		if len(g.args) > 0 {
			payload["example_args"] = g.args
		}
		if _, err := s.createProposal(ctx, store.ProposalToolError, key, payload, seen); err != nil {
			return created, err
		}
		created++
		if created == toolErrorMaxPerRun {
			break
		}
	}
	return created, nil
}

// applyToolError turns the recurring error into an open task in the project's
// queue, where tasks_ready surfaces it to the next agent. It is idempotent: a
// retry after a partial apply reuses the identically-titled open task instead
// of duplicating it -- important because Apply leaves the proposal pending on
// failure.
func (s *Service) applyToolError(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	label := payloadString(p.Payload, "suggested_title")
	if label == "" {
		return nil, errors.New("tool_error proposal missing suggested_title")
	}
	project := payloadString(p.Payload, "project")
	title := "Fix recurring error: " + truncateLabel(label, toolErrorTitleRunes)

	open, err := store.ListTasks(ctx, s.db, project, core.TaskOpen)
	if err != nil {
		return nil, err
	}
	for _, t := range open {
		if t.Title == title {
			return map[string]any{"task_id": t.ID, "title": t.Title, "project": project, "reused": true}, nil
		}
	}

	id, err := core.NewID()
	if err != nil {
		return nil, err
	}
	task := core.Task{
		ID: id, ProjectSlug: project, Title: title,
		Body: toolErrorTaskBody(p.Payload), Status: core.TaskOpen,
		CreatedBy: "gardener", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(ctx, s.db, task); err != nil {
		return nil, err
	}
	if s.events != nil {
		if _, err := s.events.Record(ctx, core.Event{
			Kind: core.EventTaskTransition, ProjectSlug: project, ItemID: id,
			Payload: map[string]any{"to": string(core.TaskOpen), "created": true, "by": "gardener"},
		}); err != nil {
			s.logger.Warn("gardener: record task event", "task", id, "error", err)
		}
	}
	return map[string]any{"task_id": id, "title": title, "project": project}, nil
}

// truncateLabel caps a single-line label at maxRunes. Task titles need this
// instead of events.Truncate, whose truncation marker embeds a newline meant
// for multi-line event bodies.
func truncateLabel(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// toolErrorTaskBody renders the proposal's evidence as the task body the fixing
// agent will read, framed per surface: a tool error asks for a surface fix, a
// hook error asks for an investigation of the swallowed failure.
func toolErrorTaskBody(payload map[string]any) string {
	var b strings.Builder
	surface := payloadString(payload, "surface")
	key := payloadString(payload, "name")
	if surface == "hook" {
		fmt.Fprintf(&b, "The %q hook stage keeps failing (fail-open, so agents never see it):\n\n", key)
	} else {
		fmt.Fprintf(&b, "Agents keep getting this error from the %s tool:\n\n", key)
	}
	for _, e := range payloadStrings(payload, "examples") {
		fmt.Fprintf(&b, "- %q\n", e)
	}
	if args := payloadList(payload, "example_args"); len(args) > 0 {
		b.WriteString("\nExample arguments:\n")
		for _, a := range args {
			raw, err := json.Marshal(a)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "- `%s`\n", raw)
		}
	}
	b.WriteString("\n")
	if reason := payloadString(payload, "reason"); reason != "" {
		b.WriteString(reason + ". ")
	}
	if surface == "hook" {
		b.WriteString("Investigate and fix the underlying failure; the hook swallows it, so only this signal surfaces it.\n")
	} else {
		b.WriteString("Consider a fix, a tool-description or alias change, a stricter schema, a doc, or a fine-tune.\n")
	}
	return b.String()
}
