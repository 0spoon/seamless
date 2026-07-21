package gardener

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// Memory-wanted thresholds: recurring zero-hit recall queries are demand for
// knowledge that does not exist. The session floor keeps one agent retrying a
// failing query from firing alone; the window must stay inside
// ToolEventRetentionDays (recall.miss is a pruned transport event) or the pass
// would silently see a shorter horizon than it claims.
const (
	memoryWantedWindow      = 14 * 24 * time.Hour
	memoryWantedMinSessions = 2  // distinct sessions that missed the same signature
	memoryWantedMaxPerRun   = 5  // proposal flood control per pass
	memoryWantedMaxQueries  = 5  // distinct raw queries kept as proposal evidence
	memoryWantedTitleRunes  = 80 // task-title budget for the suggested topic
)

// missStopwords are dropped when a miss query is normalized to a signature:
// framing words that vary freely between phrasings of the same question.
var missStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "how": {}, "what": {}, "why": {},
	"where": {}, "when": {}, "who": {}, "which": {}, "does": {}, "did": {},
	"can": {}, "could": {}, "should": {}, "would": {}, "about": {}, "using": {},
	"use": {}, "not": {}, "this": {}, "that": {}, "from": {}, "into": {},
	"any": {}, "all": {}, "are": {}, "was": {}, "were": {}, "have": {}, "has": {},
}

// normalizeMissQuery reduces a recall query to its signature: lowercase content
// terms, deduplicated and sorted, joined with "-". Word order, casing,
// punctuation, and framing words all wash out, so rephrasings of the same
// question group together. The signature is the stable identity of a knowledge
// gap -- the proposal key derives from it and nothing else, which is what lets
// a dismissal hold forever. An empty signature means the query had no usable
// content terms.
func normalizeMissQuery(q string) string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(fields))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if utf8.RuneCountInString(f) < 3 {
			continue
		}
		if _, stop := missStopwords[f]; stop {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		terms = append(terms, f)
	}
	sort.Strings(terms)
	return strings.Join(terms, "-")
}

// missGroup accumulates the window's zero-hit recalls that share one
// (project, signature) identity.
type missGroup struct {
	project, sig string
	sessions     map[string]struct{}
	queries      []string // distinct raw phrasings, oldest first
	count        int
	first, last  time.Time
}

