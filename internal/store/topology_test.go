package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestActiveMemoriesForScope_UnionsParentAndGlobals(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ts := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	insertMemory(t, db, "01IOS", "gotcha", "ios-mem", "d", "arctop-ios", "b", ts(4), "")
	insertMemory(t, db, "01PAR", "reference", "shared-mem", "d", "arctop-mobile-apps", "b", ts(3), "")
	insertMemory(t, db, "01GLO", "constraint", "global-mem", "d", "", "b", ts(2), "")
	insertMemory(t, db, "01AND", "gotcha", "android-mem", "d", "arctop-android", "b", ts(1), "")

	// With the parent as an extra scope: own + parent + globals, newest first,
	// excluding the sibling's own memory.
	got, err := ActiveMemoriesForScope(ctx, db, "arctop-ios", []string{"arctop-mobile-apps"})
	require.NoError(t, err)
	require.Equal(t, []string{"ios-mem", "shared-mem", "global-mem"}, memNames(got))

	// With no extra scope it is exactly ActiveMemories (own + globals).
	got, err = ActiveMemoriesForScope(ctx, db, "arctop-ios", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"ios-mem", "global-mem"}, memNames(got))

	// Blank and duplicate extras are ignored (no error, no double-count).
	got, err = ActiveMemoriesForScope(ctx, db, "arctop-ios", []string{"", "arctop-ios", "arctop-mobile-apps"})
	require.NoError(t, err)
	require.Equal(t, []string{"ios-mem", "shared-mem", "global-mem"}, memNames(got))
}

func TestProjectParentAndRetire(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

	_, err := EnsureProject(ctx, db, "arctop-ios", "Arctop iOS")
	require.NoError(t, err)
	_, err = EnsureProject(ctx, db, "arctop-mobile-apps", "Arctop Mobile")
	require.NoError(t, err)
	_, err = EnsureProject(ctx, db, "arctop-app", "Arctop App")
	require.NoError(t, err)

	// Fresh projects have no parent and are not retired.
	p, _, err := ProjectBySlug(ctx, db, "arctop-ios")
	require.NoError(t, err)
	require.Empty(t, p.ParentSlug)
	require.False(t, p.Retired())

	require.NoError(t, SetProjectParent(ctx, db, "arctop-ios", "arctop-mobile-apps", now))
	p, _, err = ProjectBySlug(ctx, db, "arctop-ios")
	require.NoError(t, err)
	require.Equal(t, "arctop-mobile-apps", p.ParentSlug)

	require.NoError(t, RetireProject(ctx, db, "arctop-app", now))
	src, _, err := ProjectBySlug(ctx, db, "arctop-app")
	require.NoError(t, err)
	require.True(t, src.Retired())
	require.NotNil(t, src.RetiredAt)

	// Setters are idempotent -- re-running them is a harmless no-op.
	require.NoError(t, SetProjectParent(ctx, db, "arctop-ios", "arctop-mobile-apps", now))
	require.NoError(t, RetireProject(ctx, db, "arctop-app", now))

	// The fields round-trip through ListProjects too.
	all, err := ListProjects(ctx, db)
	require.NoError(t, err)
	byslug := map[string]core.Project{}
	for _, pr := range all {
		byslug[pr.Slug] = pr
	}
	require.Equal(t, "arctop-mobile-apps", byslug["arctop-ios"].ParentSlug)
	require.True(t, byslug["arctop-app"].Retired())
}

func TestUpdateTask_ReassignsProject(t *testing.T) {
	db := newTaskDB(t)
	ctx := context.Background()
	id := addTask(t, db, "arctop-app", "port the build", 1)

	to := "arctop-ios"
	updated, err := UpdateTask(ctx, db, id, TaskPatch{ProjectSlug: &to}, "", time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "arctop-ios", updated.ProjectSlug)

	// It persists and the task now appears under the new project's ready queue.
	require.Equal(t, []string{"port the build"}, readyTitles(t, db, "arctop-ios"))
	require.Empty(t, readyTitles(t, db, "arctop-app"))
}
