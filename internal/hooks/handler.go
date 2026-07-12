// Package hooks serves the Claude Code SessionStart and UserPromptSubmit hook
// endpoints and installs/removes their entries in a settings.json. Both handlers
// authenticate the same static bearer key as MCP, and both fail open: any
// internal error yields a 200 with empty additionalContext so a broken briefing
// can never block an agent. Only a bad key returns non-2xx (401).
package hooks

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// hookTimeout bounds briefing/recall assembly so a slow store never stalls the
// agent's turn.
const hookTimeout = 2 * time.Second

// ambientPrefixLen is how many leading chars of the Claude session id name an
// ambient session (cc/{prefix}); enough to be unique per machine, short enough
// to read.
const ambientPrefixLen = 8

// Handler serves the hook endpoints.
type Handler struct {
	db       *sql.DB
	retrieve *retrieve.Service
	events   *events.Recorder
	apiKey   string
	logger   *slog.Logger
}

// NewHandler builds a hook Handler. db backs ambient sessions and the session-end
// harvest; events may be nil (injection telemetry is then skipped).
func NewHandler(db *sql.DB, ret *retrieve.Service, rec *events.Recorder, apiKey string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{db: db, retrieve: ret, events: rec, apiKey: apiKey, logger: logger}
}

// Register mounts the hook routes on mux at their full /api/hooks/* paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/hooks/session-start", h.sessionStart)
	mux.HandleFunc("POST /api/hooks/user-prompt-submit", h.userPromptSubmit)
	mux.HandleFunc("POST /api/hooks/session-end", h.sessionEnd)
}

// hookPayload is the SessionStart request body (tolerant decode; unknown fields
// ignored, empty body OK).
type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Source         string `json:"source"`     // startup|resume|clear|compact
	AgentType      string `json:"agent_type"` // non-empty => subagent
}

type promptPayload struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	UserPrompt    string `json:"user_prompt"`
	HookEventName string `json:"hook_event_name"`
}

// endPayload is the SessionEnd request body.
type endPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Reason         string `json:"reason"` // clear|logout|prompt_input_exit|other
}

