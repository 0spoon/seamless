package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// seedInteractions records a representative spread of events attributed to
// sessID. TS increase with the minute offset so ordering is deterministic
// (newest = highest).
func seedInteractions(t *testing.T, rec *events.Recorder, sessID string) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	rec0 := func(id string, ts time.Time, kind core.EventKind, session string, p map[string]any) {
		_, err := rec.Record(ctx, core.Event{ID: id, TS: ts, Kind: kind, SessionID: session, ProjectSlug: "demo", Payload: p})
		require.NoError(t, err)
	}
	rec0("01ERR", at(7), core.EventToolCall, sessID, map[string]any{"tool": "memory_read", "is_error": true, "error": "not found", "result": "memory_read: not found", "duration_ms": 3})
	rec0("01SES", at(6), core.EventSessionStarted, sessID, map[string]any{"ambient": true})
	rec0("01TC1", at(5), core.EventToolCall, sessID, map[string]any{"tool": "memory_write", "args": map[string]any{"name": "m1"}, "result": `{"status":"ok"}`, "duration_ms": 12})
	rec0("01TC2", at(4), core.EventToolCall, "", map[string]any{"tool": "recall"}) // import-shaped: no args/result
	rec0("01INJ", at(3), core.EventInjected, sessID, map[string]any{"hook": "user-prompt-submit", "content": "<seam-recall>chroma</seam-recall>", "prompt": "why chroma", "item_ids": []any{"01A"}})
	rec0("01RCL", at(2), core.EventInjected, "", map[string]any{"source": "recall", "query": "q", "item_ids": []any{"01A"}}) // dropped (recall twin)
	rec0("01HKP", at(1), core.EventHookPrompt, sessID, map[string]any{"hook": "user-prompt-submit", "prompt": "weather", "matched": false})
	rec0("01MEM", at(0), core.EventMemoryWritten, "", map[string]any{"name": "x"}) // not an interaction kind
}

func newConsoleWithSession(t *testing.T, name string) (string, *http.ServeMux, *events.Recorder) {
	t.Helper()
	db, mux := newConsole(t)
	rec := events.NewRecorder(db)
	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, store.CreateSession(context.Background(), db, core.Session{
		ID: id, Name: name, ProjectSlug: "demo", Status: core.SessionActive,
		Ambient: true, CreatedAt: now, UpdatedAt: now,
	}))
	seedInteractions(t, rec, id)
	return id, mux, rec
}

func TestInteractions_JSONFiltersAndProjects(t *testing.T) {
	sessID, mux, _ := newConsoleWithSession(t, "cc/testsess")

	var data interactionsData
	getJSON(t, mux, "/console/interactions?format=json", &data)

	// Interaction kinds only; recall-source injected and memory.written excluded.
	// Kept: 01ERR, 01SES, 01TC1, 01TC2, 01INJ, 01HKP.
	require.Len(t, data.Rows, 6)
	// Newest first.
	require.Equal(t, "tool.call", data.Rows[0].Kind)
	require.Equal(t, "01ERR", data.Rows[0].ID)

	byID := map[string]interactionRow{}
	kinds := map[string]int{}
	for _, r := range data.Rows {
		byID[r.ID] = r
		kinds[r.Kind]++
	}
	require.Equal(t, 1, kinds["retrieval.injected"], "recall-source injected twin dropped")
	require.NotContains(t, byID, "01RCL")
	require.NotContains(t, byID, "01MEM")

	// Error tool call: danger tone + isError.
	errRow := byID["01ERR"]
	require.True(t, errRow.IsError)
	require.Equal(t, "danger", errRow.Tone)
	require.Equal(t, "memory_read", errRow.Label)

	// Full tool call: args in request, result in response, attribution resolved.
	tc := byID["01TC1"]
	require.Equal(t, "memory_write", tc.Label)
	require.Contains(t, tc.Request, "m1")
	require.Contains(t, tc.Response, "ok")
	require.Equal(t, sessID, tc.SessionID)
	require.Equal(t, "cc/testsess", tc.SessionName)
	require.True(t, tc.Ambient)
	require.Equal(t, int64(12), tc.DurationMS)

	// Import-shaped tool call tolerated: no args/result.
	imp := byID["01TC2"]
	require.Equal(t, "recall", imp.Label)
	require.Empty(t, imp.Request)
	require.Empty(t, imp.Response)

	// Injection: prompt in request, content in response, one surfaced memory.
	inj := byID["01INJ"]
	require.Contains(t, inj.Request, "why chroma")
	require.Contains(t, inj.Response, "seam-recall")
	require.Equal(t, 1, inj.Items)

	// Recall-miss prompt.
	hp := byID["01HKP"]
	require.Equal(t, "prompt (no recall match)", hp.Summary)
	require.Contains(t, hp.Request, "weather")

	// Small dataset -> no next-page cursor.
	require.Empty(t, data.NextTS)
}