// proposeMemoryWanted proposes writing the memories agents keep searching for
// and not finding. It groups the window's recall.miss events by (project,
// normalized query signature) and proposes each group that recurred across
// enough sessions -- unless the same signature also succeeded in the window (an
// intermittent miss is a ranking problem, not a gap), or an FTS probe shows the
// gap has since been filled. The key depends only on the group identity, never
// on membership or counts, so a dismissed gap stays dismissed as evidence
// accumulates and pruning cannot resurrect it.
func (s *Service) proposeMemoryWanted(ctx context.Context, seen map[string]struct{}) (int, error) {
	now := s.now().UTC()
	since := now.Add(-memoryWantedWindow)
	misses, err := store.RecallMissesSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	if len(misses) == 0 {
		return 0, nil
	}
	hits, err := store.RecallHitQueriesSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	hitSigs := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		if sig := normalizeMissQuery(h.Query); sig != "" {
			hitSigs[h.Project+"\x00"+sig] = struct{}{}
		}
	}

	groups := map[string]*missGroup{}
	for _, m := range misses {
		sig := normalizeMissQuery(m.Query)
		if sig == "" {
			continue
		}
		gk := m.Project + "\x00" + sig
		if _, hit := hitSigs[gk]; hit {
			continue
		}
		g := groups[gk]
		if g == nil {
			g = &missGroup{project: m.Project, sig: sig, sessions: map[string]struct{}{}, first: m.TS}
			groups[gk] = g
		}
		g.count++
		g.last = m.TS
		// An empty session id buckets every unattributed miss together, so
		// unattributed misses alone can never satisfy the session floor.
		g.sessions[m.SessionID] = struct{}{}
		if !slices.Contains(g.queries, m.Query) {
			g.queries = append(g.queries, m.Query)
		}
	}

	ordered := make([]*missGroup, 0, len(groups))
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
		return ordered[i].sig < ordered[j].sig
	})

	created := 0
	for _, g := range ordered {
		if len(g.sessions) < memoryWantedMinSessions {
			continue
		}
		key := "memory_wanted:" + g.project + ":" + g.sig
		if _, dup := seen[key]; dup {
			continue
		}
		// Liveness guard: the gap may have been filled since the misses were
		// logged. Probe with AND semantics over the signature's terms in the
		// group's scope (project + global, mirroring recall) -- an item covering
		// every content term is evidence the knowledge now exists, while the
		// recall-style OR would let any item sharing one common word suppress a
		// real gap.
		latest := g.queries[len(g.queries)-1]
		probe, err := store.FTSSearchAllTerms(ctx, s.db, strings.Split(g.sig, "-"), nil, []string{g.project, ""}, 1)
		if err != nil {
			return created, err
		}
		if len(probe) > 0 {
			continue
		}
		payload := map[string]any{
			"project": g.project, "signature": g.sig,
			"queries":    recentQueries(g.queries, memoryWantedMaxQueries),
			"miss_count": g.count, "session_count": len(g.sessions),
			"first_missed_at": core.FormatTime(g.first),
			"last_missed_at":  core.FormatTime(g.last),
			"suggested_title": latest,
			"reason": fmt.Sprintf("knowledge gap: recall found nothing %dx across %d sessions in %dd",
				g.count, len(g.sessions), int(memoryWantedWindow.Hours()/24)),
		}
		if _, err := s.createProposal(ctx, store.ProposalMemoryWanted, key, payload, seen); err != nil {
			return created, err
		}
		created++
		if created == memoryWantedMaxPerRun {
			break
		}
	}
	return created, nil
}

// applyMemoryWanted turns the knowledge gap into an open task in the project's
// queue, where tasks_ready surfaces it to the next agent. It is idempotent: a
// retry after a partial apply reuses the identically-titled open task instead
// of duplicating it -- important because Apply leaves the proposal pending on
// failure.
func (s *Service) applyMemoryWanted(ctx context.Context, p store.Proposal, now time.Time) (map[string]any, error) {
	topic := payloadString(p.Payload, "suggested_title")
	if topic == "" {
		return nil, errors.New("memory_wanted proposal missing suggested_title")
	}
	project := payloadString(p.Payload, "project")
	title := "Write a memory: " + events.Truncate(topic, memoryWantedTitleRunes)

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
		Body: memoryWantedTaskBody(p.Payload), Status: core.TaskOpen,
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

// memoryWantedTaskBody renders the proposal's evidence as the task body the
// writing agent will read.
func memoryWantedTaskBody(payload map[string]any) string {
	var b strings.Builder
	b.WriteString("Agents searched for this and recall returned nothing:\n\n")
	for _, q := range payloadStrings(payload, "queries") {
		fmt.Fprintf(&b, "- %q\n", q)
	}
	b.WriteString("\n")
	if reason := payloadString(payload, "reason"); reason != "" {
		b.WriteString(reason + ". ")
	}
	b.WriteString("Write a memory (or note) that answers these queries; once it exists, recall finds it and the gap closes.\n")
	return b.String()
}

// recentQueries returns up to max entries of qs, newest first.
func recentQueries(qs []string, max int) []string {
	out := make([]string, 0, max)
	for i := len(qs) - 1; i >= 0 && len(out) < max; i-- {
		out = append(out, qs[i])
	}
	return out
}

// payloadStrings reads a string-array field from a proposal payload (a JSON
// round-trip delivers it as []any).
func payloadStrings(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		// A payload that never crossed JSON (a same-process apply right after
		// propose) still holds the original []string.
		if ss, ok := m[key].([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
