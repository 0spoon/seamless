package hooks

// Claude Code plan-mode capture. Plan mode writes its plan to
// ~/.claude/plans/<basename>.md with the ordinary Write/Edit tools, so the
// PostToolUse hook sees every iteration; ExitPlanMode's PostToolUse fires on
// user approval; PermissionRequest[ExitPlanMode] fires when the user is shown
// the plan. Each plan becomes one upserted note (slug cc-plan-<basename>) whose
// lifecycle rides on a plan-status:draft|presented|approved tag, plus verbatim
// plan.captured/presented/approved events. Everything here is best-effort and
// fail-open: a capture problem is logged and the hook still acks 200.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

// postToolUse captures plan-file iterations (Write/Edit/MultiEdit under the
// plans dir) and plan approvals (ExitPlanMode). The seam CLI pre-filters other
// tools locally, but the daemon still dispatches defensively. A session's
// first captured iteration may return related prior knowledge as
// additionalContext; every other outcome is a bare ack.
func (h *Handler) postToolUse(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p toolPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	extra := ""
	if h.captureEnabled() {
		ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
		defer cancel()
		switch p.ToolName {
		case "Write", "Edit", "MultiEdit":
			extra = h.capturePlanIteration(ctx, p)
		case "ExitPlanMode":
			h.capturePlanApproval(ctx, p)
		}
	}
	if extra != "" {
		writeHookResponse(w, "PostToolUse", extra)
		return
	}
	writeHookAck(w)
}

// permissionRequest marks the session's draft plan as presented when the user
// is prompted to review an ExitPlanMode call. The payload carries no plan
// content; correlation is via the session's plan_capture metadata. Claude Code
// support for this hook is optional -- nothing downstream depends on it.
func (h *Handler) permissionRequest(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p toolPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	if h.captureEnabled() && p.ToolName == "ExitPlanMode" {
		ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
		defer cancel()
		h.markPlanPresented(ctx, p)
	}
	writeHookAck(w)
}

// captureEnabled reports whether plan/subagent capture can run at all.
func (h *Handler) captureEnabled() bool {
	return h.planCapture.Enabled && h.db != nil && h.files != nil
}

// capturePlanIteration re-reads the just-written plan file from disk (the hook
// runs on the same machine, and re-reading is authoritative for Write and Edit
// alike) and upserts the cc-plan note. On the session's first captured
// iteration it returns a related-prior-knowledge block for injection ("" in
// every other case).
func (h *Handler) capturePlanIteration(ctx context.Context, p toolPayload) string {
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
		return ""
	}
	path, ok := h.planFilePath(in.FilePath)
	if !ok {
		return "" // not a plan file
	}
	content, err := os.ReadFile(path)
	if err != nil {
		h.logger.Warn("hooks: plan capture read", "error", err)
		return ""
	}
	up, ok := h.upsertPlanNote(ctx, p, planBasename(path), string(content), false)
	if !ok || !up.first || !h.planCapture.InjectRelated {
		return ""
	}
	return h.relatedPlanContext(ctx, p, up.note)
}

// capturePlanApproval handles PostToolUse[ExitPlanMode]: the tool_response
// carries the plan file path (and sometimes the plan text); the file is
// re-read as the authoritative final text. On success the note flips to
// approved and, when configured, a tracking task is created for the plan.
func (h *Handler) capturePlanApproval(ctx context.Context, p toolPayload) {
	var resp struct {
		Plan     string `json:"plan"`
		FilePath string `json:"filePath"`
	}
	_ = json.Unmarshal(p.ToolResponse, &resp) // tolerant: absent fields stay zero

	basename, content := "", ""
	if resp.FilePath != "" {
		if path, ok := h.planFilePath(resp.FilePath); ok {
			basename = planBasename(path)
			if b, err := os.ReadFile(path); err == nil {
				content = string(b)
			} else {
				h.logger.Warn("hooks: plan approval read", "error", err)
			}
		}
	}
	if content == "" {
		content = resp.Plan
	}
	if basename == "" {
		// No usable filePath: correlate via the session's captured draft.
		if meta, ok := h.sessionPlanMeta(ctx, p.SessionID); ok {
			basename = meta.Basename
		}
	}
	if basename == "" {
		h.logger.Warn("hooks: plan approval without correlation", "claude_session_id", p.SessionID)
		return
	}
	up, ok := h.upsertPlanNote(ctx, p, basename, content, true)
	if !ok {
		return
	}
	if h.planCapture.AutoTask {
		h.ensurePlanTask(ctx, p, up.note, up.planSlug)
	}
}

