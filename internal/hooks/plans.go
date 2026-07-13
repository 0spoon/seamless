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
	"strconv"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
	"github.com/0spoon/seamless/internal/validate"
)

// planNotePrefix + the plan file basename is the captured-plan note slug -- the
// correlation key across iterations (a direct NoteBySlug lookup, no tag query).
const planNotePrefix = "cc-plan-"

// Plan lifecycle statuses, stored as a plan-status:<v> note tag.
const (
	planStatusDraft     = "draft"
	planStatusPresented = "presented"
	planStatusApproved  = "approved"
)

// postToolUse captures plan-file iterations (Write/Edit/MultiEdit under the
// plans dir) and plan approvals (ExitPlanMode). The seam CLI pre-filters other
// tools locally, but the daemon still dispatches defensively.
func (h *Handler) postToolUse(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p toolPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	if h.captureEnabled() {
		ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
		defer cancel()
		switch p.ToolName {
		case "Write", "Edit", "MultiEdit":
			h.capturePlanIteration(ctx, p)
		case "ExitPlanMode":
			h.capturePlanApproval(ctx, p)
		}
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
// alike) and upserts the cc-plan note.
func (h *Handler) capturePlanIteration(ctx context.Context, p toolPayload) {
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
		return
	}
	path, ok := h.planFilePath(in.FilePath)
	if !ok {
		return // not a plan file
	}
	content, err := os.ReadFile(path)
	if err != nil {
		h.logger.Warn("hooks: plan capture read", "error", err)
		return
	}
	h.upsertPlanNote(ctx, p, planBasename(path), string(content), false)
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
	note, planSlug, ok := h.upsertPlanNote(ctx, p, basename, content, true)
	if !ok {
		return
	}
	if h.planCapture.AutoTask {
		h.ensurePlanTask(ctx, p, note, planSlug)
	}
}

// markPlanPresented flips the session's draft plan note to presented.
func (h *Handler) markPlanPresented(ctx context.Context, p toolPayload) {
	meta, ok := h.sessionPlanMeta(ctx, p.SessionID)
	if !ok || meta.Basename == "" || meta.Status != planStatusDraft {
		return
	}
	project := h.resolveProject(ctx, p.CWD)
	note, found := h.loadNoteBySlug(ctx, project, planNotePrefix+meta.Basename)
	if !found {
		return
	}
	note.Tags = setPlanStatusTag(note.Tags, planStatusPresented)
	note.Description = planNoteDescription(meta.Basename, notePlanIteration(note), planStatusPresented)
	note.Updated = time.Now().UTC()
	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: plan presented write", "error", err)
		return
	}
	meta.Status = planStatusPresented
	h.setSessionPlanMeta(ctx, p.SessionID, meta)
	h.recordPlanEvent(ctx, core.EventPlanPresented, p.SessionID, written.ID, map[string]any{
		"basename": meta.Basename, "plan_slug": meta.PlanSlug,
	})
}

// upsertPlanNote creates or updates the cc-plan-<basename> note for one plan
// iteration or approval. On update the note id, created time, and plan:<slug>
// tag are preserved (the composition slug is minted once, at first capture);
// the title, body, and status tag follow the latest content. An approval with
// no readable content flips the status of an existing note without touching
// its body; with no existing note it is dropped (fail-open).
func (h *Handler) upsertPlanNote(ctx context.Context, p toolPayload, basename, content string, approve bool) (core.Note, string, bool) {
	project := h.resolveProject(ctx, p.CWD)
	noteSlug := planNotePrefix + basename
	existing, found := h.loadNoteBySlug(ctx, project, noteSlug)

	trimmed := strings.TrimSpace(content)
	if trimmed == "" && !(approve && found) {
		return core.Note{}, "", false
	}

	now := time.Now().UTC()
	note := existing
	iter := 1
	if found {
		iter = notePlanIteration(existing)
		if !approve && trimmed != "" {
			iter++
		}
	} else {
		id, err := core.NewID()
		if err != nil {
			h.logger.Warn("hooks: plan note id", "error", err)
			return core.Note{}, "", false
		}
		note = core.Note{ID: id, Slug: noteSlug, Project: project, Created: now}
	}

	status := planStatusDraft
	if found && planStatusFromTags(existing.Tags) == planStatusApproved {
		status = planStatusApproved // an approved plan never regresses to draft
	}
	if approve {
		status = planStatusApproved
	}

	planSlug := planSlugFromTags(note.Tags)
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
	note.Description = planNoteDescription(basename, iter, status)
	note.Tags = []string{"plan:" + planSlug, "cc-plan", "plan-status:" + status, "created-by:agent"}
	note.Updated = now
	if note.Extra == nil {
		note.Extra = map[string]any{}
	}
	note.Extra["plan_iteration"] = iter

	written, err := h.files.WriteNote(ctx, note)
	if err != nil {
		h.logger.Warn("hooks: plan note write", "slug", noteSlug, "error", err)
		return core.Note{}, "", false
	}

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
	h.recordPlanEvent(ctx, kind, p.SessionID, written.ID, payload)
	return written, planSlug, true
}

