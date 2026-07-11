package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// seedMemoryRow inserts a minimal active memory index row for stats/staleness
// tests (no file needed; these tests exercise the index + events only).
func seedMemoryRow(t *testing.T, db *sql.DB, id, name string, updated time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO memories_index
			(id, kind, name, description, project, file_path, tags, content_hash, created_at, updated_at)
		VALUES (?, 'gotcha', ?, '', '', ?, '[]', 'h', ?, ?)`,
		id, name, "memory/_global/"+name+".md",
		core.FormatTime(updated), core.FormatTime(updated))
	require.NoError(t, err)
}

// insertEvent is a tiny helper writing a raw event row.
func insertEvent(t *testing.T, db *sql.DB, kind core.EventKind, itemID, payload string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, '', '', ?, ?)`,
		id, core.FormatTime(ts), string(kind), itemID, payload)
	require.NoError(t, err)
}

func TestRebuildRetrievalStats_FromEvents(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	// mem A: injected twice (once via item_ids array, once again later), read once.
	insertEvent(t, db, core.EventInjected, "", `{"item_ids":["A","B"]}`, base)
	insertEvent(t, db, core.EventInjected, "", `{"item_ids":["A"]}`, base.Add(time.Hour))
	insertEvent(t, db, core.EventMemoryRead, "A", "{}", base.Add(2*time.Hour))
	// A coarse hook injection with no item ids contributes nothing per-item.
	insertEvent(t, db, core.EventInjected, "", `{"hook":"session-start"}`, base.Add(3*time.Hour))

	require.NoError(t, RebuildRetrievalStats(ctx, db))

	a, ok, err := GetRetrievalStat(ctx, db, "A")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, a.InjectCount)
	require.Equal(t, 1, a.ReadCount)
	require.NotNil(t, a.LastInjectedAt)
	require.Equal(t, base.Add(time.Hour).Unix(), a.LastInjectedAt.Unix())
	require.NotNil(t, a.LastReadAt)
	require.Equal(t, base.Add(2*time.Hour).Unix(), a.LastReadAt.Unix())

	b, ok, err := GetRetrievalStat(ctx, db, "B")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, b.InjectCount)
	require.Equal(t, 0, b.ReadCount)
	require.Nil(t, b.LastReadAt)

	// Rebuild is idempotent (clears then re-derives).
	require.NoError(t, RebuildRetrievalStats(ctx, db))
	a2, ok, err := GetRetrievalStat(ctx, db, "A")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, a2.InjectCount)

	_, ok, err = GetRetrievalStat(ctx, db, "missing")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestStaleMemories(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)

	// fresh: updated recently -> never stale regardless of stats.
	seedMemoryRow(t, db, "fresh", "fresh", now.Add(-1*24*time.Hour))
	// old-untouched: updated long ago, never injected/read -> stale.
	seedMemoryRow(t, db, "old", "old", now.Add(-120*24*time.Hour))
	// old-but-injected: updated long ago but injected recently -> NOT stale.
	seedMemoryRow(t, db, "kept", "kept", now.Add(-120*24*time.Hour))
	insertEvent(t, db, core.EventInjected, "", `{"item_ids":["kept"]}`, now.Add(-2*24*time.Hour))

	require.NoError(t, RebuildRetrievalStats(ctx, db))

	stale, err := StaleMemories(ctx, db, cutoff)
	require.NoError(t, err)
	names := make([]string, len(stale))
	for i, m := range stale {
		names[i] = m.Name
	}
	require.Equal(t, []string{"old"}, names)
}
