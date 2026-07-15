package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertFTSRow inserts an fts row directly, bypassing the index tables, so a
// test can craft rows with byte-identical indexed content (and therefore
// identical bm25 scores) under distinct item ids.
func insertFTSRow(t *testing.T, db *sql.DB, itemID, kind, project, title, name, desc, body string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO fts (item_id, kind, project, title, name, description, body)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, itemID, kind, project, title, name, desc, body)
	require.NoError(t, err)
}

// Equal-bm25 rows must come back in a stable order (secondary ORDER BY item_id)
// so repeated identical queries cannot flip ranks and destabilize RRF fusion
// downstream. The rows are inserted out of id order on purpose: without the
// tiebreak, insertion (rowid) order would leak through instead.
func TestFTSSearch_TieBreakDeterministic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	inserted := []string{"01TIE04", "01TIE01", "01TIE05", "01TIE02", "01TIE03"}
	for _, id := range inserted {
		insertFTSRow(t, db, id, "memory", "seam", "",
			"identical-name", "identical tiebreak description", "identical tiebreak body")
	}
	want := []string{"01TIE01", "01TIE02", "01TIE03", "01TIE04", "01TIE05"}

	for run := range 10 {
		hits, err := FTSSearch(ctx, db, "identical tiebreak", nil, nil, 10)
		require.NoError(t, err)
		require.Len(t, hits, 5, "run %d", run)
		got := make([]string, len(hits))
		for i, h := range hits {
			got[i] = h.ItemID
			// The fixture must actually produce ties, or this test would pass
			// on score ordering alone.
			require.Equal(t, hits[0].Score, h.Score, "run %d: fixture rows must tie on bm25", run)
		}
		require.Equal(t, want, got, "run %d: tie order must be stable and id-ascending", run)
	}

	// The tiebreak also pins which rows survive the LIMIT cut.
	top, err := FTSSearch(ctx, db, "identical tiebreak", nil, nil, 3)
	require.NoError(t, err)
	require.Len(t, top, 3)
	for i, h := range top {
		require.Equal(t, want[i], h.ItemID)
	}
}

func TestFTSSearchScoped_ProjectAndGlobal(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())

	insertMemory(t, db, "01A", "gotcha", "seam-alpha", "shared topic alpha", "seam", "body", now, "")
	insertMemory(t, db, "01B", "gotcha", "other-beta", "shared topic beta", "other", "body", now, "")
	insertMemory(t, db, "01C", "reference", "global-gamma", "shared topic gamma", "", "body", now, "")

	// Scoped to seam + global: the other-project row is excluded at query time.
	hits, err := FTSSearch(ctx, db, "shared topic", nil, []string{"", "seam"}, 10)
	require.NoError(t, err)
	ids := hitIDs(hits)
	require.ElementsMatch(t, []string{"01A", "01C"}, ids)

	// Global-only scope sees only the global row.
	hits, err = FTSSearch(ctx, db, "shared topic", nil, []string{""}, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"01C"}, hitIDs(hits))

	// An empty projects filter searches all projects (FTSSearch behavior).
	hits, err = FTSSearch(ctx, db, "shared topic", nil, nil, 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"01A", "01B", "01C"}, hitIDs(hits))

	// Kind and project filters compose.
	hits, err = FTSSearch(ctx, db, "shared topic", []string{"note"}, []string{"", "seam"}, 10)
	require.NoError(t, err)
	require.Empty(t, hits)
}

func hitIDs(hits []SearchHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ItemID
	}
	return ids
}
