package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Blank slug is the global scope: never registered.
	p, err := EnsureProject(ctx, db, "", "")
	require.NoError(t, err)
	require.Empty(t, p.ID)
	require.Empty(t, ListProjectsSlugs(t, db))

	// First call creates; a blank name falls back to the slug.
	p, err = EnsureProject(ctx, db, "seamless", "")
	require.NoError(t, err)
	require.NotEmpty(t, p.ID)
	require.Equal(t, "seamless", p.Slug)
	require.Equal(t, "seamless", p.Name)

	// Second call is idempotent: same row, no duplicate.
	again, err := EnsureProject(ctx, db, "seamless", "Ignored New Name")
	require.NoError(t, err)
	require.Equal(t, p.ID, again.ID)
	require.Equal(t, "seamless", again.Name) // existing name preserved
	require.Equal(t, []string{"seamless"}, ListProjectsSlugs(t, db))
}

func TestRegisterProjectForCWD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A cwd inside a git repo registers a new mapping + project derived from the
	// repo directory name, and resolves subdirectories to it.
	root := filepath.Join(t.TempDir(), "My Cool Repo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	sub := filepath.Join(root, "internal", "mcp")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	slug, err := RegisterProjectForCWD(ctx, db, sub)
	require.NoError(t, err)
	require.Equal(t, "my-cool-repo", slug)

	// The projects-table row exists (project_list would show it).
	_, ok, err := ProjectBySlug(ctx, db, "my-cool-repo")
	require.NoError(t, err)
	require.True(t, ok)

	// The map grew and now resolves the cwd read-only.
	got, err := ResolveProjectForCWD(ctx, db, sub)
	require.NoError(t, err)
	require.Equal(t, "my-cool-repo", got)

	// Re-registering the same repo is idempotent: no new project, same slug.
	slug2, err := RegisterProjectForCWD(ctx, db, root)
	require.NoError(t, err)
	require.Equal(t, "my-cool-repo", slug2)
	require.Equal(t, []string{"my-cool-repo"}, ListProjectsSlugs(t, db))

	// A cwd outside any git repo stays global and registers nothing.
	nonGit := t.TempDir()
	slug3, err := RegisterProjectForCWD(ctx, db, nonGit)
	require.NoError(t, err)
	require.Empty(t, slug3)
	require.Equal(t, []string{"my-cool-repo"}, ListProjectsSlugs(t, db))

	// A blank cwd is global.
	slug4, err := RegisterProjectForCWD(ctx, db, "")
	require.NoError(t, err)
	require.Empty(t, slug4)
}

// An empty slug means "the global scope", so a store failure must stay
// distinguishable from a legitimately unmapped cwd -- both return an empty slug,
// and only the error tells them apart. A caller that drops it binds the agent to
// global memory for the whole session over a transient hiccup. Regression for
// F16, where a retrieve wrapper swallowed the error and returned "" for both.
func TestRegisterProjectForCWDFailureIsNotGlobal(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	nonGit := t.TempDir()

	// Legitimately unmapped: empty slug, no error.
	slug, err := RegisterProjectForCWD(ctx, db, nonGit)
	require.NoError(t, err)
	require.Empty(t, slug)

	// Same empty slug, but now carrying an error the caller must not discard.
	require.NoError(t, db.Close())
	slug, err = RegisterProjectForCWD(ctx, db, nonGit)
	require.Error(t, err, "a store failure must not be reported as an unmapped cwd")
	require.Empty(t, slug)
}

func TestRegisterProjectForCWDSlugCollision(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Two distinct repos with the same directory name must get distinct projects
	// so their memories never bleed together.
	mkRepo := func(name string) string {
		root := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		return root
	}
	a := mkRepo("backend")
	b := mkRepo("backend")

	slugA, err := RegisterProjectForCWD(ctx, db, a)
	require.NoError(t, err)
	require.Equal(t, "backend", slugA)

	slugB, err := RegisterProjectForCWD(ctx, db, b)
	require.NoError(t, err)
	require.Equal(t, "backend-2", slugB)

	// Both resolve to their own project.
	got, err := ResolveProjectForCWD(ctx, db, a)
	require.NoError(t, err)
	require.Equal(t, "backend", got)
	got, err = ResolveProjectForCWD(ctx, db, b)
	require.NoError(t, err)
	require.Equal(t, "backend-2", got)
}

// ListProjectsSlugs returns the registered project slugs, for test assertions.
func ListProjectsSlugs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	ps, err := ListProjects(context.Background(), db)
	require.NoError(t, err)
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Slug)
	}
	return out
}
