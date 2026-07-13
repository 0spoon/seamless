package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStampHead(t *testing.T) {
	body := "> captured from cc/abcdef12 | clever-stallman.md | iter 3 | git feedface1234 | 2026-07-12T00:00:00Z\n\n# Plan\n"
	require.Equal(t, "feedface1234", stampHead(body))

	agent := "> captured from cc/abcdef12 | agent abc123 | git unknown | 2026-07-12T00:00:00Z\n\n## Prompt\n"
	require.Equal(t, "unknown", stampHead(agent))

	require.Equal(t, "", stampHead("no stamp here"))
}

func TestMentionedPaths(t *testing.T) {
	body := "Touch `internal/hooks/plans.go:94` and cmd/seam/main.go, plus main.go. " +
		"See docs/design.md; ignore http://example.com/x and version 1.2.3."
	got := mentionedPaths(body)
	require.Contains(t, got, "internal/hooks/plans.go")
	require.Contains(t, got, "cmd/seam/main.go")
	require.Contains(t, got, "main.go")
	require.Contains(t, got, "docs/design.md")
	require.NotContains(t, got, "1.2.3")
}

func TestOverlapSuffixMatching(t *testing.T) {
	changed := []string{"internal/hooks/plans.go", "README.md"}
	mentioned := []string{"/Users/x/repo/internal/hooks/plans.go", "docs/other.md"}
	require.Equal(t, []string{"internal/hooks/plans.go"}, overlap(changed, mentioned))

	// A bare filename mention matches the repo-relative changed path.
	require.Equal(t, []string{"internal/hooks/plans.go"},
		overlap([]string{"internal/hooks/plans.go"}, []string{"plans.go"}))
	require.Empty(t, overlap([]string{"internal/hooks/plans.go"}, []string{"other.go"}))
}

// TestCheckEntryAgainstRealRepo exercises the FRESH/STALE/UNKNOWN verdicts
// against a real throwaway git repo.
func TestCheckEntryAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := gitOut(dir, args...)
		require.NoError(t, err, "git %v", args)
		return out
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "one")
	first := run("rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // changed\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "two")
	head := run("rev-parse", "HEAD")

	stamp := func(h string) string {
		return "> captured from cc/x | agent y | git " + h[:12] + " | 2026-07-12T00:00:00Z\n\n"
	}

	// Mentioned file changed since the stamped commit -> STALE.
	v, detail := checkEntry(dir, head, stamp(first)+"The fix lives in a.go.")
	require.Equal(t, "STALE", v, detail)
	require.Contains(t, detail, "a.go")

	// Mentioned file untouched -> FRESH.
	v, _ = checkEntry(dir, head, stamp(first)+"Only b.go matters here.")
	require.Equal(t, "FRESH", v)

	// Stamped at current HEAD -> FRESH without a diff.
	v, detail = checkEntry(dir, head, stamp(head)+"anything a.go")
	require.Equal(t, "FRESH", v)
	require.Contains(t, detail, "current HEAD")

	// Unresolvable stamp -> UNKNOWN.
	v, _ = checkEntry(dir, head, stamp(strings.Repeat("f", 40))+"a.go")
	require.Equal(t, "UNKNOWN", v)
	v, _ = checkEntry(dir, head, "no stamp\n\na.go")
	require.Equal(t, "UNKNOWN", v)
}
