package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// TestPlanRollupsForProject_KeepsCompletePlansActivePlansDrops pins the one
// difference between the two rollup views: the console needs the finished plans
// (it renders them as a collapsed group), the briefing contract does not.
func TestPlanRollupsForProject_KeepsCompletePlansActivePlansDrops(t *testing.T) {
	db := newTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	seed := func(plan, title string, status core.TaskStatus, at time.Time) {
		t.Helper()
		id, err := core.NewID()
		require.NoError(t, err)
		require.NoError(t, CreateTask(ctx, db, core.Task{
			ID: id, ProjectSlug: "demo", Title: title, Status: status,
			PlanSlug: plan, CreatedAt: at, UpdatedAt: at,
		}))
	}
	// "shipped" is fully closed; "running" still has an open step.
	seed("shipped", "wire it up", core.TaskDone, now.Add(-2*time.Hour))
	seed("shipped", "ship it", core.TaskDropped, now.Add(-time.Hour))
	seed("running", "keep going", core.TaskOpen, now)

	all, err := PlanRollupsForProject(ctx, db, "demo")
	require.NoError(t, err)
	bySlug := map[string]PlanRollup{}
	for _, p := range all {
		bySlug[p.Slug] = p
	}
	require.Len(t, all, 2, "the full set carries every plan")
	require.Equal(t, 2, bySlug["shipped"].Total)
	require.Equal(t, 2, bySlug["shipped"].Done, "dropped counts as closed")
	require.False(t, bySlug["shipped"].LastActivity.IsZero())

	active, err := ActivePlans(ctx, db, "demo")
	require.NoError(t, err)
	require.Len(t, active, 1, "ActivePlans still drops the complete plan")
	require.Equal(t, "running", active[0].Slug)
	require.Equal(t, 1, active[0].Claimable, "an unblocked open step is claimable")
}

// TestPlanRollupsForProject_EmptyProject keeps the no-plans case a nil slice
// rather than an error, so the console can range over it directly.
func TestPlanRollupsForProject_EmptyProject(t *testing.T) {
	db := newTaskDB(t)
	rollups, err := PlanRollupsForProject(context.Background(), db, "nobody")
	require.NoError(t, err)
	require.Empty(t, rollups)
}
