package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertSearchTask inserts a task row directly, so a search test can pin
// plan_slug and updated_at without going through the queue's write path.
func insertSearchTask(t *testing.T, db *sql.DB, id, project, title, planSlug, updated string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO tasks (id, project_slug, title, body, status, created_by,
		    plan_slug, claimed_by, lease_expires_at, created_at, updated_at, closed_at)
		VALUES (?, ?, ?, '', 'open', 'test', ?, '', NULL, ?, ?, NULL)`,
		id, project, title, planSlug, updated, updated)
	require.NoError(t, err)
}

func insertSearchNote(t *testing.T, db *sql.DB, id, title, slug, project, tagsJSON, updated string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO notes_index
		    (id, title, slug, description, project, file_path, tags, source_url,
		     content_hash, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, '', 'h', ?, ?)`,
		id, title, slug, project, "notes/"+dirOf(project)+"/"+slug+".md", tagsJSON, updated, updated)
	require.NoError(t, err)
}

func TestSearchTasks_TitleAndID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())

	insertSearchTask(t, db, "01TASKA", "seam", "wire the search palette", "", now)
	insertSearchTask(t, db, "01TASKB", "seam", "unrelated chore", "", now)

	got, err := SearchTasks(ctx, db, "palette", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01TASKA", got[0].ID)

	// Case-insensitive.
	got, err = SearchTasks(ctx, db, "PALETTE", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// An exact id is a lookup, even though it is not a title substring.
	got, err = SearchTasks(ctx, db, "01TASKB", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01TASKB", got[0].ID)
}

// LIKE metacharacters in the needle must match themselves. Without ESCAPE, "%"
// would match every task and "_" would match any single character.
func TestSearchTasks_EscapesLikeMetacharacters(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())

	insertSearchTask(t, db, "01PCT", "seam", "ship at 50% coverage", "", now)
	insertSearchTask(t, db, "01UND", "seam", "rename a_b to c", "", now)
	insertSearchTask(t, db, "01PLAIN", "seam", "nothing special", "", now)

	got, err := SearchTasks(ctx, db, "50%", 20)
	require.NoError(t, err)
	require.Len(t, got, 1, "%% must be literal, not a wildcard")
	require.Equal(t, "01PCT", got[0].ID)

	got, err = SearchTasks(ctx, db, "a_b", 20)
	require.NoError(t, err)
	require.Len(t, got, 1, "_ must be literal, not a single-char wildcard")
	require.Equal(t, "01UND", got[0].ID)

	// A bare wildcard is a literal search for "%": it finds the one title that
	// actually contains the character, not every row.
	got, err = SearchTasks(ctx, db, "%", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01PCT", got[0].ID)
}

func TestSearchTasks_LimitAndOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	at := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	insertSearchTask(t, db, "01OLD", "seam", "search one", "", at(1))
	insertSearchTask(t, db, "01MID", "seam", "search two", "", at(2))
	insertSearchTask(t, db, "01NEW", "seam", "search three", "", at(3))

	got, err := SearchTasks(ctx, db, "search", 20)
	require.NoError(t, err)
	require.Equal(t, []string{"01NEW", "01MID", "01OLD"}, []string{got[0].ID, got[1].ID, got[2].ID},
		"newest-updated first")

	got, err = SearchTasks(ctx, db, "search", 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "01NEW", got[0].ID)
}

func TestSearchSessions_NameAndID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01SESSA", Name: "cc/palette-work", ProjectSlug: "seam",
		Status: core.SessionActive, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, CreateSession(ctx, db, core.Session{
		ID: "01SESSB", Name: "cc/other", ProjectSlug: "seam",
		Status: core.SessionActive, CreatedAt: now, UpdatedAt: now,
	}))

	got, err := SearchSessions(ctx, db, "palette", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01SESSA", got[0].ID)

	got, err = SearchSessions(ctx, db, "01SESSB", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01SESSB", got[0].ID)
}

func TestSearchProjects_SlugAndName(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, CreateProject(ctx, db, core.Project{
		ID: "01PA", Slug: "seamless", Name: "Seamless", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, CreateProject(ctx, db, core.Project{
		ID: "01PB", Slug: "odysseus", Name: "Odysseus Stack", CreatedAt: now, UpdatedAt: now,
	}))

	// By slug.
	got, err := SearchProjects(ctx, db, "seam", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "seamless", got[0].Slug)

	// By display name, case-insensitively.
	got, err = SearchProjects(ctx, db, "stack", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "odysseus", got[0].Slug)
}

// A plan is bi-sourced: either its note or its tasks can carry the match, and
// the same (project, slug) reached through both must collapse to one row that
// prefers the note's real title over the slug fallback.
func TestSearchPlans_MergesNoteAndTaskSources(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	at := func(min int) string { return core.FormatTime(base.Add(time.Duration(min) * time.Minute)) }

	// Bi-sourced: note + tasks, same (project, slug).
	insertSearchNote(t, db, "01N1", "Console search plan", "console-search-plan", "seam",
		`["plan:console-search"]`, at(1))
	insertSearchTask(t, db, "01T1", "seam", "step one", "console-search", at(5))

	// Task-only plan.
	insertSearchTask(t, db, "01T2", "seam", "lonely step", "search-orphan", at(3))

	// Unrelated.
	insertSearchTask(t, db, "01T3", "seam", "chore", "billing", at(4))

	got, err := SearchPlans(ctx, db, "search", 20)
	require.NoError(t, err)
	require.Len(t, got, 2, "console-search must collapse to one row; billing must not match")

	bySlug := map[string]PlanSearchRow{}
	for _, r := range got {
		bySlug[r.Slug] = r
	}
	merged, ok := bySlug["console-search"]
	require.True(t, ok)
	require.Equal(t, "seam", merged.Project)
	require.Equal(t, "Console search plan", merged.Title, "the note title beats the slug fallback")
	require.Equal(t, at(5), core.FormatTime(merged.Updated), "the newer source wins Updated")

	orphan, ok := bySlug["search-orphan"]
	require.True(t, ok)
	require.Equal(t, "search-orphan", orphan.Title, "a task-only plan falls back to its slug")

	// Newest-updated first across the merged set.
	require.Equal(t, "console-search", got[0].Slug)
}

// The same slug under two projects is two plans, not one.
func TestSearchPlans_SameSlugDistinctProjects(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := core.FormatTime(time.Now().UTC())

	insertSearchTask(t, db, "01PA1", "alpha", "step", "shared-plan", now)
	insertSearchTask(t, db, "01PB1", "beta", "step", "shared-plan", now)

	got, err := SearchPlans(ctx, db, "shared", 20)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.ElementsMatch(t, []string{"alpha", "beta"}, []string{got[0].Project, got[1].Project})
}

func TestSearchPlans_Limit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	for i, slug := range []string{"plan-a", "plan-b", "plan-c"} {
		insertSearchTask(t, db, "01L"+slug, "seam", "step",
			slug, core.FormatTime(base.Add(time.Duration(i)*time.Minute)))
	}
	got, err := SearchPlans(ctx, db, "plan-", 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "plan-c", got[0].Slug, "the limit cuts the tail, not the head")
}