// ensurePlanTask creates the "Implement plan" tracking task for an approved
// plan unless the plan already has an open or in-progress step (idempotent on
// re-approval).
func (h *Handler) ensurePlanTask(ctx context.Context, p toolPayload, note core.Note, planSlug string) {
	tasks, err := store.ListTasksForPlan(ctx, h.db, note.Project, "", planSlug)
	if err != nil {
		h.logger.Warn("hooks: plan task list", "error", err)
		return
	}
	for _, t := range tasks {
		if !t.Status.Closed() {
			return
		}
	}
	id, err := core.NewID()
	if err != nil {
		h.logger.Warn("hooks: plan task id", "error", err)
		return
	}
	createdBy := ""
	if p.SessionID != "" {
		createdBy = ambientName(p.SessionID)
	}
	now := time.Now().UTC()
	task := core.Task{
		ID: id, ProjectSlug: note.Project,
		Title: "Implement plan: " + note.Title,
		Body: fmt.Sprintf("Plan note: %s (id %s). Agent caches: notes tagged plan:%s. "+
			"Check git stamps for staleness before trusting caches.", note.Slug, note.ID, planSlug),
		Status: core.TaskOpen, CreatedBy: createdBy, PlanSlug: planSlug,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(ctx, h.db, task); err != nil {
		h.logger.Warn("hooks: plan task create", "error", err)
		return
	}
	h.recordPlanEvent(ctx, core.EventTaskTransition, p.SessionID, id, map[string]any{
		"to": string(core.TaskOpen), "created": true, "plan_slug": planSlug,
	})
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
// active plan capture, keyed by the CC plan file basename.
type planCaptureMeta struct {
	Basename string
	PlanSlug string
	Status   string
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
	return planCaptureMeta{Basename: get("basename"), PlanSlug: get("plan_slug"), Status: get("status")}, true
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
	sess.Metadata["plan_capture"] = map[string]any{
		"basename": meta.Basename, "plan_slug": meta.PlanSlug, "status": meta.Status,
	}
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

// planNoteDescription is the captured-plan note's one-line index text.
func planNoteDescription(basename string, iter int, status string) string {
	return fmt.Sprintf("Captured Claude Code plan %s.md (iteration %d, %s)", basename, iter, status)
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

// notePlanIteration reads the plan_iteration frontmatter (preserved in Extra),
// tolerating the numeric types YAML/JSON round-trips produce.
func notePlanIteration(n core.Note) int {
	switch v := n.Extra["plan_iteration"].(type) {
	case int:
		return max(v, 1)
	case int64:
		return max(int(v), 1)
	case float64:
		return max(int(v), 1)
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return max(i, 1)
		}
	}
	return 1
}

// planSlugFromTags returns the plan:<slug> composition slug, or "".
func planSlugFromTags(tags []string) string {
	for _, t := range tags {
		if after, ok := strings.CutPrefix(t, "plan:"); ok {
			return after
		}
	}
	return ""
}

// planStatusFromTags returns the plan-status:<v> value, or "".
func planStatusFromTags(tags []string) string {
	for _, t := range tags {
		if after, ok := strings.CutPrefix(t, "plan-status:"); ok {
			return after
		}
	}
	return ""
}

// setPlanStatusTag replaces the plan-status:<v> tag, preserving all others.
func setPlanStatusTag(tags []string, status string) []string {
	out := make([]string, 0, len(tags)+1)
	for _, t := range tags {
		if !strings.HasPrefix(t, "plan-status:") {
			out = append(out, t)
		}
	}
	return append(out, "plan-status:"+status)
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
