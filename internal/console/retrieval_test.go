package console

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

func TestRetrievalPage_RatesAndLists(t *testing.T) {
	db, mgr, mux := newConsoleWithFiles(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	m := writeMemory(t, mgr, core.KindGotcha, "seamless", "watcher-race", "a pitfall")

	// The panel measures reach: how many distinct memories are surfaced, across how
	// many sessions. One memory surfaced in two sessions -> 2 injections, 1 memory
	// reached, 2 sessions.
	_, err := rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: "sessA", Payload: map[string]any{"item_ids": []any{m.ID}, "emitted_estimated_tokens": 120}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: "sessB", Payload: map[string]any{"item_ids": []any{m.ID}, "emitted_estimated_tokens": 80}})
	require.NoError(t, err)

	var data retrievalData
	getJSON(t, mux, "/console/retrieval?format=json", &data)

	require.Equal(t, 2, data.Injections)
	require.Equal(t, 200, data.InjectedTokens)
	require.Equal(t, 1, data.MemoriesSurfaced)
	require.Equal(t, 2, data.SessionsReached)
	require.Len(t, data.ByKind, 1)
	require.Equal(t, "gotcha", data.ByKind[0].Kind)
	require.Equal(t, 2, data.ByKind[0].Injects)
	require.Equal(t, 1, data.ByKind[0].Memories)
	require.Len(t, data.ByProject, 1)
	require.Equal(t, "seamless", data.ByProject[0].Project)
	require.Equal(t, 1, data.ByProject[0].Surfaced)
	require.Equal(t, 1, data.ByProject[0].Active)
	require.Equal(t, 100, data.ByProject[0].ReachRate)
	require.Equal(t, 2, data.ByProject[0].Injects)
	require.Len(t, data.TopInjected, 1)
	require.Equal(t, m.ID, data.TopInjected[0].ID)
	require.Equal(t, 2, data.TopInjected[0].Sessions)
	require.NotEmpty(t, data.Trend, "today's injections should appear in the trend")
	require.Equal(t, "24h", data.Window, "defaults to the 24h window")
}

func TestRetrievalPage_EmptyRenders(t *testing.T) {
	_, _, mux := newConsoleWithFiles(t)
	var data retrievalData
	getJSON(t, mux, "/console/retrieval?format=json", &data)
	require.Zero(t, data.Injections)
	// StaleMemories over an empty store is empty; ensure no error path.
	require.Empty(t, data.Stale)
	_ = store.MemoryStat{}
}
