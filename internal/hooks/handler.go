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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// hookTimeout bounds briefing/recall assembly so a slow store never stalls the
// agent's turn.
const hookTimeout = 2 * time.Second

// captureTimeout bounds the plan/subagent capture endpoints. They read files
// and write notes (which may embed synchronously), so they get more headroom
// than the briefing path; the installed hook timeout is 10s.
const captureTimeout = 8 * time.Second

// maxHookBody caps every hook request body. PostToolUse payloads carry the
// full tool_input (a plan-file Write includes the whole file), so the cap is
// generous; anything larger is not a payload Seamless should buffer.
const maxHookBody = 8 << 20

// ambientPrefixLen is how many leading chars of the Claude session id name an
// ambient session (cc/{prefix}); enough to be unique per machine, short enough
// to read.
const ambientPrefixLen = 8

// Config carries the Handler's dependencies. DB backs ambient sessions and the
// session-end harvest; Events may be nil (injection telemetry is then skipped);
// Files may be nil (plan/subagent capture is then skipped). MaxEventChars caps
// captured prompt/findings text (0 = unlimited); injected content is always
// stored in full (it is already bounded by the briefing/recall budgets upstream).
// PlansDir is where Claude Code writes plan-mode files; empty defaults to
// ~/.claude/plans (tests override it).
type Config struct {
	DB            *sql.DB
	Retrieve      *retrieve.Service
	Events        *events.Recorder
	Files         *files.Manager
	APIKey        string
	MaxEventChars int
	PlanCapture   config.PlanCapture
	PlansDir      string
	Logger        *slog.Logger
}

// Handler serves the hook endpoints.
type Handler struct {
	db            *sql.DB
	retrieve      *retrieve.Service
	events        *events.Recorder
	files         *files.Manager
	apiKey        string
	maxEventChars int
	planCapture   config.PlanCapture
	plansDir      string
	logger        *slog.Logger
}

// NewHandler builds a hook Handler from cfg.
func NewHandler(cfg Config) *Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PlansDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.PlansDir = filepath.Join(home, ".claude", "plans")
		}
	}
	return &Handler{
		db: cfg.DB, retrieve: cfg.Retrieve, events: cfg.Events, files: cfg.Files,
		apiKey: cfg.APIKey, maxEventChars: cfg.MaxEventChars,
		planCapture: cfg.PlanCapture, plansDir: cfg.PlansDir, logger: cfg.Logger,
	}
}

// Register mounts the hook routes on mux at their full /api/hooks/* paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/hooks/session-start", h.sessionStart)
	mux.HandleFunc("POST /api/hooks/user-prompt-submit", h.userPromptSubmit)
	mux.HandleFunc("POST /api/hooks/session-end", h.sessionEnd)
	mux.HandleFunc("POST /api/hooks/post-tool-use", h.postToolUse)
	mux.HandleFunc("POST /api/hooks/subagent-stop", h.subagentStop)
	mux.HandleFunc("POST /api/hooks/permission-request", h.permissionRequest)
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

