package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertRetrievalEvent writes a raw event row (session + item_id column + payload)
// for the retrieval-report tests.
func insertRetrievalEvent(t *testing.T, db *sql.DB, kind core.EventKind, session, itemID, payload string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, '', ?, ?)`,
		id, core.FormatTime(ts), string(kind), session, itemID, payload)
	require.NoError(t, err)
}

// seedMemoryRowProject inserts an active memory index row scoped to a project
// (seedMemoryRow always uses the global scope) for the per-project reach tests.
func seedMemoryRowProject(t *testing.T, db *sql.DB, id, name, project string, updated time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO memories_index
			(id, kind, name, description, project, file_path, tags, content_hash, created_at, updated_at)
		VALUES (?, 'gotcha', ?, '', ?, ?, '[]', 'h', ?, ?)`,
		id, name, project, "memory/"+project+"/"+id+".md",
		core.FormatTime(updated), core.FormatTime(updated))
	require.NoError(t, err)
}

func TestBuildRetrievalReport_ByProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	base := now.Add(-time.Hour)

	// alpha: 2 active, 1 surfaced (A1); beta: 1 active, surfaced twice; global: 1
	// active, never surfaced -> appears at 0%.
	seedMemoryRowProject(t, db, "A1", "alpha-one", "alpha", now)
	seedMemoryRowProject(t, db, "A2", "alpha-two", "alpha", now)
	seedMemoryRowProject(t, db, "B1", "beta-one", "beta", now)
	seedMemoryRowProject(t, db, "G1", "global-one", "", now)

	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["A1"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["B1"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "", `{"item_ids":["B1"]}`, base)

	rep, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)

	require.Equal(t, 4, rep.ActiveMemories)
	require.Equal(t, 2, rep.MemoriesSurfaced) // A1, B1
	require.Equal(t, 50, rep.ReachRate)       // 2/4

	// Sorted by active desc, then surfaced desc: alpha(2), beta(1 surfaced), global(0).
	require.Len(t, rep.ByProject, 3)
	require.Equal(t, ProjectReach{Project: "alpha", Surfaced: 1, Active: 2, ReachRate: 50, Injects: 1}, rep.ByProject[0])
	require.Equal(t, ProjectReach{Project: "beta", Surfaced: 1, Active: 1, ReachRate: 100, Injects: 2}, rep.ByProject[1])
	require.Equal(t, ProjectReach{Project: "", Surfaced: 0, Active: 1, ReachRate: 0, Injects: 0}, rep.ByProject[2])
}

func TestProjectRetrievalTrend(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	base := now.Add(-time.Hour)

	seedMemoryRowProject(t, db, "A1", "alpha-one", "alpha", now)
	seedMemoryRowProject(t, db, "B1", "beta-one", "beta", now)
	seedMemoryRowProject(t, db, "G1", "global-one", "", now) // active, never surfaced

	// alpha surfaced twice, beta once, plus an unknown id X (no active project).
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["A1"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "", `{"item_ids":["A1"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["B1"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["X"]}`, base)

	sumBuckets := func(bs []TrendBucket) int {
		n := 0
		for _, b := range bs {
			n += b.Count
		}
		return n
	}

	win := ResolveRetrievalWindow("all", now)

	alpha, err := ProjectRetrievalTrend(ctx, db, win, "alpha")
	require.NoError(t, err)
	require.Equal(t, 2, sumBuckets(alpha), "only alpha's two A1 injections")

	beta, err := ProjectRetrievalTrend(ctx, db, win, "beta")
	require.NoError(t, err)
	require.Equal(t, 1, sumBuckets(beta), "only beta's single B1 injection")

	// global (G1 never surfaced; unknown X is not attributable) -> empty trend.
	global, err := ProjectRetrievalTrend(ctx, db, win, "")
	require.NoError(t, err)
	require.Empty(t, global)

	// The alpha trend agrees with the reach report's per-project injection volume.
	rep, err := BuildRetrievalReport(ctx, db, win, 12)
	require.NoError(t, err)
	require.Equal(t, rep.ByProject[0].Injects, sumBuckets(alpha))
}

