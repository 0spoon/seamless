// Package hooks serves the Claude Code SessionStart and UserPromptSubmit hook
// endpoints and installs/removes their entries in a settings.json. Both handlers
// authenticate the same static bearer key as MCP, and both fail open: any
// internal error yields a 200 with empty additionalContext so a broken briefing
// can never block an agent. Only a bad key returns non-2xx (401).
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
)

// hookTimeout bounds briefing/recall assembly so a slow store never stalls the
// agent's turn.
const hookTimeout = 2 * time.Second

// Handler serves the hook endpoints.
type Handler struct {
	retrieve *retrieve.Service
	events   *events.Recorder
	apiKey   string
	logger   *slog.Logger
}

// NewHandler builds a hook Handler. events may be nil (injection telemetry is
// then skipped).
func NewHandler(ret *retrieve.Service, rec *events.Recorder, apiKey string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{retrieve: ret, events: rec, apiKey: apiKey, logger: logger}
}

// Register mounts the hook routes on mux at their full /api/hooks/* paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/hooks/session-start", h.sessionStart)
	mux.HandleFunc("POST /api/hooks/user-prompt-submit", h.userPromptSubmit)
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

// hookResponse is the Claude Code hook response envelope; the field names are
// load-bearing and case-sensitive.
type hookResponse struct {
	Continue           bool               `json:"continue"`
	SuppressOutput     bool               `json:"suppressOutput"`
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
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

	briefing, err := h.retrieve.Briefing(ctx, retrieve.BriefingInput{
		CWD: p.CWD, Source: p.Source, AgentType: p.AgentType,
	})
	if err != nil {
		h.logger.Warn("hooks: session-start briefing failed", "error", err)
		briefing = "" // never block the agent
	}
	if briefing != "" {
		h.recordInjection(ctx, "session-start", p.SessionID)
	}
	writeHookResponse(w, "SessionStart", briefing)
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

	out, err := h.retrieve.PromptRecall(ctx, p.CWD, p.UserPrompt)
	if err != nil {
		h.logger.Warn("hooks: user-prompt-submit recall failed", "error", err)
		out = ""
	}
	if out != "" {
		h.recordInjection(ctx, "user-prompt-submit", p.SessionID)
	}
	writeHookResponse(w, "UserPromptSubmit", out)
}

// recordInjection logs a coarse retrieval.injected event. The session_id column
// holds seamless ULIDs only, so the Claude session id rides in the payload
// rather than that column (the hook has no seamless session in P2).
func (h *Handler) recordInjection(ctx context.Context, hook, claudeSessionID string) {
	if h.events == nil {
		return
	}
	if _, err := h.events.Record(ctx, core.Event{
		Kind:    core.EventInjected,
		Payload: map[string]any{"hook": hook, "claude_session_id": claudeSessionID},
	}); err != nil {
		h.logger.Warn("hooks: record injection", "error", err)
	}
}

func writeHookResponse(w http.ResponseWriter, event, additionalContext string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hookResponse{
		Continue:       true,
		SuppressOutput: true,
		HookSpecificOutput: hookSpecificOutput{
			HookEventName: event, AdditionalContext: additionalContext,
		},
	})
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