// toolPayload is the tolerant shape of the PostToolUse, PermissionRequest, and
// SubagentStop request bodies. ToolInput/ToolResponse stay raw: their shape is
// per-tool, and only the plan-capture paths decode the fields they need.
type toolPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"` // plan|default|acceptEdits|...
	ToolName       string          `json:"tool_name"`
	HookEventName  string          `json:"hook_event_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	AgentID        string          `json:"agent_id"`   // SubagentStop only
	AgentType      string          `json:"agent_type"` // SubagentStop only
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
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p hookPayload
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	// Grow the repo->project map when this agent is working in a not-yet-mapped
	// repo, so the briefing below (and later tool calls) resolve to a real
	// project. Best-effort by the package's never-block-the-agent contract: on a
	// failure the mapping is simply not written, and every later cwd resolution
	// falls back to the global scope. That fallback is not desirable, only
	// preferable to failing session start, so it is logged rather than hidden --
	// the explicit session_start tool resolves the same cwd and does surface the
	// error, and that is the path where a wrong binding actually sticks.
	if _, err := store.RegisterProjectForCWD(ctx, h.db, p.CWD); err != nil {
		h.logger.Warn("hooks: register project for cwd; scope falls back to global", "cwd", p.CWD, "error", err)
	}

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
	// what the agent received. A SessionStart carries no user prompt (prompt="").
	if injected {
		h.recordInjection(ctx, "session-start", p.SessionID, "", briefing, injectedIDs)
	}
	writeHookResponse(w, "SessionStart", briefing)
}

func (h *Handler) sessionEnd(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p endPayload
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero (no session id -> no-op)

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	h.completeClaudeSessions(ctx, p)
	// SessionEnd has no hookSpecificOutput variant in Claude Code's schema, so
	// the response is a bare ack -- emitting one fails root validation.
	writeHookAck(w)
}

// ensureAmbientSession creates (source startup) or resumes (any source) the
// ambient session cc/{prefix} for the agent's Claude session id, scoped to the
// cwd-resolved project. It returns the session name, or "" when there is no
// session id, no DB, or a store error (best-effort; never blocks the hook).
// The resume path is a store-side targeted UPDATE of only the fields this hook
// owns (status, project scope, recency) -- never a full-row read-modify-write,
// which could clobber the findings a concurrent transcript harvest wrote to the
// same session between the read and the write-back.
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

	// Resume: reactivate and re-scope, so a compact/resume continues the same
	// ambient session rather than forking a new one.
	now := time.Now().UTC()
	resumed, err := store.ReactivateSessionByName(ctx, h.db, name, project, now)
	if err != nil {
		h.logger.Warn("hooks: ambient resume", "error", err)
		return ""
	}
	if resumed {
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
		// Two SessionStart hooks racing the same Claude session can both miss the
		// resume and collide on the UNIQUE session name; the loser resumes the
		// winner's row instead of dropping its ambient line.
		if resumed, rerr := store.ReactivateSessionByName(ctx, h.db, name, project, now); rerr == nil && resumed {
			return name
		}
		h.logger.Warn("hooks: ambient create", "error", err)
		return ""
	}
	h.recordSession(ctx, core.EventSessionStarted, sess, map[string]any{"ambient": true})
	return name
}

// completeClaudeSessions completes every active session owned by the ending Claude
// session: its ambient cc/{prefix} (findings harvested from the transcript) plus any
// explicit session_start that linked to it via claude_session_id. Because a graceful
// SessionEnd is a KNOWN end, these close immediately rather than waiting out the idle
// TTL -- the reaper is only for sessions we get no end signal for. Task claims are
// released. It is a no-op when nothing is active (an explicit session_end already
// ran, or a crash left it to the reaper). Never errors: the hook must always 200.
func (h *Handler) completeClaudeSessions(ctx context.Context, p endPayload) {
	if h.db == nil || p.SessionID == "" {
		return
	}
	sessions, err := store.ActiveSessionsByClaudeID(ctx, h.db, p.SessionID)
	if err != nil {
		h.logger.Warn("hooks: session-end lookup", "error", err)
		return
	}
	if len(sessions) == 0 {
		// Legacy ambient rows may predate claude_session_id linking; fall back to the
		// name key so a graceful end still harvests the ambient.
		if amb, ok, aerr := store.SessionByName(ctx, h.db, ambientName(p.SessionID)); aerr != nil {
			h.logger.Warn("hooks: session-end ambient fallback", "error", aerr)
			return
		} else if ok && amb.Status == core.SessionActive {
			sessions = []core.Session{amb}
		}
	}

	now := time.Now().UTC()
	harvested := "" // harvest the transcript once, lazily, only when an ambient needs it
	for _, sess := range sessions {
		sess.Status = core.SessionCompleted
		sess.UpdatedAt = now
		if sess.Ambient {
			if harvested == "" {
				harvested = harvestFindings(p.TranscriptPath)
			}
			sess.Findings = harvested // explicit sessions keep their session_update findings
		}
		if err := store.UpdateSession(ctx, h.db, sess); err != nil {
			h.logger.Warn("hooks: session-end complete", "session", sess.ID, "error", err)
			continue
		}
		released, rerr := store.ReleaseClaimsForSession(ctx, h.db, sess.ID, now)
		if rerr != nil {
			h.logger.Warn("hooks: session-end release claims", "session", sess.ID, "error", rerr)
		}
		h.recordSession(ctx, core.EventSessionEnded, sess, map[string]any{
			"ambient": sess.Ambient, "harvested": sess.Ambient, "linked": !sess.Ambient,
			"reason": p.Reason, "claims_released": released,
			"findings": events.Truncate(sess.Findings, h.maxEventChars),
		})
	}
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
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	var p promptPayload
	_ = json.NewDecoder(r.Body).Decode(&p) // tolerant: a decode error just leaves p zero (no prompt -> empty response)
	if p.UserPrompt == "" {
		writeHookResponse(w, "UserPromptSubmit", "")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	h.touchAmbient(ctx, p.SessionID)

	out, injectedIDs, err := h.retrieve.PromptRecall(ctx, p.CWD, p.UserPrompt)
	if err != nil {
		h.logger.Warn("hooks: user-prompt-submit recall failed", "error", err)
		out, injectedIDs = "", nil
	}
	if out != "" {
		h.recordInjection(ctx, "user-prompt-submit", p.SessionID, p.UserPrompt, out, injectedIDs)
	} else {
		// A recall miss: capture the prompt as its own hook.prompt event so the
		// Interactions feed can show "why didn't recall fire?" cases.
		h.recordHookPrompt(ctx, p.SessionID, p.UserPrompt)
	}
	writeHookResponse(w, "UserPromptSubmit", out)
}

// recordInjection logs a retrieval.injected event carrying the exact text that
// was injected, so the console can show what an agent actually received. itemIDs
// are the memory ULIDs surfaced by this injection; recording them (as the recall
// tool does) feeds the read-after-inject funnel so the auto-briefing counts, not
// just recall-tool hits. The event is stamped with the ambient session's seamless
// ULID and project (best-effort) so the Interactions feed can group it under the
// right agent; the Claude session id still rides in the payload. prompt (empty for
// SessionStart) is the user turn that triggered a recall injection; content is the
// full injected block, stored verbatim.
func (h *Handler) recordInjection(ctx context.Context, hook, claudeSessionID, prompt, content string, itemIDs []string) {
	if h.events == nil {
		return
	}
	sessionID, project := h.ambientRef(ctx, claudeSessionID)
	payload := map[string]any{
		"hook": hook, "claude_session_id": claudeSessionID,
		"content": content, "item_ids": itemIDs,
	}
	if prompt != "" {
		payload["prompt"] = events.Truncate(prompt, h.maxEventChars)
	}
	if _, err := h.events.Record(ctx, core.Event{
		Kind:        core.EventInjected,
		SessionID:   sessionID,
		ProjectSlug: project,
		Payload:     payload,
	}); err != nil {
		h.logger.Warn("hooks: record injection", "error", err)
	}
}

// recordHookPrompt logs a hook.prompt event for a UserPromptSubmit that matched
// no memory -- the recall-miss case. Matched prompts ride on the retrieval.injected
// event instead (no duplicate row), so BuildRetrievalReport (which counts injected
// rows) is unaffected. Session stamping mirrors recordInjection.
func (h *Handler) recordHookPrompt(ctx context.Context, claudeSessionID, prompt string) {
	if h.events == nil || prompt == "" {
		return
	}
	sessionID, project := h.ambientRef(ctx, claudeSessionID)
	if _, err := h.events.Record(ctx, core.Event{
		Kind:        core.EventHookPrompt,
		SessionID:   sessionID,
		ProjectSlug: project,
		Payload: map[string]any{
			"hook": "user-prompt-submit", "claude_session_id": claudeSessionID,
			"prompt": events.Truncate(prompt, h.maxEventChars), "matched": false,
		},
	}); err != nil {
		h.logger.Warn("hooks: record hook prompt", "error", err)
	}
}

// touchAmbient heartbeats the ambient session (cc/{prefix}) for a Claude session
// id, keeping it live for the idle reaper. Every hook that carries a session id is
// proof the agent is alive, so prompt/tool hooks bump updated_at between the MCP
// tool calls that heartbeat it via the connection binding -- covering a long, quiet
// turn that makes no seamless MCP calls. Best-effort: a no-op when the DB or id is
// absent, or the session is not active.
func (h *Handler) touchAmbient(ctx context.Context, claudeSessionID string) {
	if h.db == nil || claudeSessionID == "" {
		return
	}
	if err := store.TouchSessionByName(ctx, h.db, ambientName(claudeSessionID), time.Now().UTC()); err != nil {
		h.logger.Warn("hooks: ambient heartbeat", "error", err)
	}
}

// ambientRef resolves the ambient session (cc/{prefix}) for a Claude session id
// to its seamless ULID and project, best-effort. It returns empty strings when
// the DB is absent, the id is empty, or no such session exists -- the event then
// carries no session attribution rather than failing the hook.
func (h *Handler) ambientRef(ctx context.Context, claudeSessionID string) (sessionID, project string) {
	if h.db == nil || claudeSessionID == "" {
		return "", ""
	}
	sess, ok, err := store.SessionByName(ctx, h.db, ambientName(claudeSessionID))
	if err != nil {
		h.logger.Warn("hooks: ambient ref lookup", "error", err)
		return "", ""
	}
	if !ok {
		return "", ""
	}
	return sess.ID, sess.ProjectSlug
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
