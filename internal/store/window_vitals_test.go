package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestPriorWindow_IsTheEquallyLongStretchBefore(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	since, until, ok := PriorWindow(ResolveRetrievalWindow("24h", now), now)
	require.True(t, ok)
	require.Equal(t, now.Add(-48*time.Hour), since)
	require.Equal(t, now.Add(-24*time.Hour), until)

	since, until, ok = PriorWindow(ResolveRetrievalWindow("7d", now), now)
	require.True(t, ok)
	require.Equal(t, now.AddDate(0, 0, -7), until)
	require.Equal(t, until.Sub(since), now.Sub(until), "prior window is the same length as the selected one")

	_, _, ok = PriorWindow(ResolveRetrievalWindow("all", now), now)
	require.False(t, ok, "all time has nothing before it to compare against")
}

// insertInjection writes an injection event attributed to a session.
func insertInjection(t *testing.T, db *sql.DB, sessionID, itemID string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, '', ?, '{}')`,
		id, core.FormatTime(ts), string(core.EventInjected), sessionID, itemID)
	require.NoError(t, err)
}

func TestGetWindowVitals_BoundedOnBothSides(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return core.FormatTime(now.Add(d)) }

	// Four active memories: two in project "alpha", two global.
	insertMemory(t, db, "m1", "gotcha", "one", "", "alpha", "b", stamp(-100*time.Hour), "")
	insertMemory(t, db, "m2", "gotcha", "two", "", "alpha", "b", stamp(-100*time.Hour), "")
	insertMemory(t, db, "m3", "gotcha", "three", "", "", "b", stamp(-100*time.Hour), "")
	insertMemory(t, db, "m4", "gotcha", "four", "", "", "b", stamp(-100*time.Hour), "")

	// Inside the prior 24h window ([-48h, -24h)): m1 twice, m3 once, 2 sessions.
	insertInjection(t, db, "sA", "m1", now.Add(-40*time.Hour))
	insertInjection(t, db, "sA", "m1", now.Add(-39*time.Hour))
	insertInjection(t, db, "sB", "m3", now.Add(-30*time.Hour))
	// Inside the current 24h window -- must NOT leak into the prior rollup.
	insertInjection(t, db, "sC", "m2", now.Add(-2*time.Hour))
	// Older than the prior window -- also excluded.
	insertInjection(t, db, "sD", "m4", now.Add(-90*time.Hour))

	since, until, ok := PriorWindow(ResolveRetrievalWindow("24h", now), now)
	require.True(t, ok)

	v, err := GetWindowVitals(ctx, db, since, until)
	require.NoError(t, err)
	require.Equal(t, 3, v.Injections, "only the three injections inside [-48h, -24h)")
	require.Equal(t, 2, v.MemoriesSurfaced, "m1 and m3")
	require.Equal(t, 2, v.SessionsReached, "sA and sB")
	require.Equal(t, 4, v.ActiveMemories, "denominator is the knowledge base as it stands now")
	require.Equal(t, 50, v.ReachRate)

	// Project scoping counts only that project's own memories.
	pv, err := GetWindowVitalsForProject(ctx, db, "alpha", since, until)
	require.NoError(t, err)
	require.Equal(t, 2, pv.Injections, "m1 twice; m3 is global")
	require.Equal(t, 1, pv.MemoriesSurfaced)
	require.Equal(t, 1, pv.SessionsReached)
	require.Equal(t, 2, pv.ActiveMemories)
	require.Equal(t, 50, pv.ReachRate)

	_, err = GetWindowVitalsForProject(ctx, db, "", since, until)
	require.Error(t, err, "an empty slug is ambiguous, never 'every scope'")
}

func TestGetWindowVitals_ContinuityIsWindowScoped(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	mk := func(id string, at time.Duration, findings, project string) {
		s := core.Session{
			ID: id, Name: "cc/" + id, Status: core.SessionCompleted, ProjectSlug: project,
			Findings: findings, CreatedAt: now.Add(at), UpdatedAt: now.Add(at),
		}
		require.NoError(t, CreateSession(ctx, db, s))
	}
	// Prior window ([-48h, -24h)): two sessions, one covered.
	mk("p1", -40*time.Hour, "kept something", "alpha")
	mk("p2", -30*time.Hour, "", "alpha")
	// Current window: one covered session -- must not count toward the prior rate.
	mk("c1", -3*time.Hour, "kept something", "alpha")

	since, until, ok := PriorWindow(ResolveRetrievalWindow("24h", now), now)
	require.True(t, ok)

	v, err := GetWindowVitals(ctx, db, since, until)
	require.NoError(t, err)
	require.Equal(t, 2, v.CovTotal)
	require.Equal(t, 1, v.Covered)
	require.Equal(t, 50, v.Coverage)

	pv, err := GetWindowVitalsForProject(ctx, db, "alpha", since, until)
	require.NoError(t, err)
	require.Equal(t, 2, pv.CovTotal)
	require.Equal(t, 50, pv.Coverage)
}
