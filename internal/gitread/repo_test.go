package gitread

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// mkLinkedWorktree wires wtRoot up as a linked worktree of mainRoot using the
// on-disk layout git produces (.git file -> admin dir -> commondir), so the
// tests exercise MainWorktreeRoot without needing a git executable.
func mkLinkedWorktree(t *testing.T, mainRoot, wtRoot, wtName string, relativeGitdir bool) {
	t.Helper()
	admin := filepath.Join(mainRoot, ".git", "worktrees", wtName)
	require.NoError(t, os.MkdirAll(admin, 0o755))
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	require.NoError(t, os.MkdirAll(wtRoot, 0o755))
	gitdir := admin
	if relativeGitdir {
		rel, err := filepath.Rel(wtRoot, admin)
		require.NoError(t, err)
		gitdir = rel
	}
	writeFile(t, filepath.Join(wtRoot, ".git"), "gitdir: "+gitdir+"\n")
}

func TestRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "backend")
	deep := filepath.Join(repo, "internal", "store")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(deep, 0o755))

	require.Equal(t, repo, RepoRoot(repo), "the root itself counts")
	require.Equal(t, repo, RepoRoot(deep), "nearest ancestor with a .git")
	require.Equal(t, repo, RepoRoot(deep+string(filepath.Separator)), "cleaned first")

	// A .git file (worktree, submodule) counts as well as a directory.
	wt := filepath.Join(base, "backend-hotfix")
	require.NoError(t, os.MkdirAll(wt, 0o755))
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /nowhere\n")
	require.Equal(t, wt, RepoRoot(wt))

	// Outside any repo -> "", never an error.
	outside := t.TempDir()
	require.Equal(t, "", RepoRoot(outside))
}

func TestMainWorktreeRoot(t *testing.T) {
	t.Run("regular checkout resolves to itself", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
		require.Equal(t, repo, MainWorktreeRoot(repo))
	})

	t.Run("out-of-tree worktree resolves to the main checkout", func(t *testing.T) {
		base := t.TempDir()
		main := filepath.Join(base, "backend")
		require.NoError(t, os.MkdirAll(filepath.Join(main, ".git"), 0o755))
		wt := filepath.Join(base, "backend-hotfix")
		mkLinkedWorktree(t, main, wt, "backend-hotfix", false)
		require.Equal(t, main, MainWorktreeRoot(wt))
	})

	t.Run("in-tree worktree with a relative gitdir", func(t *testing.T) {
		main := filepath.Join(t.TempDir(), "backend")
		require.NoError(t, os.MkdirAll(filepath.Join(main, ".git"), 0o755))
		wt := filepath.Join(main, ".claude", "worktrees", "youthful-shamir")
		mkLinkedWorktree(t, main, wt, "youthful-shamir", true)
		require.Equal(t, main, MainWorktreeRoot(wt))
	})

	t.Run("submodule keeps its own identity", func(t *testing.T) {
		// A submodule's .git is a file too, but its admin dir (under the
		// superproject's .git/modules/) has no commondir: it is a genuinely
		// separate repository.
		super := filepath.Join(t.TempDir(), "super")
		modAdmin := filepath.Join(super, ".git", "modules", "lib")
		require.NoError(t, os.MkdirAll(modAdmin, 0o755))
		sub := filepath.Join(super, "lib")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		writeFile(t, filepath.Join(sub, ".git"), "gitdir: "+modAdmin+"\n")
		require.Equal(t, sub, MainWorktreeRoot(sub))
	})

	t.Run("unparseable and stale layouts resolve to the root", func(t *testing.T) {
		for name, content := range map[string]string{
			"no gitdir prefix": "something else\n",
			"empty gitdir":     "gitdir:\n",
			"missing admin":    "gitdir: /nowhere/at/all\n",
		} {
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, ".git"), content)
				require.Equal(t, root, MainWorktreeRoot(root))
			})
		}

		// A commondir that is not named .git has no main checkout to point at
		// (a bare main repo), so the worktree stays its own root.
		base := t.TempDir()
		bare := filepath.Join(base, "backend.git")
		admin := filepath.Join(bare, "worktrees", "wt")
		require.NoError(t, os.MkdirAll(admin, 0o755))
		writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
		wt := filepath.Join(base, "wt")
		require.NoError(t, os.MkdirAll(wt, 0o755))
		writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
		require.Equal(t, wt, MainWorktreeRoot(wt))
	})

	t.Run("no repo at all", func(t *testing.T) {
		dir := t.TempDir()
		require.Equal(t, dir, MainWorktreeRoot(dir))
		require.Equal(t, "", MainWorktreeRoot(""))
	})
}

func TestOriginURL(t *testing.T) {
	const config = `[core]
	repositoryformatversion = 0
	bare = false
[remote "upstream"]
	url = git@github.com:someone/else.git
	fetch = +refs/heads/*:refs/remotes/upstream/*
; a comment
[remote "origin"]
	url = git@github.com:0spoon/seamless.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
`
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".git", "config"), config)
	require.Equal(t, "git@github.com:0spoon/seamless.git", OriginURL(repo))

	t.Run("a linked worktree reads the shared config", func(t *testing.T) {
		base := t.TempDir()
		main := filepath.Join(base, "backend")
		writeFile(t, filepath.Join(main, ".git", "config"), config)
		wt := filepath.Join(base, "backend-hotfix")
		mkLinkedWorktree(t, main, wt, "backend-hotfix", false)
		require.Equal(t, "git@github.com:0spoon/seamless.git", OriginURL(wt))
	})

	t.Run("no origin is empty, not another remote", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".git", "config"),
			"[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:someone/else.git\n")
		require.Equal(t, "", OriginURL(repo))
	})

	t.Run("a re-pointed origin takes the last url", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".git", "config"),
			"[remote \"origin\"]\n\turl = https://old.example/x.git\n[remote \"origin\"]\n\tURL = https://new.example/x.git\n")
		require.Equal(t, "https://new.example/x.git", OriginURL(repo))
	})

	t.Run("a subsection named Origin is a different remote", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".git", "config"),
			"[remote \"Origin\"]\n\turl = https://example/x.git\n")
		require.Equal(t, "", OriginURL(repo))
	})

	t.Run("no config, no repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
		require.Equal(t, "", OriginURL(repo))
		require.Equal(t, "", OriginURL(t.TempDir()))
		require.Equal(t, "", OriginURL(""))
	})
}