// markPlanPresented flips the session's draft plan note to presented.
func (h *Handler) markPlanPresented(ctx context.Context, p toolPayload) {
	meta, ok := h.sessionPlanMeta(ctx, p.SessionID)
	if !ok || meta.Basename == "" || meta.Status != plans.StatusDraft {
		return
	}
	project := h.resolveProject(ctx, p.CWD)
	note, found := h.loadNoteBySlug(ctx, project, plans.NotePrefix+meta.Basename)
	if !found {
		return
	}
	note.Tags = plans.SetStatusTag(note.Tags, plans.StatusPresented)
	note.Description = plans.NoteDescription(meta.Basename, plans.NoteIteration(note), plans.StatusPresented)
	note.Updated = time.Now().UTC()
	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: plan presented write", "error", err)
		return
	}
	meta.Status = plans.StatusPresented
	h.setSessionPlanMeta(ctx, p.SessionID, meta)
	h.recordPlanEvent(ctx, core.EventPlanPresented, p.SessionID, written.ID, map[string]any{
		"basename": meta.Basename, "plan_slug": meta.PlanSlug,
	})
}

// planUpsert is upsertPlanNote's result: the written note, its composition
// slug, and whether this was the session's first plan capture (no plan_capture
// metadata existed yet) -- the trigger for the related-knowledge injection.
type planUpsert struct {
	note     core.Note
	planSlug string
	first    bool
}

// upsertPlanNote creates or updates the cc-plan-<basename> note for one plan
// iteration or approval. On update the note id, created time, and plan:<slug>
// tag are preserved (the composition slug is minted once, at first capture);
// the title, body, and status tag follow the latest content. An approval with
// no readable content flips the status of an existing note without touching
// its body; with no existing note it is dropped (fail-open). Agent-cache notes
// captured before the plan existed (pending on the session) are adopted into
// the composition here, once the slug is minted.
func (h *Handler) upsertPlanNote(ctx context.Context, p toolPayload, basename, content string, approve bool) (planUpsert, bool) {
	project := h.resolveProject(ctx, p.CWD)
	noteSlug := plans.NotePrefix + basename
	existing, found := h.loadNoteBySlug(ctx, project, noteSlug)

	trimmed := strings.TrimSpace(content)
	if trimmed == "" && !(approve && found) {
		return planUpsert{}, false
	}

	now := time.Now().UTC()
	note := existing
	iter := 1
	if found {
		iter = plans.NoteIteration(existing)
		if !approve && trimmed != "" {
			iter++
		}
	} else {
		id, err := core.NewID()
		if err != nil {
			h.logger.Warn("hooks: plan note id", "error", err)
			return planUpsert{}, false
		}
		note = core.Note{ID: id, Slug: noteSlug, Project: project, Created: now}
	}

	status := plans.StatusDraft
	if found && plans.StatusFromTags(existing.Tags) == plans.StatusApproved {
		status = plans.StatusApproved // an approved plan never regresses to draft
	}
	if approve {
		status = plans.StatusApproved
	}

	planSlug := plans.SlugFromTags(note.Tags)
	if trimmed != "" {
		title := firstHeading(content)
		if title == "" || validate.Title(title) != nil {
			title = basename
		}
		note.Title = title
		note.Body = planStamp(p.SessionID, basename, iter, gitHead(p.CWD), now) + "\n\n" + content
	}
	if planSlug == "" {
		planSlug = core.Slugify(note.Title)
	}
	note.Description = plans.NoteDescription(basename, iter, status)
	note.Tags = plans.SetStatusTag([]string{plans.SlugTag(planSlug), plans.TagPlan, "created-by:agent"}, status)
	note.Updated = now
	if note.Extra == nil {
		note.Extra = map[string]any{}
	}
	note.Extra["plan_iteration"] = iter

	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: plan note write", "slug", noteSlug, "error", err)
		return planUpsert{}, false
	}

	// Adopt agent-cache notes that completed before this plan existed: the
	// session accrued their slugs while no plan slug was known; tag them into
	// the now-minted composition and clear the pending list (the fresh meta
	// below carries none).
	prior, _ := h.sessionPlanMeta(ctx, p.SessionID)
	adopted := h.adoptPendingAgents(ctx, project, planSlug, prior.PendingAgents)
	h.setSessionPlanMeta(ctx, p.SessionID, planCaptureMeta{Basename: basename, PlanSlug: planSlug, Status: status})

	kind := core.EventPlanCaptured
	if approve {
		kind = core.EventPlanApproved
	}
	payload := map[string]any{
		"basename": basename, "plan_slug": planSlug, "iteration": iter,
		"title": events.Truncate(written.Title, h.maxEventChars),
	}
	if trimmed != "" {
		payload["content"] = content // verbatim, unbounded by design
	}
	if adopted > 0 {
		payload["adopted_agents"] = adopted
	}
	h.recordPlanEvent(ctx, kind, p.SessionID, written.ID, payload)
	return planUpsert{note: written, planSlug: planSlug, first: prior.Basename == ""}, true
}

