package hooks

// Claude Code plan-mode capture. Plan mode writes its plan to
// ~/.claude/plans/<basename>.md with the ordinary Write/Edit tools, so the
// PostToolUse hook sees every iteration; ExitPlanMode's PostToolUse fires on
// user approval; PermissionRequest[ExitPlanMode] fires when the user is shown
// the plan. Each plan becomes one upserted note (slug cc-plan-<basename>) whose
// lifecycle rides on a plan-status:draft|presented|approved tag, plus verbatim
// plan.captured/presented/approved events. Everything here is best-effort and
// fail-open: a capture problem is logged and the hook still acks 200.
//
// This file is the hook entry points and the capture flow. The note upsert lives
// in plans_note.go, session correlation in plans_session.go, and the plan-file
// vocabulary in plans_stamp.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
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
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero (no tool name -> no capture)

	// Heartbeat the ambient session on any tool activity, so a long turn that never
	// calls a seamless MCP tool still keeps its cc/* session live for the reaper.
	hbCtx, hbCancel := context.WithTimeout(r.Context(), hookTimeout)
	h.touchAmbient(hbCtx, p.SessionID)
	hbCancel()

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
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero (no tool name -> no capture)

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
