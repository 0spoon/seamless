package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertNoteReadInSession writes a note.read attributed to a session, which the
// package's insertEvent helper cannot do (it leaves session_id empty, and an
// event with no session escapes the per-session dedup entirely).
func insertNoteReadInSession(t *testing.T, db *sql.DB, itemID, sessionID string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, '', ?, '{}')`,
		id, core.FormatTime(ts), string(core.EventNoteRead), sessionID, itemID)
	require.NoError(t, err)
}

// A note read is query-gated demand exactly like a memory read: same counters,
// same "read" signal class, same weight. Notes are first-class recall results,
// so a note an agent pulls back up has to earn credit or the closed loop is
// blind to half of what it surfaces.
func TestRebuildRetrievalStats_NoteReadIsDemand(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour)

	insertEvent(t, db, core.EventNoteRead, "NOTE", "{}", ts)
	insertEvent(t, db, core.EventMemoryRead, "MEM", "{}", ts)

	require.NoError(t, rebuildRetrievalStats(ctx, db, now))

	note := getStat(t, db, "NOTE")
	require.Equal(t, 1, note.ReadCount)
	require.Equal(t, 0, note.InjectCount)
	require.NotNil(t, note.LastReadAt)
	require.Equal(t, ts.Unix(), note.LastReadAt.Unix())
	require.Greater(t, note.Components.Read, 0.0)
	require.Zero(t, note.Components.Recall)
	require.Zero(t, note.Components.Prompt)
	require.Greater(t, note.Utility, 0.0)

	// The note's row is keyed by item id like any other, so it lands beside the
	// memories' with the same score for the same signal.
	require.Equal(t, getStat(t, db, "MEM").Utility, note.Utility)

	scores, err := UtilityScores(ctx, db)
	require.NoError(t, err)
	require.Contains(t, scores, "NOTE")
}

// The once-per-(item, session, class) dedup covers note reads too: re-opening
// one note through a long session is one unit of demand, not one per read.
func TestRebuildRetrievalStats_NoteReadPerSessionDedup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-time.Hour)

	insertNoteReadInSession(t, db, "BURST", "s1", ts)
	insertNoteReadInSession(t, db, "BURST", "s1", ts)
	insertNoteReadInSession(t, db, "SPREAD", "s1", ts)
	insertNoteReadInSession(t, db, "SPREAD", "s2", ts)

	require.NoError(t, rebuildRetrievalStats(ctx, db, now))

	burst, spread := getStat(t, db, "BURST"), getStat(t, db, "SPREAD")
	require.Equal(t, 2, burst.ReadCount, "read counters keep every event")
	require.InDelta(t, utilityWeightRead*utilityDecay(time.Hour), burst.Components.Read, 0.001,
		"two same-session note reads credit once")
	require.Greater(t, spread.Utility, burst.Utility,
		"two sessions of demand beat two repeats in one")
}

// Note reads run through the same decay path as every other signal.
// TestRebuildRetrievalStats_UtilityDecay already pins the half-life math itself,
// so this only proves the note side is not exempt from it.
func TestRebuildRetrievalStats_NoteReadDecays(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	insertEvent(t, db, core.EventNoteRead, "FRESH", "{}", now.Add(-time.Hour))
	insertEvent(t, db, core.EventNoteRead, "OLD", "{}", now.Add(-28*24*time.Hour))

	require.NoError(t, rebuildRetrievalStats(ctx, db, now))

	fresh, old := getStat(t, db, "FRESH"), getStat(t, db, "OLD")
	require.Greater(t, fresh.Components.Read, old.Components.Read)
	require.Greater(t, fresh.Utility, old.Utility)
}
