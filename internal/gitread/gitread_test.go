package gitread

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestHead(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(gitDir, "refs", "heads", "main"),
		"0123456789abcdef0123456789abcdef01234567\n")
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", Head(repo))

	// packed-refs fallback when the loose ref is absent.
	require.NoError(t, os.Remove(filepath.Join(gitDir, "refs", "heads", "main")))
	writeFile(t, filepath.Join(gitDir, "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted\nfeedfacefeedfacefeedfacefeedfacefeedface refs/heads/main\n")
	require.Equal(t, "feedfacefeedfacefeedfacefeedfacefeedface", Head(repo))

	// Detached HEAD is the hash itself.
	writeFile(t, filepath.Join(gitDir, "HEAD"), "aaaabbbbccccddddeeeeffff0000111122223333\n")
	require.Equal(t, "aaaabbbbccccddddeeeeffff0000111122223333", Head(repo))

	// No repo -> empty, never an error.
	require.Equal(t, "", Head(t.TempDir()))
	require.Equal(t, "", Head(""))
}

func TestHeadFollowsWorktreeGitFile(t *testing.T) {
	main := t.TempDir()
	wtGitDir := filepath.Join(main, ".git", "worktrees", "wt")
	writeFile(t, filepath.Join(wtGitDir, "HEAD"), "ref: refs/heads/feature\n")
	writeFile(t, filepath.Join(wtGitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(main, ".git", "refs", "heads", "feature"),
		"1111222233334444555566667777888899990000\n")

	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+wtGitDir+"\n")
	require.Equal(t, "1111222233334444555566667777888899990000", Head(wt))
}

func TestReflogHEAD(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "cccc000000000000000000000000000000000000\n")
	writeFile(t, filepath.Join(gitDir, "logs", "HEAD"),
		"0000000000000000000000000000000000000000 aaaa000000000000000000000000000000000000 A U Thor <a@b.c> 1720000000 +0000\tcommit (initial): first\n"+
			"aaaa000000000000000000000000000000000000 bbbb000000000000000000000000000000000000 A U Thor <a@b.c> 1720000100 +0000\tcommit: teach the pass a trick\n"+
			"not a reflog line\n"+
			"bbbb000000000000000000000000000000000000 cccc000000000000000000000000000000000000 A U Thor <a@b.c> 1720000200 +0000\tmerge topic: Fast-forward\n")

	entries := ReflogHEAD(repo)
	require.Len(t, entries, 3, "the malformed line is skipped")
	require.Equal(t, "commit (initial): first", entries[0].Message)
	require.Equal(t, "aaaa000000000000000000000000000000000000", entries[1].Old)
	require.Equal(t, "bbbb000000000000000000000000000000000000", entries[1].New)
	require.Equal(t, "commit: teach the pass a trick", entries[1].Message)
	require.Equal(t, "merge topic: Fast-forward", entries[2].Message)

	// No log, no repo -> nil, never an error.
	bare := t.TempDir()
	writeFile(t, filepath.Join(bare, ".git", "HEAD"), "cccc000000000000000000000000000000000000\n")
	require.Nil(t, ReflogHEAD(bare))
	require.Nil(t, ReflogHEAD(t.TempDir()))
	require.Nil(t, ReflogHEAD(""))
}
