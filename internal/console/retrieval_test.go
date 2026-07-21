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
	require.Equal(t, 1, data.ByProject[0].Created, "the just-written memory is in-window churn")
	require.Equal(t, 1, data.CreatedInWindow)
	require.Zero(t, data.RetiredInWindow)

	// The churn annotation renders beside the denominator (hero + project row).
	body := getHTMLBody(t, mux, "/console/retrieval")
	require.Contains(t, body, "+1 new")
	require.Len(t, data.TopInjected, 1)
	require.Equal(t, m.ID, data.TopInjected[0].ID)
	require.Equal(t, 2, data.TopInjected[0].Sessions)
	require.NotEmpty(t, data.Trend, "today's injections should appear in the trend")
	require.Equal(t, "24h", data.Window, "defaults to the 24h window")
}

// Loop health: demand rate counts briefed memories that were also pulled,
// waste share prices the ones that never are, the miss rate tracks prompts the
// knowledge base could not answer, and dead weight names the offenders.
func TestRetrievalPage_LoopHealth(t *testing.T) {
	db, mgr, mux := newConsoleWithFiles(t)
	ctx := context.Background()
	rec := events.NewRecorder(db)

	wanted := writeMemory(t, mgr, core.KindGotcha, "seamless", "wanted-memory", "gets pulled")
	noise := writeMemory(t, mgr, core.KindGotcha, "seamless", "noise-memory", "never pulled")

	// Both briefed (120 + 80 tokens); only wanted-memory earns demand.
	_, err := rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: "sessA",
		Payload: map[string]any{"item_ids": []any{wanted.ID}, "hook": "session-start", "emitted_estimated_tokens": 120}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: "sessA",
		Payload: map[string]any{"item_ids": []any{noise.ID}, "hook": "session-start", "emitted_estimated_tokens": 80}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventInjected, SessionID: "sessA",
		Payload: map[string]any{"item_ids": []any{wanted.ID}, "source": "recall"}})
	require.NoError(t, err)
	_, err = rec.Record(ctx, core.Event{Kind: core.EventHookPrompt, SessionID: "sessA",
		Payload: map[string]any{"prompt": "a question nothing matched", "matched": false}})
	require.NoError(t, err)

	var data retrievalData
	getJSON(t, mux, "/console/retrieval?format=json", &data)
	require.Equal(t, 2, data.BriefingSurfaced)
	require.Equal(t, 1, data.DemandedOfSurfaced)
	require.Equal(t, 50, data.DemandRate)
	require.Equal(t, 40, data.WasteShare, "80 of 200 injected tokens went to the undemanded memory")
	require.Equal(t, 1, data.RecallMisses)
	require.Zero(t, data.PromptMatches)
	require.Equal(t, 100, data.MissRate)
	require.Len(t, data.DeadWeight, 1)
	require.Equal(t, noise.ID, data.DeadWeight[0].ID)

	body := getHTMLBody(t, mux, "/console/retrieval")
	require.Contains(t, body, "Demand rate")
	require.Contains(t, body, "Waste share")
	require.Contains(t, body, "Dead weight")
	require.Contains(t, body, "noise-memory")

	// The memories library accepts the utility sort and, once the page-load
	// rebuild has run, the detail payload carries the score with its breakdown.
	var mems memoriesData
	getJSON(t, mux, "/console/memories?sort=utility&format=json", &mems)
	var detail memoryDetail
	getJSON(t, mux, "/console/memories/"+wanted.ID+"?format=json", &detail)
	require.Greater(t, detail.Utility, 0.0)
	require.NotNil(t, detail.UtilityParts)
	require.Greater(t, detail.UtilityParts.Recall, 0.0)
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
