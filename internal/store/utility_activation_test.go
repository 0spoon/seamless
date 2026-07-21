package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertProjectEvent writes a raw event row stamped with a project slug and a
// session, for the per-project demand tests.
func insertProjectEvent(t *testing.T, db *sql.DB, kind core.EventKind, session, project, itemID, payload string, ts time.Time) {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, core.FormatTime(ts), string(kind), session, project, itemID, payload)
	require.NoError(t, err)
}

// Activation resolution: the global mode wins outright, the per-project force
// wins over the latch, and the latch is what "auto" defers to.
func TestUtilityActivation_Active(t *testing.T) {
	now := time.Now()
	a := UtilityActivation{Projects: map[string]UtilityProjectState{
		"latched": {ReadyAt: &now},
		"forced":  {Forced: "on"},
		"muted":   {ReadyAt: &now, Forced: "off"},
	}}

	require.True(t, a.Active("latched", "auto"))
	require.True(t, a.Active("forced", "auto"))
	require.False(t, a.Active("muted", "auto"), "force off wins over the latch")
	require.False(t, a.Active("unknown", "auto"))
	require.True(t, a.Active("unknown", "on"))
	require.False(t, a.Active("latched", "off"))
	require.True(t, a.Active("latched", ""), "empty mode behaves as auto")
}

func TestUtilityActivation_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	empty, err := GetUtilityActivation(ctx, db)
	require.NoError(t, err)
	require.Empty(t, empty.Projects, "absent row reads as nothing active")

	ready := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	empty.Projects["seamless"] = UtilityProjectState{ReadyAt: &ready}
	empty.Projects["quiet"] = UtilityProjectState{Forced: "off"}
	require.NoError(t, SetUtilityActivation(ctx, db, empty))

	got, err := GetUtilityActivation(ctx, db)
	require.NoError(t, err)
	require.NotNil(t, got.Projects["seamless"].ReadyAt)
	require.Equal(t, ready.Unix(), got.Projects["seamless"].ReadyAt.Unix())
	require.Equal(t, "off", got.Projects["quiet"].Forced)
}

// Demand grouping: query-gated events roll up per project slug with session
// dedup; briefing injections contribute nothing; the window bounds the counts
// while Earliest is all-time.
func TestUtilityDemandByProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	window := 30 * 24 * time.Hour

	old := now.Add(-40 * 24 * time.Hour)   // outside the window, sets Earliest
	recent := now.Add(-2 * 24 * time.Hour) // inside

	insertProjectEvent(t, db, core.EventInjected, "s0", "alpha", "",
		`{"item_ids":["M1"],"source":"recall"}`, old)
	// Two same-session prompt matches: one demand event, two memories.
	insertProjectEvent(t, db, core.EventInjected, "s1", "alpha", "",
		`{"item_ids":["M1"],"hook":"user-prompt-submit"}`, recent)
	insertProjectEvent(t, db, core.EventInjected, "s1", "alpha", "",
		`{"item_ids":["M2"],"hook":"user-prompt-submit"}`, recent)
	// A read in another session: distinct class -> second event.
	insertProjectEvent(t, db, core.EventMemoryRead, "s2", "alpha", "M1", "{}", recent)
	// Briefing exposure never counts as demand.
	insertProjectEvent(t, db, core.EventInjected, "s3", "alpha", "",
		`{"item_ids":["M9"],"hook":"session-start"}`, recent)
	// A different project stays separate.
	insertProjectEvent(t, db, core.EventInjected, "s4", "beta", "",
		`{"item_ids":["B1"],"source":"recall"}`, recent)

	demand, err := UtilityDemandByProject(ctx, db, now, window)
	require.NoError(t, err)

	alpha := demand["alpha"]
	require.Equal(t, old.Unix(), alpha.Earliest.Unix(), "earliest is all-time")
	require.Equal(t, 2, alpha.RecentEvents, "prompt (s1, deduped) + read (s2); the old recall is outside the window")
	require.Equal(t, 2, alpha.RecentMemories, "M1 + M2; briefing-only M9 excluded")

	beta := demand["beta"]
	require.Equal(t, 1, beta.RecentEvents)
	require.Equal(t, 1, beta.RecentMemories)

	// Readiness: age satisfied (40d) but volume below the floors.
	require.False(t, alpha.Ready(now))
	require.True(t, UtilityProjectDemand{
		Earliest:     now.Add(-UtilityReadyMinAgeDays * 24 * time.Hour),
		RecentEvents: UtilityReadyMinEvents, RecentMemories: UtilityReadyMinMemories,
	}.Ready(now))
	require.False(t, UtilityProjectDemand{
		Earliest:     now.Add(-(UtilityReadyMinAgeDays - 1) * 24 * time.Hour),
		RecentEvents: UtilityReadyMinEvents, RecentMemories: UtilityReadyMinMemories,
	}.Ready(now), "too young")
}
