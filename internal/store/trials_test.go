package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func addTrial(t *testing.T, db *sql.DB, lab, title string, outcome core.TrialOutcome, metrics map[string]any, seq int) string {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Add(time.Duration(seq) * time.Minute)
	require.NoError(t, CreateTrial(context.Background(), db, core.Trial{
		ID: id, Lab: lab, Title: title, Outcome: outcome, Metrics: metrics,
		ProjectSlug: "demo", CreatedAt: ts,
	}))
	return id
}

func TestQueryTrialsFilters(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	addTrial(t, db, "demo-dfu", "baseline", core.OutcomeFail, map[string]any{"fw": "2.0.3", "hz": 497}, 1)
	addTrial(t, db, "demo-dfu", "retry", core.OutcomePass, map[string]any{"fw": "2.0.4", "hz": 500}, 2)
	addTrial(t, db, "other-lab", "unrelated", core.OutcomePass, map[string]any{"hz": 497}, 3)

	// Lab filter, newest first.
	byLab, err := QueryTrials(ctx, db, TrialFilter{Lab: "demo-dfu"})
	require.NoError(t, err)
	require.Len(t, byLab, 2)
	require.Equal(t, "retry", byLab[0].Title)

	// Outcome filter.
	fails, err := QueryTrials(ctx, db, TrialFilter{Lab: "demo-dfu", Outcome: string(core.OutcomeFail)})
	require.NoError(t, err)
	require.Len(t, fails, 1)
	require.Equal(t, "baseline", fails[0].Title)

	// Metrics equality filter: 497 (int literal) matches the stored 497.
	byHz, err := QueryTrials(ctx, db, TrialFilter{Lab: "demo-dfu", MetricsEquals: map[string]any{"hz": 497}})
	require.NoError(t, err)
	require.Len(t, byHz, 1)
	require.Equal(t, "baseline", byHz[0].Title)
	require.EqualValues(t, 497, byHz[0].Metrics["hz"])

	// Multi-key metrics filter that nothing satisfies.
	none, err := QueryTrials(ctx, db, TrialFilter{MetricsEquals: map[string]any{"hz": 497, "fw": "9.9.9"}})
	require.NoError(t, err)
	require.Empty(t, none)
}
