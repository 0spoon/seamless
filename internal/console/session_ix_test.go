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

// TestSessionDetail_InteractionsSurface verifies the session timeline renders as
// the shared IX feed: server-rendered .ix-row rows with copyable request/response
// sections (pre.ix-raw[data-ix-title], upgraded client-side by IX.enhance), the
// kind-filter segments, and an embedded session-scoped volume histogram -- so it
// matches the live Interactions feed and the project-detail interactions tab.
func TestSessionDetail_InteractionsSurface(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	id, err := core.NewID()
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: id, Name: "cc/ixsurface", ProjectSlug: "seamless", Status: core.SessionCompleted,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}))

	// Two interaction-kind events (recorded oldest first) plus one non-interaction
	// event (memory.read) that the IX surface excludes but the counts still tally.
	_, err = rec.Record(ctx, core.Event{
		Kind: core.EventToolCall, SessionID: id,
		Payload: map[string]any{"tool": "memory_write", "args": map[string]any{"name": "watcher-race"}, "result": "ok"},
	})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{
		Kind: core.EventMemoryRead, SessionID: id, ItemID: "01ABC", Payload: map[string]any{"name": "watcher-race"},
	})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{
		Kind: core.EventHookPrompt, SessionID: id,
		Payload: map[string]any{"hook": "user-prompt-submit", "prompt": "why does the watcher race"},
	})
	require.NoError(t, err)

	// JSON projection: interaction rows are the two interaction-kind events, newest
	// first; the memory.read still counts under Reads and stays in the full Timeline.
	var d sessionDetail
	getJSON(t, mux, "/console/sessions/"+id+"?format=json", &d)
	require.Len(t, d.Interactions, 2, "only interaction-kind events become IX rows")
	require.Equal(t, "hook.prompt", d.Interactions[0].Kind, "IX rows are newest first")
	require.Len(t, d.Timeline, 3, "the full timeline still carries every event kind")
	require.Equal(t, 1, d.Reads, "the excluded memory.read is still counted")

	// HTML detail renders the shared IX surface.
	req := httptest.NewRequest(http.MethodGet, "/console/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := do(mux, req)
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `id="ix-sfeed"`, "renders the shared IX feed container")
	require.Contains(t, body, `id="ix-skinds"`, "renders the kind-filter segments")
	require.Contains(t, body, `class="ix-volume`, "renders the volume histogram mount")
	require.Contains(t, body, `data-ix-title="Request"`, "tool args/prompt become a copyable IX section")
	require.Contains(t, body, "memory_write", "the tool label shows on its row")
	require.Contains(t, body, `data-vol="[`, "embeds the session-scoped volume buckets as JSON")
	require.Contains(t, body, `latestId`, "volume buckets carry an event-detail click target")
}
