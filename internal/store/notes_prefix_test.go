package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// insertNoteTags inserts a notes_index row carrying an explicit JSON tags array.
func insertNoteTags(t *testing.T, db *sql.DB, id, project, tagsJSON, updated string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO notes_index
		    (id, title, slug, description, project, file_path, tags, source_url,
		     content_hash, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, '', 'h', ?, ?)`,
		id, id, id, project, "notes/"+dirOf(project)+"/"+id+".md", tagsJSON, updated, updated)
	require.NoError(t, err)
}

// TestNotesByTagPrefix returns only notes with a tag under the prefix, matches
// the prefix literally (plan: must not catch plan-status:), and honors the
// project scope.
func TestNotesByTagPrefix(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	insertNoteTags(t, db, "narr", "seam", `["plan:refactor","created-by:agent"]`, "2026-07-13T03:00:00Z")
	insertNoteTags(t, db, "supp", "seam", `["plan:refactor"]`, "2026-07-13T02:00:00Z")
	insertNoteTags(t, db, "otherproj", "web", `["plan:ui-pass"]`, "2026-07-13T01:00:00Z")
	insertNoteTags(t, db, "lifecycle", "seam", `["plan-status:approved"]`, "2026-07-13T04:00:00Z")
	insertNoteTags(t, db, "loose", "seam", `["created-by:agent"]`, "2026-07-13T05:00:00Z")

	all, err := NotesByTagPrefix(ctx, db, "", "plan:")
	require.NoError(t, err)
	ids := noteIDs(all)
	require.ElementsMatch(t, []string{"narr", "supp", "otherproj"}, ids)
	// plan-status: is not under the plan: prefix, and untagged notes are excluded.
	require.NotContains(t, ids, "lifecycle")
	require.NotContains(t, ids, "loose")
	// Newest-updated first within the result.
	require.Equal(t, "narr", all[0].ID)

	scoped, err := NotesByTagPrefix(ctx, db, "seam", "plan:")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"narr", "supp"}, noteIDs(scoped))
}

// TestGetNavCounts_CountsPlans counts distinct plan:<slug> compositions: a
// capture and its agent-cache share one slug, a composed note is a second, and
// plan-status: / untagged notes must not count.
func TestGetNavCounts_CountsPlans(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	insertNoteTags(t, db, "cap", "seam", `["plan:alpha","cc-plan"]`, "2026-07-13T03:00:00Z")
	insertNoteTags(t, db, "agent", "seam", `["plan:alpha","agent"]`, "2026-07-13T02:00:00Z")
	insertNoteTags(t, db, "composed", "web", `["plan:beta"]`, "2026-07-13T01:00:00Z")
	insertNoteTags(t, db, "life", "seam", `["plan-status:approved"]`, "2026-07-13T04:00:00Z")
	insertNoteTags(t, db, "loose", "seam", `["created-by:agent"]`, "2026-07-13T05:00:00Z")

	n, err := GetNavCounts(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 2, n.Plans) // distinct plan:<slug>: alpha, beta (plan-status: excluded)
}

func noteIDs(notes []core.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	return out
}