// adoptPendingAgents adds the plan:<slug> tag to agent-cache notes captured
// before the plan's first iteration (the explore-first pattern: subagents
// finish before any plan file exists). Best-effort per note; one that vanished
// or already belongs to a plan is skipped. Returns how many were adopted.
func (h *Handler) adoptPendingAgents(ctx context.Context, project, planSlug string, slugs []string) int {
	adopted := 0
	for _, slug := range slugs {
		note, found := h.loadNoteBySlug(ctx, project, slug)
		if !found || plans.SlugFromTags(note.Tags) != "" {
			continue
		}
		note.Tags = append([]string{plans.SlugTag(planSlug)}, note.Tags...)
		note.Updated = time.Now().UTC()
		if _, err := h.files.WriteNote(ctx, note); err != nil {
			h.logger.Warn("hooks: adopt pending agent note", "slug", slug, "error", err)
			continue
		}
		adopted++
	}
	return adopted
}

// ensurePlanTask creates the "Implement plan" tracking task for an approved
// plan unless the plan already has an open or in-progress step (idempotent on
// re-approval).
func (h *Handler) ensurePlanTask(ctx context.Context, p toolPayload, note core.Note, planSlug string) {
	createdBy := ""
	if p.SessionID != "" {
		createdBy = ambientName(p.SessionID)
	}
	task, created, err := plans.EnsureTask(ctx, h.db, note, planSlug, createdBy)
	if err != nil {
		h.logger.Warn("hooks: plan task", "error", err)
		return
	}
	if !created {
		return
	}
	h.recordPlanEvent(ctx, core.EventTaskTransition, p.SessionID, task.ID, map[string]any{
		"to": string(core.TaskOpen), "created": true, "plan_slug": planSlug,
	})
}

// relatedPlanHits caps how many recall hits the first-capture injection lists.
const relatedPlanHits = 5