func TestInteractions_CursorAndSincePaging(t *testing.T) {
	_, mux, _ := newConsoleWithSession(t, "cc/testsess")
	injTS := core.FormatTime(time.Date(2026, 7, 12, 0, 3, 0, 0, time.UTC)) // 01INJ

	// Older page (before 01INJ, descending): only 01HKP is older AND an interaction
	// (01RCL is dropped, 01MEM is not an interaction kind).
	var older interactionsData
	getJSON(t, mux, "/console/interactions?format=json&before=01INJ&beforeTs="+injTS, &older)
	require.Len(t, older.Rows, 1)
	require.Equal(t, "01HKP", older.Rows[0].ID)

	// Gap-fill (since 01INJ, ascending oldest-first): 01TC2, 01TC1, 01SES, 01ERR.
	var newer interactionsData
	getJSON(t, mux, "/console/interactions?format=json&since=01INJ&sinceTs="+injTS, &newer)
	ids := []string{}
	for _, r := range newer.Rows {
		ids = append(ids, r.ID)
	}
	require.Equal(t, []string{"01TC2", "01TC1", "01SES", "01ERR"}, ids)
}

func TestInteractions_BoundedHistoryWindow(t *testing.T) {
	_, mux, _ := newConsoleWithSession(t, "cc/testsess")
	after := core.FormatTime(time.Date(2026, 7, 12, 0, 4, 0, 0, time.UTC))

	var data interactionsData
	getJSON(t, mux, "/console/interactions?format=json&afterTs="+after, &data)
	require.Equal(t, after, data.AfterTS)
	require.Len(t, data.Rows, 4)
	require.Equal(t, []string{"01ERR", "01SES", "01TC1", "01TC2"}, []string{
		data.Rows[0].ID, data.Rows[1].ID, data.Rows[2].ID, data.Rows[3].ID,
	})
}

func TestInteractions_HistoryArgumentsFailClosed(t *testing.T) {
	_, mux := newConsole(t)
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/console/interactions?format=json&"+query, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Accept", "application/json")
		return do(mux, req)
	}

	for _, query := range []string{
		"history=", "history=0", "history=-60", "history=abc", "history=2592001",
		"afterTs=not-a-time", "before=01A", "beforeTs=2026-07-12T00%3A00%3A00Z",
		"before=01A&beforeTs=not-a-time",
		"since=01A", "sinceTs=2026-07-12T00%3A00%3A00Z",
		"history=60&afterTs=2026-07-12T00%3A00%3A00Z",
		"history=60&before=01A&beforeTs=2026-07-12T00%3A00%3A00Z",
	} {
		rr := get(query)
		require.Equal(t, http.StatusBadRequest, rr.Code, "query %q must not broaden or change silently", query)
	}

	rr := get("history=300")
	require.Equal(t, http.StatusOK, rr.Code)
	var data interactionsData
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &data))
	require.NotEmpty(t, data.AfterTS)
}

