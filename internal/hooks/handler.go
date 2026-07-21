// Package hooks serves the Claude Code SessionStart and UserPromptSubmit hook
// endpoints and installs/removes their entries in a settings.json. Both handlers
// authenticate the same static bearer key as MCP, and both fail open: any
// internal error yields a 200 with empty additionalContext so a broken briefing
// can never block an agent. Only a bad key (401) or an unknown ?client=
// discriminator (400, an install bug rather than a runtime condition) returns
// non-2xx.
package hooks

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// ambientPrefixLen is how many leading chars of the client's external session id
// remain visible in an ambient display name. Identity uses the full id + client;
// the display name adds a stable digest suffix to remain collision-resistant.
const ambientPrefixLen = 8

// ambientDigestBytes contributes 64 stable digest bits to a display name. The
// full external id remains the authoritative store key; this suffix prevents the
// UNIQUE human handle from reintroducing the old truncated-prefix collision.
const ambientDigestBytes = 8

// Config carries the Handler's dependencies. DB backs ambient sessions and the
// session-end harvest; Events may be nil (injection telemetry is then skipped);
// Files may be nil (plan/subagent capture is then skipped). MaxEventChars caps
// captured prompt/findings text (0 = unlimited); injected content is always
// stored in full (it is bounded by the client-aware context policy upstream).
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
	mux.HandleFunc("POST /api/hooks/stop", h.stop)
	mux.HandleFunc("POST /api/hooks/subagent-start", h.subagentStart)
	mux.HandleFunc("POST /api/hooks/post-tool-use", h.postToolUse)
	mux.HandleFunc("POST /api/hooks/subagent-stop", h.subagentStop)
	mux.HandleFunc("POST /api/hooks/permission-request", h.permissionRequest)
}

// hookPayload is the SessionStart request body (tolerant decode; unknown fields
// ignored, empty body OK). The client discriminator is not a body field: it rides
// on the request as the ?client= query param (see adapter.go).
type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Source         string `json:"source"`     // startup|resume|clear|compact
	AgentType      string `json:"agent_type"` // non-empty => subagent
	Model          string `json:"model"`      // Codex sends it; Claude Code does not (see setAmbientModel)
}

type promptPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	UserPrompt     string `json:"user_prompt"` // Codex names this `prompt`; decodePrompt normalizes it
	HookEventName  string `json:"hook_event_name"`
	Model          string `json:"model"` // Codex sends it; Claude Code does not (see setAmbientModel)
}

// endPayload is the SessionEnd request body.
type endPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Reason         string `json:"reason"` // clear|logout|prompt_input_exit|other
}

