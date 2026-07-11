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
	"github.com/0spoon/seamless/internal/store"
)

func TestSessionsPage_ListAndDetail(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	sess := core.Session{
		ID: id, Name: "cc/abcd1234", ProjectSlug: "seamless", Status: core.SessionCompleted,
		Findings: "found the bug in the watcher", Source: "startup", Ambient: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	require.NoError(t, store.CreateSession(ctx, db, sess))

	memID, _ := core.NewID()
	_, err = rec.Record(ctx, core.Event{Kind: core.EventToolCall, SessionID: id, Payload: map[string]any{"tool": "memory_write"}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: id, Payload: map[string]any{"item_ids": []any{memID}}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventMemoryRead, SessionID: id, ItemID: memID, Payload: map[string]any{"name": "watcher-race"}})
	require.NoError(t, err)

	// List
	var list sessionsData
	getJSON(t, mux, "/console/sessions?format=json", &list)
	require.Equal(t, 1, list.Total)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "cc/abcd1234", list.Sessions[0].Name)
	require.True(t, list.Sessions[0].Ambient)

	// Filter by active -> empty (our session is completed)
	var active sessionsData
	getJSON(t, mux, "/console/sessions?status=active&format=json", &active)
	require.Empty(t, active.Sessions)

	// Detail
	var detail sessionDetail
	getJSON(t, mux, "/console/sessions/"+id+"?format=json", &detail)
	require.Equal(t, id, detail.Session.ID)
	require.Equal(t, 1, detail.ToolCalls)
	require.Equal(t, 1, detail.Reads)
	require.Equal(t, 1, detail.Injected)
	require.Equal(t, 1, detail.ReadBack, "injected item was later read -> read-after-inject")
	require.Len(t, detail.Timeline, 3)

	// HTML list renders
	reqL := httptest.NewRequest(http.MethodGet, "/console/sessions", nil)
	reqL.Header.Set("Authorization", "Bearer "+testKey)
	rrL := do(mux, reqL)
	require.Equal(t, http.StatusOK, rrL.Code)
	require.Contains(t, rrL.Body.String(), "cc/abcd1234")

	// HTML detail renders
	req := httptest.NewRequest(http.MethodGet, "/console/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "found the bug in the watcher")
}

func TestSessionDetail_NotFound(t *testing.T) {
	_, mux := newConsole(t)
	req := httptest.NewRequest(http.MethodGet, "/console/sessions/NOSUCHID", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}
