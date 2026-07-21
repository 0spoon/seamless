package retrieve

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/store"
)

func starMemory(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`UPDATE memories_index SET favorite = 1 WHERE id = ?`, id)
	require.NoError(t, err)
}

// A starred non-constraint memory is pinned as a FAVORITE head line: it leaves
// the trimmable index (surviving MemoryMaxItems=1), renders once, and reports
// its id for the retrieval funnel. A starred constraint keeps its CONSTRAINT
// section and is never rendered twice.
func TestBriefingPinsFavorites(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))

	insMem(t, db, "01A", "constraint", "starred-constraint", "a starred hard rule", "seam")
	insMemAt(t, db, "01B", "gotcha", "starred-gotcha", "the starred pitfall", "seam", time.Now().Add(-2*time.Hour))
	insMem(t, db, "01C", "gotcha", "plain-gotcha", "an ordinary pitfall", "seam")
	starMemory(t, db, "01A")
	starMemory(t, db, "01B")

	svc := New(db, nil, budgets(), nil)
	// A one-item index cap: the starred gotcha must survive it by being pinned,
	// while the plain gotcha keeps the sole index slot.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.MemoryMaxItems = 1 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "FAVORITE: starred-gotcha: the starred pitfall")
	require.Contains(t, b, "CONSTRAINT: starred-constraint")
	require.NotContains(t, b, "FAVORITE: starred-constraint", "a starred constraint stays in its own pinned section")
	require.Contains(t, b, "plain-gotcha")
	require.Subset(t, ids, []string{"01A", "01B", "01C"})
}

// The post-fusion boost promotes a starred memory past a slightly better
// lexical match, carries Favorite on the hit, and never widens scope: a starred
// memory in another project stays invisible.
func TestRecallFavoriteBoost(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	// top-dog matches "race" twice (better bm25 rank); fav-target once.
	insMem(t, db, "01TOP", "gotcha", "top-dog", "race race", "seam")
	insMem(t, db, "01FAV", "gotcha", "fav-target", "boot race", "seam")
	insMem(t, db, "01OTH", "gotcha", "other-scope", "race race race", "elsewhere")
	starMemory(t, db, "01FAV")
	starMemory(t, db, "01OTH")

	svc := New(db, nil, budgets(), nil)
	hits, err := svc.Recall(ctx, RecallInput{Query: "race", Project: "seam", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 2, "the starred out-of-scope memory must not be resurrected")
	require.Equal(t, "fav-target", hits[0].Name, "the boost outranks the better lexical match")
	require.True(t, hits[0].Favorite)
	require.False(t, hits[1].Favorite)
}
