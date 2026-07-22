package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
)

func TestEventDetail_InjectionContentAndItems(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	// A memory the injection surfaced, so item_ids resolve to a real row.
	now := core.FormatTime(time.Now().UTC())
	memID := mustID(t)
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories_index
		    (id, kind, name, description, project, file_path, tags, valid_from,
		     invalid_at, superseded_by, source_session, content_hash, created_at, updated_at)
		VALUES (?, 'gotcha', 'watcher-race', 'the file watcher races the index', 'seamless',
		        'memory/seamless/watcher-race.md', '[]', ?, NULL, NULL, '', 'h', ?, ?)`,
		memID, now, now, now)
	require.NoError(t, err)

	id, err := rec.Record(ctx, core.Event{
		Kind: core.EventInjected, ProjectSlug: "seamless",
		Payload: map[string]any{
			"hook":              "user-prompt-submit",
			"claude_session_id": "cc/deadbeef",
			"content":           "<seam-recall>watcher notes</seam-recall>",
			"item_ids":          []any{memID, "MISSINGID"},
		},
	})
	require.NoError(t, err)

	// JSON projection
	var d eventDetailData
	getJSON(t, mux, "/console/events/"+id+"?format=json", &d)
	require.Equal(t, id, d.Event.ID)
	require.Equal(t, "<seam-recall>watcher notes</seam-recall>", d.Content)
	require.Equal(t, "<seam-recall>watcher notes</seam-recall>", d.Trace.Response)
	require.Len(t, d.Items, 2)
	require.Equal(t, "watcher-race", d.Items[0].Name)
	require.False(t, d.Items[0].Missing)
	require.True(t, d.Items[1].Missing, "unresolved id is marked missing")
	// content and item_ids are handled specially, so scalar fields hold the rest.
	keys := map[string]string{}
	for _, f := range d.Fields {
		keys[f.Key] = f.Value
	}
	require.Equal(t, "user-prompt-submit", keys["hook"])
	require.Equal(t, "cc/deadbeef", keys["claude_session_id"])
	require.NotContains(t, keys, "content")
	require.NotContains(t, keys, "item_ids")
	require.Contains(t, d.RawJSON, "seam-recall")

	// HTML renders the injected content and the surfaced memory link.
	req := httptest.NewRequest(http.MethodGet, "/console/events/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `class="event-detail-hero tone-brand"`)
	require.Contains(t, body, `class="event-workspace"`)
	require.Contains(t, body, "Injected context")
	require.Contains(t, body, "seam-recall")
	require.Contains(t, body, "watcher-race")
	require.Contains(t, body, `class="event-memory-grid"`)
	require.Contains(t, body, `href="/console/interactions"`)
}

func TestEventDetail_ToolCallPromotesRequestAndResponse(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	id, err := rec.Record(ctx, core.Event{
		Kind: core.EventToolCall, ProjectSlug: "seamless",
		Payload: map[string]any{
			"tool":        "memory_write",
			"args":        map[string]any{"name": "watcher-race", "kind": "gotcha"},
			"result":      `{"status":"ok"}`,
			"duration_ms": float64(14),
			"is_error":    false,
		},
	})
	require.NoError(t, err)

	var d eventDetailData
	getJSON(t, mux, "/console/events/"+id+"?format=json", &d)
	require.Contains(t, d.Trace.Request, `"name": "watcher-race"`)
	require.Equal(t, `{"status":"ok"}`, d.Trace.Response)
	require.EqualValues(t, 14, d.Trace.DurationMS)
	keys := map[string]string{}
	for _, f := range d.Fields {
		keys[f.Key] = f.Value
	}
	require.NotContains(t, keys, "args")
	require.NotContains(t, keys, "result")
	require.Equal(t, "memory_write", keys["tool"])

	req := httptest.NewRequest(http.MethodGet, "/console/events/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `class="card event-content-card event-request-card"`)
	require.Contains(t, body, `class="card event-content-card event-response-card"`)
	require.Contains(t, body, ">Request</h2>")
	require.Contains(t, body, ">Response</h2>")
	require.Contains(t, body, "watcher-race")
	require.Contains(t, body, "memory_write")
}

func TestEventDetail_HookErrorPromotesFailure(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	id, err := events.NewRecorder(db).Record(ctx, core.Event{
		Kind: core.EventHookError, ProjectSlug: "seamless",
		Payload: map[string]any{
			"stage":  "session-start",
			"client": "claude-code",
			"error":  "briefing assembly failed",
		},
	})
	require.NoError(t, err)

	var d eventDetailData
	getJSON(t, mux, "/console/events/"+id+"?format=json", &d)
	require.True(t, d.Trace.IsError)
	require.Equal(t, "danger", d.Trace.Tone)
	require.Equal(t, "briefing assembly failed", d.Trace.Response)
	for _, f := range d.Fields {
		require.NotEqual(t, "error", f.Key, "promoted failure should not be duplicated in payload fields")
	}

	req := httptest.NewRequest(http.MethodGet, "/console/events/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `class="event-detail-hero tone-danger is-error"`)
	require.Contains(t, body, ">Error</h2>")
	require.Contains(t, body, "briefing assembly failed")
	require.Contains(t, body, "session-start")
}

// A task.transition event's ItemID is a task id, not a memory. It must not be
// resolved against the memory index -- doing so rendered a phantom "removed /
// no longer in the index" surfaced-memory row (the task echoing its own id).
func TestEventDetail_TaskTransitionSurfacesNoMemories(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	taskID := mustID(t)
	id, err := rec.Record(ctx, core.Event{
		Kind: core.EventTaskTransition, ProjectSlug: "seamless", ItemID: taskID,
		Payload: map[string]any{"created": true, "to": "open"},
	})
	require.NoError(t, err)

	var d eventDetailData
	getJSON(t, mux, "/console/events/"+id+"?format=json", &d)
	require.Empty(t, d.Items, "task.transition item id must not resolve as a surfaced memory")

	req := httptest.NewRequest(http.MethodGet, "/console/events/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.NotContains(t, body, "Surfaced memories")
	require.NotContains(t, body, "no longer in the index")
}

func TestEventDetail_NotFound(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/events/NOSUCHID", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	return id
}