// relatedPlanContext builds the additionalContext block returned on a
// session's first captured plan iteration: top recall hits for the plan title
// (prior plans, constraints, related notes), so the planning agent sees prior
// art before the plan is finalized. Returns "" when recall is unavailable,
// errors, or finds nothing beyond the plan's own note; a non-empty block is
// also recorded as a retrieval.injected event.
func (h *Handler) relatedPlanContext(ctx context.Context, p toolPayload, note core.Note) string {
	if h.retrieve == nil || strings.TrimSpace(note.Title) == "" {
		return ""
	}
	hits, err := h.retrieve.Recall(ctx, retrieve.RecallInput{
		Query: note.Title, Project: note.Project, Limit: relatedPlanHits + 1,
	})
	if err != nil {
		h.logger.Warn("hooks: related plan recall", "error", err)
		return ""
	}
	var b strings.Builder
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit.ID == note.ID {
			continue // the plan's own freshly-written note
		}
		if len(ids) == relatedPlanHits {
			break
		}
		read := "memory_read name=" + hit.Name
		if hit.Kind == "note" {
			read = "notes_read id=" + hit.ID
		}
		fmt.Fprintf(&b, "\n- [%s] %s (%s): %s -- %s", hit.Kind, hit.Title, hit.Age, hit.Description, read)
		ids = append(ids, hit.ID)
	}
	if len(ids) == 0 {
		return ""
	}
	block := "<seam-plan-context>\nSeamless has prior knowledge related to this plan; check before finalizing:" +
		b.String() + "\n</seam-plan-context>"
	h.recordInjection(ctx, "post-tool-use", p.SessionID, "", block, ids)
	return block
}

