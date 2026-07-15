package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	// Test-only, and deliberately: the seam BINARY must not import internal/store
	// (it would drag the ~30 modernc.org/sqlite packages in), but the test binary
	// can, which is what lets TestPlanWindows_MatchTheResolver pin the transcribed
	// planWindows against the canonical resolver instead of trusting it.
	"github.com/0spoon/seamless/internal/store"
)

// The bugs the plans group carried into the table, pinned against the real
// commands(). Each one used to succeed while answering a question other than the
// one asked, which is why neither was ever reported.
//
// parse opens no connection, so these run without a server.

func TestPlanParse_RejectsTheSilentlyIgnoredArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{
			// fs.NArg() was never checked, so the slug was dropped and every plan came
			// back. A full listing looks exactly like a filtered one that matched
			// everything.
			"plan list <slug> listed every plan",
			[]string{"plan", "list", "my-slug"},
			`unexpected argument "my-slug": seam plan list takes no positional arguments`,
		},
		{
			"and the hint names plan show",
			[]string{"plan", "list", "my-slug"},
			"(to show one plan, use: seam plan show <slug>)",
		},
		{
			// runPlanShow read args[0] and ignored the rest.
			"plan show takes exactly one slug",
			[]string{"plan", "show", "a", "b"},
			`unexpected argument "b": seam plan show takes at most 1 positional argument`,
		},
		{
			"plan approve takes exactly one slug",
			[]string{"plan", "approve", "a", "b"},
			"takes at most 1 positional argument",
		},
		{
			"a missing slug is named, not defaulted",
			[]string{"plan", "show"},
			"missing argument: seam plan show requires <slug>",
		},
		{
			"plan check requires a slug",
			[]string{"plan", "check", "--cwd", "/tmp"},
			"missing argument: seam plan check requires <slug>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(commands(), tc.argv)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// --window bogus reached store.ResolveRetrievalWindow's `default:` and silently
// came back as "all time": nothing in the output names the window it answered, so
// a full-history list reads as the 24h one that was asked for.
func TestPlanParse_WindowEnum(t *testing.T) {
	_, err := parse(commands(), []string{"plan", "list", "--window", "bogus"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "valid values are 24h, 7d, 30d, all")

	for _, w := range planWindows {
		p, perr := parse(commands(), []string{"plan", "list", "--window", w})
		require.NoError(t, perr, "%s is canonical", w)
		require.Equal(t, w, *p.opts.(*planListOpts).window)
	}
}

// planWindows is transcribed rather than imported (seam must not pull
// internal/store into a CLI that has no SQLite driver), so it is pinned against
// the real resolver: every value seam accepts must round-trip to its own key, and
// the resolver's silent default must stay the thing the enum keeps unreachable.
//
// What this cannot catch: a window store GAINS that seam does not. The resolver
// is a switch, not an enumerable set, so there is nothing to diff against -- seam
// would reject the new value while it worked everywhere else. That direction is
// caught by the docs table, not here; it is the residual cost of the transcription
// the no-store-import rule buys.
func TestPlanWindows_MatchTheResolver(t *testing.T) {
	now := time.Now()
	for _, w := range planWindows {
		require.Equal(t, w, store.ResolveRetrievalWindow(w, now).Key,
			"seam accepts %q but the resolver does not answer to it", w)
	}
	require.Equal(t, "all", store.ResolveRetrievalWindow("bogus", now).Key,
		"the resolver's silent default is what the enum exists to keep unreachable")
}

// The permuting parser means the slug may come before or after --cwd. This is the
// case requireFlagsFirst used to reject outright, and the docs advertised as an
// error.
func TestPlanParse_FlagsAndSlugInEitherOrder(t *testing.T) {
	for _, argv := range [][]string{
		{"plan", "check", "--cwd", "/tmp/repo", "my-slug"},
		{"plan", "check", "my-slug", "--cwd", "/tmp/repo"},
	} {
		p, err := parse(commands(), argv)
		require.NoError(t, err, "%v", argv)
		require.Equal(t, []string{"my-slug"}, p.pos)
		require.Equal(t, "/tmp/repo", *p.opts.(*planCheckOpts).cwd)
	}
}

// plan show/approve/check had no parser at all: they read args[0] directly, so
// --help was consumed as a slug and looked up as a plan.
func TestPlanParse_HelpIsNotASlug(t *testing.T) {
	for _, argv := range [][]string{
		{"plan", "show", "--help"},
		{"plan", "approve", "--help"},
		{"plan", "check", "--help"},
	} {
		_, err := parse(commands(), argv)
		require.ErrorIs(t, err, flag.ErrHelp, "%v", argv)
	}
}

// Bare `seam plan` used to be an alias for `seam plan list`. Listing is
// `plan list`'s job, and the same reasoning retired bare `seam task` in B3: a
// family name is not a command, but it is not a typo either -- the caller needs
// the word for what they want.
func TestPlanDispatch_BareFamilyNameListsItsSubcommands(t *testing.T) {
	e, _, errb := stubEnv()
	require.Equal(t, 2, dispatch(context.Background(), e, []string{"plan"}))
	require.Contains(t, errb.String(),
		`unknown command "plan": valid values are plan list, plan show, plan check, plan approve`)

	// The unknown-subcommand phrasing is the house one, not the old "(use: ...)".
	e, _, errb = stubEnv()
	require.Equal(t, 2, dispatch(context.Background(), e, []string{"plan", "bogus"}))
	require.Contains(t, errb.String(), `unknown command "plan bogus": valid values are plan list`)
	require.NotContains(t, errb.String(), "(use:")
}

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
