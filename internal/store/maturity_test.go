package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestComputeStage(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name string
		in   StageInputs
		want ProjectStage
	}{
		{"unregistered stays seedling", StageInputs{Memories: 100, Events: 5000}, StageSeedling},
		{"young stays seedling", StageInputs{CreatedAt: now.Add(-2 * day), Memories: 10, Events: 100}, StageSeedling},
		{"old but empty stays seedling", StageInputs{CreatedAt: now.Add(-400 * day)}, StageSeedling},
		{"sprouting at the exact bars", StageInputs{CreatedAt: now.Add(-7 * day), Memories: 5, Events: 25}, StageSprouting},
		{"established", StageInputs{CreatedAt: now.Add(-31 * day), Memories: 20, Events: 300}, StageEstablished},
		{"deep-rooted needs reach", StageInputs{CreatedAt: now.Add(-100 * day), Memories: 50, Events: 2000}, StageEstablished},
		{"deep-rooted", StageInputs{CreatedAt: now.Add(-100 * day), Memories: 50, Events: 2000, HasReach: true, ReachRate: 45}, StageDeepRooted},
		{"reach below the bar holds established", StageInputs{CreatedAt: now.Add(-100 * day), Memories: 50, Events: 2000, HasReach: true, ReachRate: 30}, StageEstablished},
	} {
		require.Equal(t, tc.want, ComputeStage(tc.in, now), tc.name)
	}
	require.Greater(t, StageDeepRooted.Rank(), StageEstablished.Rank())
	require.Greater(t, StageEstablished.Rank(), StageSprouting.Rank())
	require.Greater(t, StageSprouting.Rank(), StageSeedling.Rank())
	require.Equal(t, 0, ProjectStage("garbage").Rank(), "unknown reads as seedling")
}

// seedRegisteredProject registers a project and backdates its creation.
func seedRegisteredProject(t *testing.T, db *sql.DB, slug string, created time.Time) {
	t.Helper()
	ctx := context.Background()
	id, err := core.NewID()
	require.NoError(t, err)
	require.NoError(t, CreateProject(ctx, db, core.Project{ID: id, Slug: slug, Name: slug}))
	_, err = db.ExecContext(ctx, `UPDATE projects SET created_at = ? WHERE slug = ?`,
		core.FormatTime(created), slug)
	require.NoError(t, err)
}

// seedProjectEvents writes n minimal event rows for a project.
func seedProjectEvents(t *testing.T, db *sql.DB, slug string, n int, ts time.Time) {
	t.Helper()
	for range n {
		id, err := core.NewID()
		require.NoError(t, err)
		_, err = db.ExecContext(context.Background(), `
			INSERT INTO events (id, ts, kind, session_id, project_slug, item_id, payload)
			VALUES (?, ?, 'tool.call', '', ?, '', '{}')`,
			id, core.FormatTime(ts), slug)
		require.NoError(t, err)
	}
}

func TestEvaluateProjectStages_LatchesAndNeverRegresses(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	seedRegisteredProject(t, db, "demo", now.AddDate(0, 0, -10))
	seedRegisteredProject(t, db, "fresh", now)
	seedProjectEvents(t, db, "demo", 30, now.Add(-time.Hour))

	rows := []ProjectBoardRow{
		{Project: ""}, // the global scope earns no stage
		{Project: "demo", Memories: 6},
		{Project: "fresh", Memories: 0},
	}

	stages, crossings, err := EvaluateProjectStages(ctx, db, rows, now)
	require.NoError(t, err)
	require.NotContains(t, stages, "", "the global scope is not a project")
	require.Equal(t, StageSprouting, stages["demo"].Stage)
	require.Equal(t, StageSeedling, stages["fresh"].Stage)
	require.Equal(t, []StageCrossing{{Project: "demo", From: StageSeedling, To: StageSprouting}}, crossings,
		"one crossing minted; being born a seedling is not one")
	require.Equal(t, 6, stages["demo"].Inputs.Memories, "the inputs ride along for the progress tooltip")
	require.Equal(t, 30, stages["demo"].Inputs.Events)

	// A second evaluation mints nothing new.
	_, crossings, err = EvaluateProjectStages(ctx, db, rows, now)
	require.NoError(t, err)
	require.Empty(t, crossings, "the latch was already set")

	// The project sheds its memories: the stage stands (never regress).
	rows[1].Memories = 0
	stages, crossings, err = EvaluateProjectStages(ctx, db, rows, now)
	require.NoError(t, err)
	require.Empty(t, crossings)
	require.Equal(t, StageSprouting, stages["demo"].Stage, "stages never regress")

	// A corrupt latch row degrades to recomputation, never a failed page.
	require.NoError(t, SetSetting(ctx, db, SettingProjectStages, "not json"))
	rows[1].Memories = 6
	stages, _, err = EvaluateProjectStages(ctx, db, rows, now)
	require.NoError(t, err)
	require.Equal(t, StageSprouting, stages["demo"].Stage)
}