// recordPlanEvent appends a plan-capture event, attributed to the ambient
// session (best-effort) with the Claude session id riding in the payload.
func (h *Handler) recordPlanEvent(ctx context.Context, kind core.EventKind, claudeSessionID, itemID string, payload map[string]any) {
	if h.events == nil {
		return
	}
	sessionID, project := h.ambientRef(ctx, claudeSessionID)
	payload["claude_session_id"] = claudeSessionID
	if _, err := h.events.Record(ctx, core.Event{
		Kind: kind, SessionID: sessionID, ProjectSlug: project, ItemID: itemID, Payload: payload,
	}); err != nil {
		h.logger.Warn("hooks: record plan event", "kind", kind, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Session correlation (plan_capture metadata)
// ---------------------------------------------------------------------------

// planCaptureMeta mirrors core.Session.Metadata["plan_capture"]: the session's
// active plan capture, keyed by the CC plan file basename. PendingAgents holds
// the slugs of agent-cache notes captured before any plan file existed; they
// are adopted into the composition (and the list cleared) at the next capture.
type planCaptureMeta struct {
	Basename      string
	PlanSlug      string
	Status        string
	PendingAgents []string
}

// sessionPlanMeta returns the ambient session's plan_capture metadata.
func (h *Handler) sessionPlanMeta(ctx context.Context, claudeSessionID string) (planCaptureMeta, bool) {
	sess, ok := h.ambientSession(ctx, claudeSessionID)
	if !ok {
		return planCaptureMeta{}, false
	}
	m, ok := sess.Metadata["plan_capture"].(map[string]any)
	if !ok {
		return planCaptureMeta{}, false
	}
	get := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	meta := planCaptureMeta{Basename: get("basename"), PlanSlug: get("plan_slug"), Status: get("status")}
	if raw, ok := m["pending_agents"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				meta.PendingAgents = append(meta.PendingAgents, s)
			}
		}
	}
	return meta, true
}

// setSessionPlanMeta stores meta on the ambient session (best-effort).
func (h *Handler) setSessionPlanMeta(ctx context.Context, claudeSessionID string, meta planCaptureMeta) {
	sess, ok := h.ambientSession(ctx, claudeSessionID)
	if !ok {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	m := map[string]any{
		"basename": meta.Basename, "plan_slug": meta.PlanSlug, "status": meta.Status,
	}
	if len(meta.PendingAgents) > 0 {
		m["pending_agents"] = meta.PendingAgents
	}
	sess.Metadata["plan_capture"] = m
	sess.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSession(ctx, h.db, sess); err != nil {
		h.logger.Warn("hooks: session plan meta", "error", err)
	}
}

// ambientSession looks up the ambient session for a Claude session id.
func (h *Handler) ambientSession(ctx context.Context, claudeSessionID string) (core.Session, bool) {
	if h.db == nil || claudeSessionID == "" {
		return core.Session{}, false
	}
	sess, ok, err := store.SessionByName(ctx, h.db, ambientName(claudeSessionID))
	if err != nil {
		h.logger.Warn("hooks: ambient session lookup", "error", err)
		return core.Session{}, false
	}
	return sess, ok
}

// ---------------------------------------------------------------------------
// Shared note/project helpers
// ---------------------------------------------------------------------------

// resolveProject maps the hook payload cwd to a project slug (best-effort; ""
// scopes to the inbox/global).
func (h *Handler) resolveProject(ctx context.Context, cwd string) string {
	project, err := store.ResolveProjectForCWD(ctx, h.db, cwd)
	if err != nil {
		h.logger.Warn("hooks: plan project resolve", "error", err)
		return ""
	}
	return project
}

// loadNoteBySlug resolves a (project, slug) to the full on-disk note.
func (h *Handler) loadNoteBySlug(ctx context.Context, project, slug string) (core.Note, bool) {
	idx, ok, err := store.NoteBySlug(ctx, h.db, project, slug)
	if err != nil {
		h.logger.Warn("hooks: note lookup", "slug", slug, "error", err)
		return core.Note{}, false
	}
	if !ok {
		return core.Note{}, false
	}
	note, err := h.files.Store().ReadNote(idx.FilePath)
	if err != nil {
		h.logger.Warn("hooks: note read", "slug", slug, "error", err)
		return core.Note{}, false
	}
	return note, true
}

// planFilePath validates that a tool-input path is a .md file directly under
// the plans dir (defense in depth behind the CLI pre-filter) and returns the
// cleaned absolute path.
func (h *Handler) planFilePath(raw string) (string, bool) {
	if h.plansDir == "" || raw == "" {
		return "", false
	}
	path := raw
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		path = filepath.Join(home, path[2:])
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != filepath.Clean(h.plansDir) || filepath.Ext(path) != ".md" {
		return "", false
	}
	if validate.Name(planBasename(path)) != nil {
		return "", false
	}
	return path, true
}

// planBasename is the plan's correlation key: the file name without .md.
func planBasename(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// planStamp is the provenance blockquote prepended to every captured plan body.
func planStamp(claudeSessionID, basename string, iter int, head string, now time.Time) string {
	return fmt.Sprintf("> captured from %s | %s.md | iter %d | git %s | %s",
		stampSession(claudeSessionID), basename, iter, shortHead(head), now.UTC().Format(time.RFC3339))
}

// stampSession names the capturing session in a stamp line.
func stampSession(claudeSessionID string) string {
	if claudeSessionID == "" {
		return "cc/unknown"
	}
	return ambientName(claudeSessionID)
}

// shortHead abbreviates a commit hash for stamps ("unknown" when absent).
func shortHead(head string) string {
	if head == "" {
		return "unknown"
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

// firstHeading returns the text of the first "# " heading line, or "".
func firstHeading(content string) string {
	for line := range strings.Lines(content) {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// gitHead resolves the repo's current commit at cwd by reading .git directly
// (no exec dependency): a detached HEAD is the hash itself; a symbolic ref is
// dereferenced via its loose ref file, then packed-refs, following a
// worktree's gitdir pointer and commondir. Any failure yields "" -- the stamp
// is best-effort by design.
func gitHead(cwd string) string {
	if cwd == "" {
		return ""
	}
	gitDir := filepath.Join(cwd, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		// A worktree/submodule .git file: "gitdir: <path>".
		b, err := os.ReadFile(gitDir)
		if err != nil {
			return ""
		}
		after, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(after)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(cwd, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	after, ok := strings.CutPrefix(ref, "ref:")
	if !ok {
		return ref // detached HEAD: the hash itself
	}
	refName := strings.TrimSpace(after)
	if refName == "" || strings.Contains(refName, "..") {
		return ""
	}
	// Loose ref in the git dir, then its commondir (worktrees), then packed-refs.
	dirs := []string{gitDir}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		dirs = append(dirs, filepath.Clean(common))
	}
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(refName))); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	for _, d := range dirs {
		if hash := packedRef(filepath.Join(d, "packed-refs"), refName); hash != "" {
			return hash
		}
	}
	return ""
}

// packedRef scans a packed-refs file for refName and returns its hash, or "".
func packedRef(path, refName string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if hash, name, ok := strings.Cut(line, " "); ok && name == refName {
			return hash
		}
	}
	return ""
}