// hookResponse is the Claude Code hook response envelope; the field names are
// load-bearing and case-sensitive.
type hookResponse struct {
	Continue           bool                `json:"continue"`
	SuppressOutput     bool                `json:"suppressOutput"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func (h *Handler) sessionStart(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p hookPayload
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	// Grow the repo->project map when this agent is working in a not-yet-mapped
	// repo, so the briefing below (and later tool calls) resolve to a real
	// project. Best-effort: a failure just leaves the cwd on the global scope.
	h.retrieve.RegisterProjectForCWD(ctx, p.CWD)

	briefing, injectedIDs, err := h.retrieve.Briefing(ctx, retrieve.BriefingInput{
		CWD: p.CWD, Source: p.Source, AgentType: p.AgentType,
	})
	if err != nil {
		h.logger.Warn("hooks: session-start briefing failed", "error", err)
		briefing, injectedIDs = "", nil // never block the agent
	}
	injected := briefing != ""

	// Ambient session: create or resume cc/{prefix} so work is recorded without
	// the agent calling session_start. Subagents share the parent's session, so
	// they get no ambient session of their own. Best-effort: a failure just omits
	// the ambient line.
	if p.AgentType == "" {
		if name := h.ensureAmbientSession(ctx, p); name != "" {
			briefing = injectAmbientLine(briefing, name)
		}
	}
	// Record after the ambient line is appended so the stored content is exactly
	// what the agent received.
	if injected {
		h.recordInjection(ctx, "session-start", p.SessionID, briefing, injectedIDs)
	}
	writeHookResponse(w, "SessionStart", briefing)
}

func (h *Handler) sessionEnd(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p endPayload
	_ = json.NewDecoder(r.Body).Decode(&p)

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	h.completeAmbientSession(ctx, p)
	// SessionEnd has no hookSpecificOutput variant in Claude Code's schema, so
	// the response is a bare ack -- emitting one fails root validation.
	writeHookAck(w)
}

// ensureAmbientSession creates (source startup) or resumes (any source) the
// ambient session cc/{prefix} for the agent's Claude session id, scoped to the
// cwd-resolved project. It returns the session name, or "" when there is no
// session id, no DB, or a store error (best-effort; never blocks the hook).
func (h *Handler) ensureAmbientSession(ctx context.Context, p hookPayload) string {
	if h.db == nil || p.SessionID == "" {
		return ""
	}
	name := ambientName(p.SessionID)
	project, err := store.ResolveProjectForCWD(ctx, h.db, p.CWD)
	if err != nil {
		h.logger.Warn("hooks: ambient project resolve", "error", err)
		project = ""
	}

	existing, ok, err := store.SessionByName(ctx, h.db, name)
	if err != nil {
		h.logger.Warn("hooks: ambient lookup", "error", err)
		return ""
	}
	now := time.Now().UTC()
	if ok {
		// Resume: reactivate and re-scope, so a compact/resume continues the same
		// ambient session rather than forking a new one.
		existing.Status = core.SessionActive
		if project != "" {
			existing.ProjectSlug = project
		}
		existing.UpdatedAt = now
		if err := store.UpdateSession(ctx, h.db, existing); err != nil {
			h.logger.Warn("hooks: ambient resume", "error", err)
			return ""
		}
		return name
	}

	id, err := core.NewID()
	if err != nil {
		return ""
	}
	sess := core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
		ClaudeSessionID: p.SessionID, CWD: p.CWD, Source: p.Source, Ambient: true,
		Metadata:  map[string]any{"claude_session_id": p.SessionID, "cwd": p.CWD, "source": p.Source},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(ctx, h.db, sess); err != nil {
		h.logger.Warn("hooks: ambient create", "error", err)
		return ""
	}
	h.recordSession(ctx, core.EventSessionStarted, sess, map[string]any{"ambient": true})
	return name
}

// completeAmbientSession harvests findings from the transcript and completes the
// ambient session cc/{prefix}. It is a no-op when the session is absent or was
// already completed (e.g. an explicit session_end ran first). Never errors: the
// hook must always return 200.
func (h *Handler) completeAmbientSession(ctx context.Context, p endPayload) {
	if h.db == nil || p.SessionID == "" {
		return
	}
	sess, ok, err := store.SessionByName(ctx, h.db, ambientName(p.SessionID))
	if err != nil {
		h.logger.Warn("hooks: session-end lookup", "error", err)
		return
	}
	if !ok || sess.Status == core.SessionCompleted {
		return // nothing to harvest, or already ended
	}
	sess.Status = core.SessionCompleted
	sess.Findings = harvestFindings(p.TranscriptPath)
	sess.UpdatedAt = time.Now().UTC()
	if err := store.UpdateSession(ctx, h.db, sess); err != nil {
		h.logger.Warn("hooks: session-end complete", "error", err)
		return
	}
	h.recordSession(ctx, core.EventSessionEnded, sess, map[string]any{"ambient": true, "harvested": true})
}

// ambientName is the ambient session name for a Claude session id: cc/{prefix}.
func ambientName(claudeSessionID string) string {
	prefix := claudeSessionID
	if len(prefix) > ambientPrefixLen {
		prefix = prefix[:ambientPrefixLen]
	}
	return "cc/" + strings.ToLower(prefix)
}

// injectAmbientLine adds the "Seam session: cc/xxxx (ambient)" line to a briefing,
// placing it just before the closing tag, or wrapping a fresh minimal briefing
// when there was none.
func injectAmbientLine(briefing, sessionName string) string {
	line := "Seam session: " + sessionName + " (ambient)"
	if briefing == "" {
		return "<seam-briefing>\n" + line + "\n</seam-briefing>"
	}
	const closeTag = "</seam-briefing>"
	if i := strings.LastIndex(briefing, closeTag); i >= 0 {
		head := strings.TrimRight(briefing[:i], "\n")
		return head + "\n" + line + "\n" + closeTag
	}
	return briefing + "\n" + line
}

func (h *Handler) userPromptSubmit(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p promptPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	if p.UserPrompt == "" {
		writeHookResponse(w, "UserPromptSubmit", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	out, injectedIDs, err := h.retrieve.PromptRecall(ctx, p.CWD, p.UserPrompt)
	if err != nil {
		h.logger.Warn("hooks: user-prompt-submit recall failed", "error", err)
		out, injectedIDs = "", nil
	}
	if out != "" {
		h.recordInjection(ctx, "user-prompt-submit", p.SessionID, out, injectedIDs)
	}
	writeHookResponse(w, "UserPromptSubmit", out)
}

// recordInjection logs a retrieval.injected event carrying the exact text that
// was injected, so the console can show what an agent actually received. itemIDs
// are the memory ULIDs surfaced by this injection; recording them (as the recall
// tool does) feeds the read-after-inject funnel so the auto-briefing counts, not
// just recall-tool hits. The session_id column holds seamless ULIDs only, so the
// Claude session id rides in the payload rather than that column (the hook has no
// seamless session in P2).
func (h *Handler) recordInjection(ctx context.Context, hook, claudeSessionID, content string, itemIDs []string) {
	if h.events == nil {
		return
	}
	if _, err := h.events.Record(ctx, core.Event{
		Kind: core.EventInjected,
		Payload: map[string]any{
			"hook": hook, "claude_session_id": claudeSessionID,
			"content": content, "item_ids": itemIDs,
		},
	}); err != nil {
		h.logger.Warn("hooks: record injection", "error", err)
	}
}

// recordSession appends a session lifecycle event for an ambient session.
func (h *Handler) recordSession(ctx context.Context, kind core.EventKind, sess core.Session, payload map[string]any) {
	if h.events == nil {
		return
	}
	if _, err := h.events.Record(ctx, core.Event{
		Kind: kind, SessionID: sess.ID, ProjectSlug: sess.ProjectSlug, Payload: payload,
	}); err != nil {
		h.logger.Warn("hooks: record session event", "kind", kind, "error", err)
	}
}

func writeHookResponse(w http.ResponseWriter, event, additionalContext string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hookResponse{
		Continue:       true,
		SuppressOutput: true,
		HookSpecificOutput: &hookSpecificOutput{
			HookEventName: event, AdditionalContext: additionalContext,
		},
	})
}

// writeHookAck confirms a hook that carries no additional context. SessionEnd
// (and other events without a hookSpecificOutput variant) must omit that field
// entirely, or Claude Code's schema validation rejects the whole response.
func writeHookAck(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hookResponse{Continue: true, SuppressOutput: true})
}

// verifyBearer constant-time-compares the request's bearer token to key.
func verifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(key)) == 1
}
