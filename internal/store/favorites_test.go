package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// Each DB-side setter round-trips through the entity's own scan, and none of
// them bumps updated_at (a star is metadata, not authorship).
func TestSetFavorites_RoundTripWithoutUpdatedBump(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)

	require.NoError(t, CreateProject(ctx, db, core.Project{
		ID: "01P", Slug: "seam", Name: "Seam", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, CreateTask(ctx, db, core.Task{
		ID: "01T", ProjectSlug: "seam", Title: "task", Status: core.TaskOpen,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01S", Name: "cc/fav", ProjectSlug: "seam", Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, CreateTrial(ctx, db, core.Trial{
		ID: "01TR", Lab: "lab", Title: "trial", ProjectSlug: "seam", CreatedAt: now,
	}))

	require.NoError(t, SetProjectFavorite(ctx, db, "seam", true))
	require.NoError(t, SetTaskFavorite(ctx, db, "01T", true))
	require.NoError(t, SetSessionFavorite(ctx, db, "01S", true))
	require.NoError(t, SetTrialFavorite(ctx, db, "01TR", true))

	p, ok, err := ProjectBySlug(ctx, db, "seam")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, p.Favorite)
	require.Equal(t, now, p.UpdatedAt.UTC(), "starring must not bump updated_at")

	task, err := TaskByID(ctx, db, "01T")
	require.NoError(t, err)
	require.True(t, task.Favorite)
	require.Equal(t, now, task.UpdatedAt.UTC())

	sess, ok, err := SessionByID(ctx, db, "01S")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, sess.Favorite)
	require.Equal(t, now, sess.UpdatedAt.UTC())

	tr, ok, err := TrialByID(ctx, db, "01TR")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, tr.Favorite)

	// Unset round-trips too, and an unknown id is a silent no-op.
	require.NoError(t, SetTaskFavorite(ctx, db, "01T", false))
	task, err = TaskByID(ctx, db, "01T")
	require.NoError(t, err)
	require.False(t, task.Favorite)
	require.NoError(t, SetTaskFavorite(ctx, db, "does-not-exist", true))
}

// A plan search row is favorite when any of its tagged notes is starred; the
// task leg alone contributes false.
func TestSearchPlans_Favorite(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())

	insertSearchNote(t, db, "01N1", "Starred plan", "starred-plan-note", "seam",
		`["plan:starred-plan"]`, now)
	_, err := db.ExecContext(ctx, `UPDATE notes_index SET favorite = 1 WHERE id = '01N1'`)
	require.NoError(t, err)
	insertSearchTask(t, db, "01T1", "seam", "step", "plain-plan", now)

	got, err := SearchPlans(ctx, db, "plan", 20)
	require.NoError(t, err)
	bySlug := map[string]PlanSearchRow{}
	for _, r := range got {
		bySlug[r.Slug] = r
	}
	require.True(t, bySlug["starred-plan"].Favorite)
	require.False(t, bySlug["plain-plan"].Favorite)
}
