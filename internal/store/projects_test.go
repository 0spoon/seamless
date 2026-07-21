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

// mkLinkedWorktree wires wtRoot up as a linked worktree of mainRoot using the
// real on-disk layout git produces (.git file -> admin dir -> commondir), so
// the tests exercise gitMainWorktreeRoot without needing a git executable.
func mkLinkedWorktree(t *testing.T, mainRoot, wtRoot, wtName string, relativeGitdir bool) {
	t.Helper()
	admin := filepath.Join(mainRoot, ".git", "worktrees", wtName)
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	require.NoError(t, os.MkdirAll(wtRoot, 0o755))
	gitdir := admin
	if relativeGitdir {
		rel, err := filepath.Rel(wtRoot, admin)
		require.NoError(t, err)
		gitdir = rel
	}
	require.NoError(t, os.WriteFile(filepath.Join(wtRoot, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
}

func TestRegisterProjectForCWDManagedWorktreeFirstContact(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A first-ever session landing inside a managed worktree
	// (<repo>/.claude/worktrees/<name>) must register the main checkout's
	// project, not a transient project named after the worktree directory.
	// The relative gitdir mirrors what git writes for in-tree worktrees.
	main := filepath.Join(t.TempDir(), "backend")
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".git"), 0o755))
	wt := filepath.Join(main, ".claude", "worktrees", "youthful-shamir")
	mkLinkedWorktree(t, main, wt, "youthful-shamir", true)

	slug, err := RegisterProjectForCWD(ctx, db, filepath.Join(wt, "internal"))
	require.NoError(t, err)
	require.Equal(t, "backend", slug)
	require.Equal(t, []string{"backend"}, ListProjectsSlugs(t, db))

	// The mapping keys the main root, so the worktree resolves by prefix and no
	// per-worktree entry accumulates for in-tree (transient) worktrees.
	m, err := RepoProjectMap(ctx, db)
	require.NoError(t, err)
	require.Equal(t, map[string]string{main: "backend"}, m)

	got, err := ResolveProjectForCWD(ctx, db, wt)
	require.NoError(t, err)
	require.Equal(t, "backend", got)
}

func TestRegisterProjectForCWDOutOfTreeWorktree(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	base := t.TempDir()
	main := filepath.Join(base, "backend")
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".git"), 0o755))
	wt := filepath.Join(base, "backend-hotfix")
	mkLinkedWorktree(t, main, wt, "backend-hotfix", false)

	// Main checkout already mapped: the worktree adopts its project and gets its
	// own map entry so read paths resolve it too.
	require.NoError(t, AddRepoMapping(ctx, db, main, "backend"))
	slug, err := RegisterProjectForCWD(ctx, db, wt)
	require.NoError(t, err)
	require.Equal(t, "backend", slug)

	got, err := ResolveProjectForCWD(ctx, db, wt)
	require.NoError(t, err)
	require.Equal(t, "backend", got)

	// A second out-of-tree worktree with nothing mapped at all: registration
	// derives the project from the main checkout and maps both roots.
	db2 := openTestDB(t)
	slug, err = RegisterProjectForCWD(ctx, db2, wt)
	require.NoError(t, err)
	require.Equal(t, "backend", slug)
	m, err := RepoProjectMap(ctx, db2)
	require.NoError(t, err)
	require.Equal(t, map[string]string{main: "backend", wt: "backend"}, m)
}

func TestRegisterProjectForCWDSubmoduleStaysItsOwnProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A submodule checkout also has a .git file, but its gitdir (under the
	// superproject's .git/modules/) has no commondir file: it is a genuinely
	// separate repository and must keep its own project identity.
	super := filepath.Join(t.TempDir(), "super")
	modAdmin := filepath.Join(super, ".git", "modules", "lib")
	require.NoError(t, os.MkdirAll(modAdmin, 0o755))
	sub := filepath.Join(super, "lib")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+modAdmin+"\n"), 0o644))

	slug, err := RegisterProjectForCWD(ctx, db, sub)
	require.NoError(t, err)
	require.Equal(t, "lib", slug)
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
