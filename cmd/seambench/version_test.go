package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationSkew(t *testing.T) {
	tests := []struct {
		name           string
		baseline, cand []string
		wantExtra      []string
		wantErr        string
	}{
		{
			name:     "identical refs",
			baseline: []string{"001_a.sql", "002_b.sql"},
			cand:     []string{"001_a.sql", "002_b.sql"},
		},
		{
			name:      "the candidate appended migrations",
			baseline:  []string{"001_a.sql", "002_b.sql"},
			cand:      []string{"001_a.sql", "002_b.sql", "003_c.sql", "004_d.sql"},
			wantExtra: []string{"003_c.sql", "004_d.sql"},
		},
		{
			// An edited migration means the two refs disagree about history
			// that is already applied; no comparison across that is meaningful.
			name:     "an applied migration was edited",
			baseline: []string{"001_a.sql", "002_b.sql"},
			cand:     []string{"001_a.sql", "002_renamed.sql", "003_c.sql"},
			wantErr:  "already-applied migration was edited",
		},
		{
			name:     "the candidate is behind the baseline",
			baseline: []string{"001_a.sql", "002_b.sql", "003_c.sql"},
			cand:     []string{"001_a.sql", "002_b.sql"},
			wantErr:  "candidate is behind the baseline",
		},
		{
			name:      "an empty baseline is a prefix of everything",
			baseline:  nil,
			cand:      []string{"001_a.sql"},
			wantExtra: []string{"001_a.sql"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra, err := migrationSkew(tt.baseline, tt.cand)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantExtra, extra)
		})
	}
}

func TestMigrationNames_ReadsThisCheckout(t *testing.T) {
	root, err := resolveRepoRoot(context.Background(), "")
	require.NoError(t, err)

	names, err := migrationNames(root)
	require.NoError(t, err)
	require.NotEmpty(t, names)
	require.Equal(t, "001_initial.sql", names[0])
	for _, n := range names {
		require.True(t, strings.HasSuffix(n, ".sql"), n)
	}

	_, err = migrationNames(t.TempDir())
	require.ErrorContains(t, err, "read migrations")
}

func TestVersionSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v0.4.5", "v0.4.5"},
		{"v0.4.5-12-gabc1234-dirty", "v0.4.5-12-gabc1234-dirty"},
		{"feature/branch", "feature-branch"},
		{"../escape", "escape"},
		{"", "version"},
		{"///", "version"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := versionSlug(tt.in)
			require.Equal(t, tt.want, got)
			require.NotContains(t, got, string(os.PathSeparator), "a version label must stay one path element")
		})
	}
}

// The baseline arm is built from its OWN ref, in a throwaway checkout that
// never touches the user's working tree and never outlives the run.
func TestBaselineWorktree_MaterializesTheRefAndCleansUp(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "dev@example.com")
	git("config", "user.name", "dev")
	marker := filepath.Join(repo, "marker.txt")
	require.NoError(t, os.WriteFile(marker, []byte("old\n"), 0o644))
	git("add", "-A")
	git("commit", "-qm", "old")
	old := git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(marker, []byte("new\n"), 0o644))
	git("add", "-A")
	git("commit", "-qm", "new")

	dir := filepath.Join(t.TempDir(), "baseline-src")
	src, err := addBaselineWorktree(ctx, repo, old, dir)
	require.NoError(t, err)

	b, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
	require.NoError(t, err)
	require.Equal(t, "old\n", string(b), "the baseline arm builds from the baseline ref, not the working tree")
	require.Equal(t, "new\n", readFile(t, marker), "the user's checkout is untouched")
	require.Contains(t, git("worktree", "list"), dir)

	src.remove(ctx)
	require.NoDirExists(t, dir)
	require.NotContains(t, git("worktree", "list"), dir, "no stray worktree registration is left behind")

	t.Run("a leftover checkout from an interrupted run is replaced", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "junk"), []byte("x"), 0o644))
		src, err := addBaselineWorktree(ctx, repo, old, dir)
		require.NoError(t, err)
		defer src.remove(ctx)
		require.NoFileExists(t, filepath.Join(dir, "junk"))
	})

	t.Run("an unknown ref fails before anything is built", func(t *testing.T) {
		_, err := addBaselineWorktree(ctx, repo, "no-such-ref", filepath.Join(t.TempDir(), "wt"))
		require.ErrorContains(t, err, "check out the baseline ref")
	})
}

func TestSuite_CompareRefusesReusedArms(t *testing.T) {
	s := &suite{reuseArms: true, w: os.Stderr}
	err := s.compare(context.Background(), versionArm{repoRoot: t.TempDir(), base: t.TempDir()}, "HEAD~1")
	require.ErrorContains(t, err, "--reuse-arms cannot be combined with --baseline")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
