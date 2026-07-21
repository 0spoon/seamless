package retrieve

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// seedReads writes n sessionless memory.read events for id and rebuilds the
// stats projection, giving the memory a real utility score. Sessionless events
// dodge the per-session dedup, so n directly scales the raw demand.
func seedReads(t *testing.T, db *sql.DB, id string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		evID, err := core.NewID()
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
			INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
			VALUES (?, ?, ?, '', '', ?, '{}')`,
			evID, core.FormatTime(time.Now().UTC().Add(-time.Hour)), string(core.EventMemoryRead), id)
		require.NoError(t, err)
	}
	require.NoError(t, store.RebuildRetrievalStats(ctx, db))
}

// latchUtility marks a project's utility activation as latched (what the
// gardener does once readiness trips).
func latchUtility(t *testing.T, db *sql.DB, project string) {
	t.Helper()
	ctx := context.Background()
	a, err := store.GetUtilityActivation(ctx, db)
	require.NoError(t, err)
	now := time.Now().UTC()
	a.Projects[project] = store.UtilityProjectState{ReadyAt: &now}
	require.NoError(t, store.SetUtilityActivation(ctx, db, a))
}

// The briefing index blends utility with recency once ranking is active: a
// ten-day-old workhorse overtakes fresher but never-demanded memories, weight
// 0 restores the exact legacy order, and in auto mode nothing changes until
// the project's latch trips.
func TestBriefingIndexUtilityReorders(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	insMemAt(t, db, "01HOT", "gotcha", "old-hot-memory", "proven workhorse", "p", time.Now().Add(-10*24*time.Hour))
	insMemAt(t, db, "01QUI", "gotcha", "old-quiet-memory", "never demanded", "p", time.Now().Add(-9*24*time.Hour))
	insMem(t, db, "01NEW", "gotcha", "new-quiet-memory", "just written", "p")
	seedReads(t, db, "01HOT", 6)

	svc := New(db, nil, budgets(), nil)
	order := func(b string) (hot, quiet, fresh int) {
		return strings.Index(b, "old-hot-memory"), strings.Index(b, "old-quiet-memory"), strings.Index(b, "new-quiet-memory")
	}

	// Default mode is auto and nothing is latched: exact legacy recency order.
	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	hot, quiet, fresh := order(b)
	require.True(t, fresh < quiet && quiet < hot, "legacy order is newest-updated first: %s", b)

	// Mode on: the blend lifts the proven memory over both quiet ones, while
	// the brand-new memory stays ahead of the equally-old quiet one (recency
	// dominates among the unproven).
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.UtilityMode = "on" }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	hot, quiet, fresh = order(b)
	require.True(t, hot < fresh && fresh < quiet, "utility blend order: %s", b)

	// Weight 0 is the kill switch even with mode on.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.UtilityMode = "on"; b.UtilityWeight = 0 }))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	hot, quiet, fresh = order(b)
	require.True(t, fresh < quiet && quiet < hot, "weight 0 restores recency order")

	// Auto + a latched project ranks; forcing it off reverts.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) {}))
	latchUtility(t, db, "p")
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	hot, _, fresh = order(b)
	require.True(t, hot < fresh, "latched project ranks by the blend")

	a, err := store.GetUtilityActivation(ctx, db)
	require.NoError(t, err)
	st := a.Projects["p"]
	st.Forced = "off"
	a.Projects["p"] = st
	require.NoError(t, store.SetUtilityActivation(ctx, db, a))
	b, _, err = svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	hot, _, fresh = order(b)
	require.True(t, fresh < hot, "force off wins over the latch")
}

// Pinned sections ignore utility entirely: with ranking on and a one-line
// index cap, constraints and favorites render regardless of who is hot.
func TestBriefingUtilityNeverTouchesPinned(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))

	insMem(t, db, "01C", "constraint", "hard-rule", "a load-bearing rule", "p")
	insMemAt(t, db, "01F", "gotcha", "starred-pick", "the starred pitfall", "p", time.Now().Add(-30*24*time.Hour))
	insMem(t, db, "01P", "gotcha", "plain-hot", "the utility magnet", "p")
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET favorite = 1 WHERE id = '01F'`)
	require.NoError(t, err)
	seedReads(t, db, "01P", 10)

	svc := New(db, nil, budgets(), nil)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.UtilityMode = "on"; b.MemoryMaxItems = 1 }))

	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "CONSTRAINT: hard-rule")
	require.Contains(t, b, "FAVORITE: starred-pick")
	require.Contains(t, b, "plain-hot", "the hot memory holds the sole index slot")
}

// Utility breaks exact prompt-recall score ties, but the floors run on the raw
// IDF score first: a sub-threshold candidate stays out no matter its utility.
func TestScorePromptUtilityBoost(t *testing.T) {
	set := func(toks ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(toks))
		for _, tok := range toks {
			out[tok] = struct{}{}
		}
		return out
	}
	c := &promptCorpus{
		idf: map[string]float64{"alpha": 2, "beta": 2, "gamma": 2},
		candidates: []promptCand{
			{id: "A", name: "a", tokens: []string{"alpha", "beta"}, tokenSet: set("alpha", "beta")},
			{id: "B", name: "b", tokens: []string{"alpha", "beta"}, tokenSet: set("alpha", "beta"), utility: 0.9},
			// One-token overlap: below promptMinOverlap regardless of utility.
			{id: "SUB", name: "sub", tokens: []string{"alpha", "gamma"}, tokenSet: set("alpha", "gamma"), utility: 0.99},
		},
	}

	hits := scorePrompt([]string{"alpha", "beta"}, c)
	require.Len(t, hits, 2, "utility can never resurrect a sub-floor candidate")
	require.Equal(t, "B", hits[0].id, "utility breaks the exact score tie")
	require.Equal(t, "A", hits[1].id)
}

// The post-fusion utility multiplier promotes a proven memory past a slightly
// better lexical match -- the favoriteBoost contract, at a smaller magnitude.
func TestRecallUtilityBoost(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()

	// top-dog matches "race" twice (better bm25 rank); hot-target once.
	insMem(t, db, "01TOP", "gotcha", "top-dog", "race race", "seam")
	insMem(t, db, "01HOT", "gotcha", "hot-target", "boot race", "seam")
	seedReads(t, db, "01HOT", 6)

	svc := New(db, nil, budgets(), nil)
	hits, err := svc.Recall(ctx, RecallInput{Query: "race", Project: "seam", Limit: 10})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "hot-target", hits[0].Name, "utility outranks the adjacent lexical rank")
}
