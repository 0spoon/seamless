package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertMishapEvent writes a raw agent.mishap event row for a project.
func insertMishapEvent(t *testing.T, db *sql.DB, project, payload string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, '', ?, '', ?)`,
		id, core.FormatTime(ts), string(core.EventAgentMishap), project, payload)
	require.NoError(t, err)
}

func TestRecentMishapItemIDs_WindowAndProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	window := 30 * 24 * time.Hour

	// A is referenced twice in the window; the most recent timestamp must win.
	insertMishapEvent(t, db, "demo", `{"description":"d1","item_ids":["A"]}`, now.Add(-72*time.Hour))
	insertMishapEvent(t, db, "demo", `{"description":"d2","item_ids":["A","B"]}`, now.Add(-time.Hour))
	// C: referenced, but outside the window.
	insertMishapEvent(t, db, "demo", `{"description":"d3","item_ids":["C"]}`, now.Add(-31*24*time.Hour))
	// D: another project's mishap never surfaces in demo's view.
	insertMishapEvent(t, db, "other", `{"description":"d4","item_ids":["D"]}`, now.Add(-time.Hour))
	// E: exactly on the window edge is still inside (>= cutoff).
	insertMishapEvent(t, db, "demo", `{"description":"d5","item_ids":["E"]}`, now.Add(-window))
	// Unlinked reports and degenerate payloads contribute nothing.
	insertMishapEvent(t, db, "demo", `{"description":"names nothing"}`, now.Add(-time.Hour))
	insertMishapEvent(t, db, "demo", `{}`, now.Add(-time.Hour))
	insertMishapEvent(t, db, "demo", `not json`, now.Add(-time.Hour))

	got, err := recentMishapItemIDs(ctx, db, "demo", window, now)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, now.Add(-time.Hour).Unix(), got["A"].Unix(), "most recent reference wins")
	require.Equal(t, now.Add(-time.Hour).Unix(), got["B"].Unix())
	require.Equal(t, now.Add(-window).Unix(), got["E"].Unix())
	require.NotContains(t, got, "C", "outside the window")
	require.NotContains(t, got, "D", "another project")
}

func TestRecentMishapItemIDs_NoEventsYieldsEmpty(t *testing.T) {
	db := openTestDB(t)
	got, err := RecentMishapItemIDs(context.Background(), db, "demo", 30*24*time.Hour)
	require.NoError(t, err)
	require.Empty(t, got)
}

// The hard boundary of the closed-loop-utility-signal-contract: agent.mishap
// events are a briefing-side ordering signal only. RebuildRetrievalStats reads
// retrieval.injected and memory.read exclusively, so a mishap reference must
// never create a stats row or move utility.
func TestRecentMishapItemIDs_MishapsDoNotFeedUtility(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	insertMishapEvent(t, db, "demo", `{"description":"d","item_ids":["A"]}`, now.Add(-time.Hour))
	require.NoError(t, rebuildRetrievalStats(ctx, db, now))

	_, ok, err := GetRetrievalStat(ctx, db, "A")
	require.NoError(t, err)
	require.False(t, ok, "a mishap reference must not touch retrieval stats or utility")
}
