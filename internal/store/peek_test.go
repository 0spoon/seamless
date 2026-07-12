package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertNote inserts a notes_index row directly (no FTS/body needed for the
// list + count queries under test).
func insertNote(t *testing.T, db *sql.DB, id, title, slug, project, updated string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO notes_index
		    (id, title, slug, description, project, file_path, tags, source_url,
		     content_hash, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, '[]', '', 'h', ?, ?)`,
		id, title, slug, project, "notes/"+dirOf(project)+"/"+slug+".md", updated, updated)
	require.NoError(t, err)
}

func TestMemoriesSuperseding(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ts := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	// old1, old2 were both superseded by keep; other is unrelated.
	insertMemory(t, db, "keep", "gotcha", "keep", "d", "seam", "b", ts(3), "")
	insertMemory(t, db, "old1", "gotcha", "old1", "d", "seam", "b", ts(1), ts(3))
	insertMemory(t, db, "old2", "gotcha", "old2", "d", "seam", "b", ts(2), ts(3))
	insertMemory(t, db, "other", "gotcha", "other", "d", "seam", "b", ts(1), "")
	_, err := db.ExecContext(ctx, `UPDATE memories_index SET superseded_by='keep' WHERE id IN ('old1','old2')`)
	require.NoError(t, err)

	got, err := MemoriesSuperseding(ctx, db, "keep")
	require.NoError(t, err)
	names := memNames(got)
	require.ElementsMatch(t, []string{"old1", "old2"}, names)

	// A memory that superseded nothing yields none; empty id is a no-op.
	none, err := MemoriesSuperseding(ctx, db, "other")
	require.NoError(t, err)
	require.Empty(t, none)
	empty, err := MemoriesSuperseding(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestTasksBlockedBy(t *testing.T) {
	db := newTaskDB(t)
	ctx := context.Background()
	// b and c both depend on a; a depends on nothing.
	a := addTask(t, db, "demo", "A", 1)
	b := addTask(t, db, "demo", "B", 2, a)
	c := addTask(t, db, "demo", "C", 3, a)

	// Close c: reverse-blocks has no status filter, so it is still listed.
	setStatus(t, db, c, core.TaskDone)

	blocked, err := TasksBlockedBy(ctx, db, a)
	require.NoError(t, err)
	var ids []string
	for _, tk := range blocked {
		ids = append(ids, tk.ID)
	}
	require.ElementsMatch(t, []string{b, c}, ids)

	// A leaf task blocks nothing; empty id is a no-op.
	leaf, err := TasksBlockedBy(ctx, db, b)
	require.NoError(t, err)
	require.Empty(t, leaf)
	none, err := TasksBlockedBy(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestListNotes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ts := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	insertNote(t, db, "n1", "First", "first", "seam", ts(1))
	insertNote(t, db, "n2", "Second", "second", "", ts(3))
	insertNote(t, db, "n3", "Third", "third", "seam", ts(2))

	notes, err := ListNotes(ctx, db)
	require.NoError(t, err)
	require.Len(t, notes, 3)
	// Newest-updated first.
	require.Equal(t, []string{"n2", "n3", "n1"}, []string{notes[0].ID, notes[1].ID, notes[2].ID})
}

func TestGetProjectCounts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ts := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	// seam: 2 active memories (+1 inactive, excluded), 1 note, 1 session,
	// 1 open + 1 in_progress task (+1 done, excluded).
	insertMemory(t, db, "m1", "gotcha", "m1", "d", "seam", "b", ts(1), "")
	insertMemory(t, db, "m2", "gotcha", "m2", "d", "seam", "b", ts(2), "")
	insertMemory(t, db, "m3", "gotcha", "m3", "d", "seam", "b", ts(3), ts(4)) // inactive
	insertMemory(t, db, "g1", "gotcha", "g1", "d", "", "b", ts(1), "")        // global, other project
	insertNote(t, db, "n1", "N", "n", "seam", ts(1))

	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "s1", Name: "cc/x", ProjectSlug: "seam", Status: core.SessionActive,
		CreatedAt: base, UpdatedAt: base,
	}))
	open := addTask(t, db, "seam", "open", 1)
	_ = open
	inprog := addTask(t, db, "seam", "inprog", 2)
	setStatus(t, db, inprog, core.TaskInProgress)
	done := addTask(t, db, "seam", "done", 3)
	setStatus(t, db, done, core.TaskDone)
	addTask(t, db, "other", "elsewhere", 4) // different project, excluded

	c, err := GetProjectCounts(ctx, db, "seam")
	require.NoError(t, err)
	require.Equal(t, ProjectCounts{Memories: 2, Sessions: 1, OpenTasks: 2, Notes: 1}, c)

	// Unknown slug yields all zeros, not an error.
	z, err := GetProjectCounts(ctx, db, "nope")
	require.NoError(t, err)
	require.Equal(t, ProjectCounts{}, z)
}

func TestGetNavCounts_IncludesNotes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	ts := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }
	insertNote(t, db, "n1", "A", "a", "seam", ts(1))
	insertNote(t, db, "n2", "B", "b", "", ts(2))

	n, err := GetNavCounts(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 2, n.Notes)
}