func TestInteractions_PlanCaptureRows(t *testing.T) {
	db, mux := newConsole(t)
	rec := events.NewRecorder(db)
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	rec0 := func(id string, min int, kind core.EventKind, p map[string]any) {
		_, err := rec.Record(ctx, core.Event{
			ID: id, TS: base.Add(time.Duration(min) * time.Minute),
			Kind: kind, ProjectSlug: "demo", Payload: p,
		})
		require.NoError(t, err)
	}
	rec0("01PC1", 0, core.EventPlanCaptured, map[string]any{
		"basename": "clever-stallman", "plan_slug": "my-plan", "iteration": 2,
		"content": "# My Plan\n\nSteps.",
	})
	rec0("01PP1", 1, core.EventPlanPresented, map[string]any{"basename": "clever-stallman"})
	rec0("01PA1", 2, core.EventPlanApproved, map[string]any{
		"basename": "clever-stallman", "content": "# My Plan\n\nFinal.",
	})
	rec0("01SA1", 3, core.EventSubagentCaptured, map[string]any{
		"agent_type": "Explore", "agent_id": "abc123",
		"prompt": "Explore the gardener", "content": "Final report.",
	})

	var data interactionsData
	getJSON(t, mux, "/console/interactions?format=json", &data)
	require.Len(t, data.Rows, 4)
	byID := map[string]interactionRow{}
	for _, r := range data.Rows {
		byID[r.ID] = r
	}

	cap := byID["01PC1"]
	require.Equal(t, "clever-stallman", cap.Label)
	require.Equal(t, "captured plan clever-stallman (iter 2)", cap.Summary)
	require.Contains(t, cap.Response, "# My Plan")
	require.Equal(t, "accent", cap.Tone)

	require.Equal(t, "presented plan clever-stallman", byID["01PP1"].Summary)
	require.Empty(t, byID["01PP1"].Response)

	appr := byID["01PA1"]
	require.Equal(t, "approved plan clever-stallman", appr.Summary)
	require.Contains(t, appr.Response, "Final.")
	require.Equal(t, "ok", appr.Tone)

	sub := byID["01SA1"]
	require.Equal(t, "Explore", sub.Label)
	require.Equal(t, "cached subagent (Explore)", sub.Summary)
	require.Equal(t, "Explore the gardener", sub.Request)
	require.Equal(t, "Final report.", sub.Response)
}

func TestInteractions_HTMLShellRenders(t *testing.T) {
	_, mux, _ := newConsoleWithSession(t, "cc/testsess")
	req := httptest.NewRequest(http.MethodGet, "/console/interactions", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `id="ix-feed"`)
	require.Contains(t, body, "Interactions")
	require.Contains(t, body, `id="ix-history-window"`)
	require.Contains(t, body, `id="ix-history-load"`)
	require.Contains(t, body, `id="ix-empty-load"`)
	require.NotContains(t, body, "Activity range")
	require.NotContains(t, body, `id="ix-volume"`)
	require.NotContains(t, body, `fetch('/console/interactions?format=json',`, "opening the screen must not hydrate historical rows")
	require.Contains(t, body, `last = { ts: new Date().toISOString(), id: '' }`, "the clean feed still anchors reconnect gap-fill")
}

func TestInteractionsClient_SparseRowsHaveNoFalseDisclosure(t *testing.T) {
	js := string(interactionsJS)
	css := string(consoleCSS)

	require.Contains(t, js, "var expandable = !!(d.request || d.response)")
	require.Contains(t, js, "expandable ? 'details' : 'article'")
	require.Contains(t, js, "row.appendChild(staticMeta(d))")
	require.Contains(t, js, "Event details \\u2192")
	require.Contains(t, css, ".ix-static-meta")
	require.Contains(t, css, ".ix-row > summary::after", "expandable rows expose a disclosure cue that static cards omit")
}