func TestProjectRetrievalTrend_WindowBounds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMemoryRowProject(t, db, "A1", "alpha-one", "alpha", now)

	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["A1"]}`, now.Add(-48*time.Hour))
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "", `{"item_ids":["A1"]}`, now.Add(-90*time.Minute))

	sumBuckets := func(bs []TrendBucket) int {
		n := 0
		for _, b := range bs {
			n += b.Count
		}
		return n
	}

	day, err := ProjectRetrievalTrend(ctx, db, ResolveRetrievalWindow("24h", now), "alpha")
	require.NoError(t, err)
	require.Equal(t, 1, sumBuckets(day), "the 48h-old injection is outside the 24h window")

	all, err := ProjectRetrievalTrend(ctx, db, ResolveRetrievalWindow("all", now), "alpha")
	require.NoError(t, err)
	require.Equal(t, 2, sumBuckets(all))
}

func TestBuildRetrievalReport_Reach(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	base := now.Add(-time.Hour)
	seedMemoryRow(t, db, "A", "mem-a", now)
	seedMemoryRow(t, db, "B", "mem-b", now)
	seedMemoryRow(t, db, "C", "mem-c", now) // active but never surfaced

	// A surfaced in two sessions, B in one, plus an injection of an unknown id X
	// (archived/unknown -> counts toward volume + session reach, not the breakdowns).
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["A"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "", `{"item_ids":["A"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["B"]}`, base)
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["X"]}`, base)

	rep, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)

	require.Equal(t, 4, rep.Injected, "volume counts every injected id, incl. unknown X")
	require.Equal(t, 2, rep.MemoriesSurfaced, "distinct active memories surfaced: A, B")
	require.Equal(t, 3, rep.ActiveMemories)
	require.Equal(t, 67, rep.ReachRate) // round(2/3 * 100)
	require.Equal(t, 2, rep.SessionsReached, "distinct sessions: sessA, sessB")

	require.Len(t, rep.ByKind, 1)
	require.Equal(t, "gotcha", rep.ByKind[0].Kind)
	require.Equal(t, 3, rep.ByKind[0].Injects, "A(2)+B(1); unknown X excluded")
	require.Equal(t, 2, rep.ByKind[0].Memories)

	require.Len(t, rep.Top, 2)
	require.Equal(t, "A", rep.Top[0].ID)
	require.Equal(t, 2, rep.Top[0].Injects)
	require.Equal(t, 2, rep.Top[0].Sessions)
	require.Equal(t, "B", rep.Top[1].ID)
	require.Equal(t, 1, rep.Top[1].Sessions)

	sum := 0
	for _, b := range rep.Trend {
		sum += b.Count
	}
	require.Equal(t, rep.Injected, sum, "hero volume == sum of trend buckets")
	require.False(t, rep.Hourly)
}

// InjectedTokens sums each event's emitted estimate, falls back to a ~4 bytes/token
// estimate of the recorded content for older hook events, and costs recall-tool
// injections (ids only, no pushed context) at zero.
func TestBuildRetrievalReport_InjectedTokens(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMemoryRow(t, db, "A", "mem-a", now)

	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "",
		`{"item_ids":["A"],"content":"unused when the estimate is present","emitted_estimated_tokens":120}`, now.Add(-time.Minute))
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "",
		`{"item_ids":["A"],"content":"12345678"}`, now.Add(-time.Minute)) // 8 bytes -> 2 tokens
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "",
		`{"item_ids":["A"],"source":"recall"}`, now.Add(-time.Minute))
	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "",
		`{"item_ids":["A"],"emitted_estimated_tokens":50}`, now.Add(-48*time.Hour))

	all, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)
	require.Equal(t, 172, all.InjectedTokens, "120 + 2 (content fallback) + 0 (recall) + 50")

	day, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("24h", now), 12)
	require.NoError(t, err)
	require.Equal(t, 122, day.InjectedTokens, "the 48h-old event is outside the 24h window")
}

// Older briefing injections were recorded before the ambient session was linked,
// so their session_id column is empty but the payload always carries the Claude
// session id; reach must fall back to it so those sessions still count.
func TestBuildRetrievalReport_SessionFallback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMemoryRow(t, db, "A", "mem-a", now)

	insertRetrievalEvent(t, db, core.EventInjected, "", "", `{"item_ids":["A"],"claude_session_id":"cc-1"}`, now.Add(-time.Minute))
	insertRetrievalEvent(t, db, core.EventInjected, "sess-2", "", `{"item_ids":["A"]}`, now)

	rep, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)
	require.Equal(t, 1, rep.MemoriesSurfaced)
	require.Equal(t, 2, rep.SessionsReached, "cc-1 (payload fallback) + sess-2 (column)")
	require.Equal(t, 2, rep.Top[0].Sessions)
}

func TestBuildRetrievalReport_SessionFallbackIncludesExternalClient(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMemoryRow(t, db, "A", "mem-a", now)

	insertRetrievalEvent(t, db, core.EventInjected, "", "",
		`{"item_ids":["A"],"claude_session_id":"shared","external_client":"claude-code"}`,
		now.Add(-time.Minute))
	insertRetrievalEvent(t, db, core.EventInjected, "", "",
		`{"item_ids":["A"],"claude_session_id":"shared","external_client":"codex"}`,
		now)

	rep, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)
	require.Equal(t, 2, rep.SessionsReached,
		"identical client-issued ids remain distinct when the event row has no Seamless session id")
	require.Equal(t, 2, rep.Top[0].Sessions)
}

func TestBuildRetrievalReport_WindowBounds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMemoryRow(t, db, "A", "mem-a", now)

	insertRetrievalEvent(t, db, core.EventInjected, "sessA", "", `{"item_ids":["A"]}`, now.Add(-48*time.Hour))
	insertRetrievalEvent(t, db, core.EventInjected, "sessB", "", `{"item_ids":["A"]}`, now.Add(-90*time.Minute))

	all, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("all", now), 12)
	require.NoError(t, err)
	require.Equal(t, 2, all.Injected)
	require.False(t, all.Hourly)

	day, err := BuildRetrievalReport(ctx, db, ResolveRetrievalWindow("24h", now), 12)
	require.NoError(t, err)
	require.Equal(t, 1, day.Injected, "the 48h-old injection is outside the 24h window")
	require.Equal(t, 1, day.SessionsReached)
	require.True(t, day.Hourly, "hourly buckets for the 24h window")

	sum := 0
	for _, b := range day.Trend {
		sum += b.Count
	}
	require.Equal(t, day.Injected, sum)
}