// stopPayload is the Codex Stop request body. Stop fires at every turn end and,
// for Codex, stands in for the missing SessionEnd: it carries the turn's last
// agent message directly (LastAssistantMessage), with transcript_path as the
// rollout-file fallback when it is absent.
type stopPayload struct {
	SessionID            string `json:"session_id"`
	CWD                  string `json:"cwd"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
	Model                string `json:"model"` // Codex sends it; Claude Code does not (see setAmbientModel)
}

// toolPayload is the tolerant shape of the PostToolUse and PermissionRequest
// request bodies. ToolInput/ToolResponse stay raw: their shape is per-tool, and
// only the plan-capture paths decode the fields they need.
type toolPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"` // plan|default|acceptEdits|...
	ToolName       string          `json:"tool_name"`
	HookEventName  string          `json:"hook_event_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

// subagentPayload is the normalized SubagentStart/SubagentStop shape. Codex
// names session_id as the PARENT session on both events; the child is identified
// separately by agent_id. SubagentStop also distinguishes the parent rollout
// (transcript_path) from the child rollout (agent_transcript_path). Claude Code's
// smaller SubagentStop payload decodes into the same shape with the Codex-only
// fields left empty, preserving its plan-capture path.
type subagentPayload struct {
	ParentSessionID      string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type"`
	CWD                  string `json:"cwd"`
	Model                string `json:"model"`
	PermissionMode       string `json:"permission_mode"`
	HookEventName        string `json:"hook_event_name"`
	TranscriptPath       string `json:"transcript_path"`
	AgentTranscriptPath  string `json:"agent_transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
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
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodeSessionStart(client, readHookBody(w, r))

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
	// Ambient session: create or resume the client/full-id identity so work is recorded
	// without the agent calling session_start. Subagents share the parent's
	// session, so they get no ambient session of their own. Best-effort: a failure
	// just omits the ambient line.
	if p.AgentType == "" {
		if name := h.ensureAmbientSession(ctx, client, p); name != "" {
			briefing = injectAmbientLine(briefing, name)
			h.setAmbientModel(ctx, client, p.SessionID, p.Model, p.TranscriptPath)
		}
	}
	// Cap after the ambient line is appended, then record and serialize the same
	// prepared bytes. A SessionStart carries no user prompt (prompt="").
	h.writeContextResponse(ctx, w, "SessionStart", "session-start", client,
		p.SessionID, "", briefing, injected, injectedIDs)
}

func (h *Handler) sessionEnd(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodeSessionEnd(client, readHookBody(w, r))

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	h.completeClaudeSessions(ctx, client, p)
	// SessionEnd has no hookSpecificOutput variant in Claude Code's schema, so
	// the response is a bare ack -- emitting one fails root validation.
	writeHookAck(w)
}

// stop serves the Stop hook. Stop fires at every turn end. For Codex -- which has
// no SessionEnd event (design D5) -- it is the only end-ish signal, so it both
// heartbeats the ambient session and provisionally harvests the turn's last agent
// message into that session's findings; the idle reaper later expires the session
// with the findings already in place. For any other client it only heartbeats
// (Claude Code harvests on its own SessionEnd, and installs no Stop hook), so a CC
// session's end path is untouched.
func (h *Handler) stop(w http.ResponseWriter, r *http.Request) {
	if !verifyBearer(r, h.apiKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodeStop(client, readHookBody(w, r))

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	// A Stop is proof the agent is alive this turn: heartbeat so the idle reaper
	// does not expire the session mid-work. Always, for every client.
	h.touchAmbient(ctx, client, p.SessionID)
	h.setAmbientModel(ctx, client, p.SessionID, p.Model, p.TranscriptPath)

	if client == ClientCodex {
		h.harvestCodexStop(ctx, p)
	}

	// Stop has no hookSpecificOutput variant in Codex's schema (it can only
	// continue / decision:block / systemMessage), so the response is a bare ack --
	// Stop cannot inject.
	writeHookAck(w)
}

// harvestCodexStop upserts the turn's last agent message onto the Codex ambient
// session as provisional findings. Best-effort by the package's never-block
// contract: a missing session id or DB, a turn with nothing to harvest, or a
// store error all leave the heartbeat above as the Stop's only effect, logged at
// debug rather than surfaced. Repeated Stops converge findings on the latest turn.
func (h *Handler) harvestCodexStop(ctx context.Context, p stopPayload) {
	if h.db == nil || p.SessionID == "" {
		return
	}
	// Real token totals: the rollout carries a cumulative token_count every turn, so
	// re-reading the latest one each Stop and overwriting is idempotent (Codex has no
	// SessionEnd, so this is the only place its usage lands). Independent of findings
	// -- a turn may update one without the other.
	if usage, ok := codexRolloutTokens(p.TranscriptPath); ok {
		if _, err := store.SetAmbientSessionTokens(
			ctx, h.db, ClientCodex.externalIdentity(), p.SessionID, usage, time.Now().UTC(),
		); err != nil {
			h.logger.Warn("hooks: codex stop token harvest", "external_session_id", p.SessionID, "error", err)
		}
	}

	findings := codexStopFindings(p.LastAssistantMessage, p.TranscriptPath)
	if findings == "" {
		return // nothing this turn; the heartbeat kept the session alive
	}
	if _, err := store.UpdateAmbientFindings(
		ctx, h.db, ClientCodex.externalIdentity(), p.SessionID, findings, time.Now().UTC(),
	); err != nil {
		h.logger.Warn("hooks: codex stop harvest", "external_session_id", p.SessionID, "error", err)
	}
}

// harvestClaudeTokens parses the Claude Code transcript for the session's real
// model token totals and overwrites them on the ambient row. Best-effort by the
// package's never-block contract: a blank/unparseable transcript records nothing
// (never an error). It must run while the row is still active -- the caller invokes
// it before the SessionEnd completion flips the status, so SetAmbientSessionTokens'
// active-only guard matches.
func (h *Handler) harvestClaudeTokens(ctx context.Context, client Client, externalSessionID, transcriptPath string, now time.Time) {
	if h.db == nil || externalSessionID == "" {
		return
	}
	usage, ok := claudeTranscriptTokens(transcriptPath)
	if !ok {
		return
	}
	if _, err := store.SetAmbientSessionTokens(
		ctx, h.db, client.externalIdentity(), externalSessionID, usage, now,
	); err != nil {
		h.logger.Warn("hooks: session-end token harvest", "external_session_id", externalSessionID, "error", err)
	}
}

// ensureAmbientSession creates (source startup) or resumes (any source) the
// ambient session for the agent's full external session identity, scoped to the
// cwd-resolved project. It returns the row's display name, or "" when there is
// no session id, no DB, or a store error (best-effort; never blocks the hook).
// The resume path is a store-side targeted UPDATE of only the fields this hook
// owns (status, project scope, recency) -- never a full-row read-modify-write,
// which could clobber the findings a concurrent transcript harvest wrote to the
// same session between the read and the write-back.
func (h *Handler) ensureAmbientSession(ctx context.Context, client Client, p hookPayload) string {
	if h.db == nil || p.SessionID == "" {
		return ""
	}
	externalClient := client.externalIdentity()
	project, err := store.ResolveProjectForCWD(ctx, h.db, p.CWD)
	if err != nil {
		h.logger.Warn("hooks: ambient project resolve", "error", err)
		project = ""
	}

	// Resume: reactivate and re-scope, so a compact/resume continues the same
	// ambient session rather than forking a new one.
	now := time.Now().UTC()
	resumed, found, err := store.ReactivateAmbientSession(
		ctx, h.db, externalClient, p.SessionID, project, now)
	if err != nil {
		h.logger.Warn("hooks: ambient resume", "error", err)
		return ""
	}
	if found {
		return resumed.Name
	}

	name := ambientName(client, p.SessionID)
	id, err := core.NewID()
	if err != nil {
		return ""
	}
	sess := core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
		ExternalSessionID: p.SessionID, ExternalClient: externalClient,
		CWD: p.CWD, Source: p.Source, Ambient: true,
		Metadata: map[string]any{
			"claude_session_id": p.SessionID, "external_client": externalClient,
			"cwd": p.CWD, "source": p.Source,
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(ctx, h.db, sess); err != nil {
		// Two SessionStart hooks racing the same full identity can both miss the
		// resume and collide on its UNIQUE identity/name. The loser resumes the
		// winner's row instead of dropping its ambient line. A display-name
		// collision belonging to another identity remains an error.
		if errors.Is(err, store.ErrSessionIdentityExists) || errors.Is(err, store.ErrSessionNameExists) {
			if existing, ok, rerr := store.ReactivateAmbientSession(
				ctx, h.db, externalClient, p.SessionID, project, now,
			); rerr == nil && ok {
				return existing.Name
			}
		}
		h.logger.Warn("hooks: ambient create", "error", err)
		return ""
	}
	h.recordSession(ctx, core.EventSessionStarted, sess, map[string]any{"ambient": true})
	return name
}

// completeClaudeSessions completes every active session owned by the ending
// client's full external identity: its ambient row (findings harvested from the
// transcript) plus any explicit session_start linked to that client/id pair. Because a graceful
// SessionEnd is a KNOWN end, these close immediately rather than waiting out the idle
// TTL -- the reaper is only for sessions we get no end signal for. Task claims are
// released. It is a no-op when nothing is active (an explicit session_end already
// ran, or a crash left it to the reaper). Never errors: the hook must always 200.
func (h *Handler) completeClaudeSessions(ctx context.Context, client Client, p endPayload) {
	if h.db == nil || p.SessionID == "" {
		return
	}
	sessions, err := store.ActiveSessionsByExternalIdentity(
		ctx, h.db, client.externalIdentity(), p.SessionID)
	if err != nil {
		h.logger.Warn("hooks: session-end lookup", "error", err)
		return
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
			// Overwrite the real token totals while the row is STILL active, before the
			// UpdateSession below flips it to completed -- SetAmbientSessionTokens guards
			// on status = 'active'.
			h.harvestClaudeTokens(ctx, client, p.SessionID, p.TranscriptPath, now)
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

// ambientName constructs a collision-resistant display name for a NEW ambient
// session. It is never used to resolve lifecycle identity: store lookups use the
// full external id plus client. Existing legacy rows keep and return their
// historical names when resumed.
func ambientName(client Client, externalSessionID string) string {
	if externalSessionID == "" {
		return client.ambientPrefix()
	}
	prefix := externalSessionID
	if len(prefix) > ambientPrefixLen {
		prefix = prefix[:ambientPrefixLen]
	}
	digest := sha256.Sum256([]byte(externalSessionID))
	suffix := hex.EncodeToString(digest[:ambientDigestBytes])
	return client.ambientPrefix() + strings.ToLower(prefix) + "-" + suffix
}

// injectAmbientLine adds the "Seam session: cc/<handle> (ambient)" line to a briefing,
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
	client, ok := requireRequestClient(w, r)
	if !ok {
		return
	}
	p := decodePrompt(client, readHookBody(w, r))
	if p.UserPrompt == "" {
		h.writeContextResponse(r.Context(), w, "UserPromptSubmit", "user-prompt-submit",
			client, p.SessionID, "", "", false, nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()

	h.touchAmbient(ctx, client, p.SessionID)
	h.setAmbientModel(ctx, client, p.SessionID, p.Model, p.TranscriptPath)

	out, injectedIDs, err := h.retrieve.PromptRecall(ctx, p.CWD, p.UserPrompt)
	if err != nil {
		h.logger.Warn("hooks: user-prompt-submit recall failed", "error", err)
		out, injectedIDs = "", nil
	}
	if out == "" {
		// A recall miss: capture the prompt as its own hook.prompt event so the
		// Interactions feed can show "why didn't recall fire?" cases.
		h.recordHookPrompt(ctx, client, p.SessionID, p.UserPrompt)
	}
	h.writeContextResponse(ctx, w, "UserPromptSubmit", "user-prompt-submit", client,
		p.SessionID, p.UserPrompt, out, out != "", injectedIDs)
}

// recordInjection logs a retrieval.injected event carrying the exact text that
// was injected, so the console can show what an agent actually received. itemIDs
// are the memory ULIDs surfaced by this injection; recording them (as the recall
// tool does) feeds the read-after-inject funnel so the auto-briefing counts, not
// just recall-tool hits. The event is stamped with the ambient session's seamless
// ULID and project (best-effort) so the Interactions feed can group it under the
// right agent; the Claude session id still rides in the payload. prompt (empty for
// SessionStart) is the user turn that triggered a recall injection; content is the
// full emitted block, stored verbatim with bounded cap metadata.
func (h *Handler) recordInjection(ctx context.Context, hook string, client Client, claudeSessionID, prompt string, prepared preparedHookContext, itemIDs []string) {
	if h.events == nil {
		return
	}
	// A post-assembly cap can remove memory lines without retaining a structured
	// id-to-line map, so on truncation the list is a superset of what reached the
	// model -- item_ids_exact records that. The ids are still credited, because
	// item_ids is the only source of last_injected_at (store.RebuildRetrievalStats),
	// which feeds store.StaleMemories and the gardener's archive proposals.
	// Dropping them makes an actively injected memory read as never injected and
	// proposes archiving it; over-crediting a trimmed line merely keeps it alive
	// one more staleness cycle. Err toward the recoverable direction.
	originalItemCount := len(itemIDs)
	itemIDsExact := !prepared.truncated || originalItemCount == 0
	sessionID, project := h.ambientRef(ctx, client, claudeSessionID)
	payload := map[string]any{
		"hook": hook, "claude_session_id": claudeSessionID,
		"external_client":           client.externalIdentity(),
		"content":                   prepared.content,
		"item_ids":                  itemIDs,
		"item_ids_exact":            itemIDsExact,
		"original_item_count":       originalItemCount,
		"original_estimated_tokens": prepared.originalEstimatedTokens,
		"emitted_estimated_tokens":  prepared.emittedEstimatedTokens,
		"truncated":                 prepared.truncated,
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
func (h *Handler) recordHookPrompt(ctx context.Context, client Client, claudeSessionID, prompt string) {
	if h.events == nil || prompt == "" {
		return
	}
	sessionID, project := h.ambientRef(ctx, client, claudeSessionID)
	if _, err := h.events.Record(ctx, core.Event{
		Kind:        core.EventHookPrompt,
		SessionID:   sessionID,
		ProjectSlug: project,
		Payload: map[string]any{
			"hook": "user-prompt-submit", "claude_session_id": claudeSessionID,
			"external_client": client.externalIdentity(),
			"prompt":          events.Truncate(prompt, h.maxEventChars), "matched": false,
		},
	}); err != nil {
		h.logger.Warn("hooks: record hook prompt", "error", err)
	}
}

// touchAmbient heartbeats the ambient session for a client's full external
// identity, keeping it live for the idle reaper. Every hook that carries
// a session id is proof the agent is alive, so prompt/tool hooks bump updated_at
// between the MCP tool calls that heartbeat it via the connection binding --
// covering a long, quiet turn that makes no seamless MCP calls. Best-effort: a
// no-op when the DB or id is absent, or the session is not active.
func (h *Handler) touchAmbient(ctx context.Context, client Client, claudeSessionID string) {
	if h.db == nil || claudeSessionID == "" {
		return
	}
	if err := store.TouchAmbientSession(
		ctx, h.db, client.externalIdentity(), claudeSessionID, time.Now().UTC(),
	); err != nil {
		h.logger.Warn("hooks: ambient heartbeat", "error", err)
	}
}

// setAmbientModel records which LLM powers the ambient session's agent, stored
// verbatim as the provider names it ("claude-fable-5", "gpt-5.5"). Codex hook
// payloads carry the model directly; Claude Code's do not, so its sessions
// fall back to tail-sniffing the transcript for the last main-thread assistant
// entry's model id. Called on session-start, every prompt, and stop, so a
// mid-session model switch updates the row (SetAmbientSessionModel no-ops when
// the value is unchanged). Best-effort by the package's never-block contract:
// nothing sniffable leaves the previous attribution in place, and a store
// error only logs.
func (h *Handler) setAmbientModel(ctx context.Context, client Client, claudeSessionID, model, transcriptPath string) {
	if h.db == nil || claudeSessionID == "" {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" && client == ClientClaudeCode {
		model = transcriptModel(transcriptPath)
	}
	if model == "" {
		return
	}
	if err := store.SetAmbientSessionModel(
		ctx, h.db, client.externalIdentity(), claudeSessionID, model,
	); err != nil {
		h.logger.Warn("hooks: ambient model", "error", err)
	}
}

// ambientRef resolves the ambient session for a client's full external identity
// to its Seamless ULID and project, best-effort. It returns
// empty strings when the DB is absent, the id is empty, or no such session exists
// -- the event then carries no session attribution rather than failing the hook.
func (h *Handler) ambientRef(ctx context.Context, client Client, claudeSessionID string) (sessionID, project string) {
	if h.db == nil || claudeSessionID == "" {
		return "", ""
	}
	sess, ok, err := store.AmbientSessionByExternalIdentity(
		ctx, h.db, client.externalIdentity(), claudeSessionID)
	if err != nil {
		h.logger.Warn("hooks: ambient ref lookup", "error", err)
		return "", ""
	}
	if !ok {
		return "", ""
	}
	return sess.ID, sess.ProjectSlug
}

// ambientModel returns the model recorded on the client's ambient session, or
// "" when unknown (no DB, no id, no session, or a lookup failure -- logged, per
// ambientRef). Plan capture stamps it onto the notes it writes.
func (h *Handler) ambientModel(ctx context.Context, client Client, claudeSessionID string) string {
	if h.db == nil || claudeSessionID == "" {
		return ""
	}
	sess, ok, err := store.AmbientSessionByExternalIdentity(
		ctx, h.db, client.externalIdentity(), claudeSessionID)
	if err != nil {
		h.logger.Warn("hooks: ambient model lookup", "error", err)
		return ""
	}
	if !ok {
		return ""
	}
	return sess.Model
}

// ambientDisplayName returns the stored human handle for an ambient identity,
// preserving a legacy pre-digest name in provenance. Before a row exists (or on
// a best-effort lookup failure), it falls back to the deterministic new-name
// constructor; neither path is used for lifecycle mutation.
func (h *Handler) ambientDisplayName(ctx context.Context, client Client, externalSessionID string) string {
	if externalSessionID == "" {
		return client.ambientPrefix() + "unknown"
	}
	if h.db != nil {
		sess, ok, err := store.AmbientSessionByExternalIdentity(
			ctx, h.db, client.externalIdentity(), externalSessionID)
		if err != nil {
			h.logger.Warn("hooks: ambient display-name lookup", "error", err)
		} else if ok {
			return sess.Name
		}
	}
	return ambientName(client, externalSessionID)
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

func writePreparedHookResponse(w http.ResponseWriter, event string, prepared preparedHookContext) {
	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // the 200 is already committed; a failed write means the
	// agent hung up and there is no second channel to report it on.
	_ = json.NewEncoder(w).Encode(hookResponse{
		Continue:       true,
		SuppressOutput: true,
		HookSpecificOutput: &hookSpecificOutput{
			HookEventName: event, AdditionalContext: prepared.content,
		},
	})
}

// writeHookAck confirms a hook that carries no additional context. SessionEnd
// (and other events without a hookSpecificOutput variant) must omit that field
// entirely, or Claude Code's schema validation rejects the whole response.
func writeHookAck(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	//nolint:errcheck // see writePreparedHookResponse: the response is already committed.
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
