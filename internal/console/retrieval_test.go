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

	// Two injections, one read -> 50% read-after-inject.
	for range 2 {
		_, err := rec.Record(ctx, core.Event{Kind: core.EventInjected, Payload: map[string]any{"item_ids": []any{m.ID}}})
		require.NoError(t, err)
	}
	_, err := rec.Record(ctx, core.Event{Kind: core.EventMemoryRead, ItemID: m.ID, Payload: map[string]any{"name": m.Name}})
	require.NoError(t, err)

	var data retrievalData
	getJSON(t, mux, "/console/retrieval?format=json", &data)

	require.Equal(t, 2, data.Injections)
	require.Equal(t, 1, data.Reads)
	require.Equal(t, 50, data.ReadRate)
	require.Len(t, data.ByKind, 1)
	require.Equal(t, "gotcha", data.ByKind[0].Kind)
	require.Equal(t, 2, data.ByKind[0].Injects)
	require.Equal(t, 50, data.ByKind[0].ReadRate)
	require.Len(t, data.TopInjected, 1)
	require.Equal(t, m.ID, data.TopInjected[0].ID)
	require.NotEmpty(t, data.Trend, "today's injections should appear in the trend")
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
