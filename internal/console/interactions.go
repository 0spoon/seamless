package console

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// interactionKinds are the event kinds the Interactions feed surfaces: live MCP
// tool calls, recall-miss prompts, hook injections, session lifecycle, and the
// plan-mode capture stream.
var interactionKinds = []core.EventKind{
	core.EventToolCall, core.EventHookPrompt, core.EventInjected,
	core.EventSessionStarted, core.EventSessionEnded,
	core.EventPlanCaptured, core.EventPlanPresented, core.EventPlanApproved,
	core.EventSubagentCaptured,
}

// isInteraction reports whether an event belongs on the Interactions feed.
func isInteraction(e core.Event) bool {
	switch e.Kind {
	case core.EventToolCall, core.EventHookPrompt, core.EventInjected,
		core.EventSessionStarted, core.EventSessionEnded,
		core.EventPlanCaptured, core.EventPlanPresented, core.EventPlanApproved,
		core.EventSubagentCaptured:
		return true
	}
	return false
}

// skipInteraction drops rows that would duplicate another feed row. A recall via
// the MCP tool records BOTH a retrieval.injected (source=recall) and a tool.call;
// the tool.call carries the same content plus its args, so the injected twin is
// dropped. Session lifecycle twins (an MCP session_start's session.started plus
// its tool.call) are kept deliberately, as feed markers.
func skipInteraction(e core.Event) bool {
	return e.Kind == core.EventInjected && payloadStr(e.Payload, "source") == "recall"
}

// interactionRow is a display-ready projection of one Interactions event: enough
// to render the summary line and the request/response bodies without a second
// fetch. It is JSON-tagged so the screen's JS (and the CLI) consume it directly.
type interactionRow struct {
	ID          string    `json:"id"`
	TS          time.Time `json:"ts"`
	Kind        string    `json:"kind"`
	Tone        string    `json:"tone"`
	Label       string    `json:"label"`
	Summary     string    `json:"summary"`
	Project     string    `json:"project,omitempty"`
	SessionID   string    `json:"sessionId,omitempty"`
	SessionName string    `json:"sessionName,omitempty"`
	Ambient     bool      `json:"ambient,omitempty"`
	IsError     bool      `json:"isError,omitempty"`
	DurationMS  int64     `json:"durationMs,omitempty"`
	Request     string    `json:"request,omitempty"`  // pretty-JSON tool args, or prompt text
	Response    string    `json:"response,omitempty"` // tool result / injected content / findings
	Items       int       `json:"items,omitempty"`    // count of surfaced memories
}

// toInteractionRow projects an event into a feed row. name resolves a session id
// to its (name, ambient) pair (memoized by the caller). It tolerates import-shaped
// tool.call payloads that carry no args/result.
func toInteractionRow(e core.Event, name func(string) (string, bool)) interactionRow {
	p := e.Payload
	row := interactionRow{
		ID: e.ID, TS: e.TS, Kind: string(e.Kind),
		Tone: evtTone(string(e.Kind)), Summary: eventSummary(e),
		Project: e.ProjectSlug, SessionID: e.SessionID,
		Items: len(injectedEventItemIDs(e)),
	}
	if isErr, _ := p["is_error"].(bool); isErr {
		row.IsError = true
		row.Tone = "danger"
	}
	if e.SessionID != "" && name != nil {
		row.SessionName, row.Ambient = name(e.SessionID)
	}
	if d, ok := p["duration_ms"].(float64); ok {
		row.DurationMS = int64(d)
	}
	switch e.Kind {
	case core.EventToolCall:
		row.Label = payloadStr(p, "tool")
		row.Request = prettyArgs(p["args"])
		if r := payloadStr(p, "result"); r != "" {
			row.Response = r
		} else if row.IsError {
			row.Response = payloadStr(p, "error")
		}
	case core.EventHookPrompt:
		row.Label = payloadStr(p, "hook")
		row.Request = payloadStr(p, "prompt")
	case core.EventInjected:
		row.Label = payloadStr(p, "hook")
		row.Request = payloadStr(p, "prompt")
		row.Response = payloadStr(p, "content")
	case core.EventSessionStarted:
		row.Label = "session"
	case core.EventSessionEnded:
		row.Label = "session"
		row.Response = payloadStr(p, "findings")
	case core.EventPlanCaptured, core.EventPlanApproved:
		row.Label = payloadStr(p, "basename")
		row.Response = payloadStr(p, "content")
	case core.EventPlanPresented:
		row.Label = payloadStr(p, "basename")
	case core.EventSubagentCaptured:
		row.Label = payloadStr(p, "agent_type")
		row.Request = payloadStr(p, "prompt")
		row.Response = payloadStr(p, "content")
	}
	return row
}

// prettyArgs renders a tool.call's args map as indented JSON, or "" when absent.
func prettyArgs(v any) string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// sessionNamer returns a memoized session-id -> (name, ambient) resolver bound to
// ctx, so a feed of many rows sharing a session costs one query. Unknown ids are
// negatively cached and resolve to ("", false).
func (s *Service) sessionNamer(ctx context.Context) func(string) (string, bool) {
	cache := map[string]core.Session{}
	return func(id string) (string, bool) {
		if id == "" {
			return "", false
		}
		if sess, ok := cache[id]; ok {
			return sess.Name, sess.Ambient
		}
		sess, ok, err := store.SessionByID(ctx, s.cfg.DB, id)
		if err != nil || !ok {
			cache[id] = core.Session{} // negative cache
			return "", false
		}
		cache[id] = sess
		return sess.Name, sess.Ambient
	}
}

// interactionsData is the Interactions screen payload (also the JSON endpoint the
// screen's JS polls). NextTS/NextID cursor the next (older) page.
type interactionsData struct {
	Rows   []interactionRow `json:"rows"`
	NextTS string           `json:"nextTs,omitempty"`
	NextID string           `json:"nextId,omitempty"`
}

// interactionsPageLimit bounds one page / gap-fill batch of the feed.
const interactionsPageLimit = 200

// interactions serves the live transport feed: an HTML shell (default) that its
// JS hydrates from this same handler's JSON (?format=json), paging older via
// before/beforeTs and gap-filling newer via since/sinceTs after an SSE drop.
func (s *Service) interactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.cfg.Events == nil {
		http.Error(w, "interactions unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	name := s.sessionNamer(ctx)

	gapFill := q.Get("since") != "" || q.Get("sinceTs") != ""
	var evs []core.Event
	var err error
	if gapFill {
		evs, err = s.cfg.Events.ByKindsSince(ctx, interactionKinds, q.Get("sinceTs"), q.Get("since"), interactionsPageLimit)
	} else {
		evs, err = s.cfg.Events.ByKinds(ctx, interactionKinds, q.Get("beforeTs"), q.Get("before"), interactionsPageLimit)
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	rows := make([]interactionRow, 0, len(evs))
	for _, e := range evs {
		if skipInteraction(e) {
			continue
		}
		rows = append(rows, toInteractionRow(e, name))
	}
	data := interactionsData{Rows: rows}
	// Older-page cursor: the last (oldest) event of a full descending page. Based
	// on the raw fetch, not the filtered rows, so paging never stalls on a page
	// that was all-skipped.
	if !gapFill && len(evs) == interactionsPageLimit {
		last := evs[len(evs)-1]
		data.NextTS = core.FormatTime(last.TS)
		data.NextID = last.ID
	}
	s.render(w, r, "interactions", pageData{Title: "Interactions", Active: "interactions", Data: data})
}
