// Version comparison: running the same suite against two refs.
//
// THE RULE, AND WHY. Each version's arms are built from THAT version's own ref
// -- its harness.sh, its install-hooks, its seamlessd -- into its own throwaway
// base dir, and the two never share a seeded data dir. The tempting shortcut
// (build once, point both arms at the same seeded dir) happens to work today
// because every migration so far has been additive: a baseline daemon opening a
// data dir whose schema_migrations max is above its own list simply applies
// nothing. That is luck, not a guarantee. One destructive or renaming migration
// between the two refs and the baseline arm would be measured against a schema
// it cannot read, which shows up as a plausible uplift regression rather than as
// an error. (plan:seambench, CURRENCY CHECK 2026-07-28.)
//
// WHAT IS STILL SHARED, AND WHY IT HAS TO BE. The scenario fixture -- the seed,
// the prompt, the grader -- is the candidate build's, on both arms. It must be:
// two versions graded against two different experiments do not compare. So the
// seeded data dir is written by candidate-level store code even on the baseline
// arm, which is exactly the hazard above. migrationSkew is the guard: it refuses
// a comparison whose two refs disagree about already-applied history, and names
// the extra migrations when the candidate is merely ahead, so the operator can
// see which SQL is in play instead of discovering it as a bad number.
//
// The baseline checkout is a detached `git worktree add` under the throwaway
// base. The user's working tree is never touched, and the registration is
// removed afterwards.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// migrationsRel is where the schema migrations live in a checkout.
var migrationsRel = filepath.Join("internal", "store", "migrations")

// baselineSrc is a throwaway checkout of the baseline ref.
type baselineSrc struct {
	repoRoot string // the repo that owns the worktree registration
	dir      string // the checkout
	ref      string
}

// addBaselineWorktree materializes ref in dir as a detached worktree, clearing
// any leftover from an interrupted earlier run first -- a missing-but-still-
// registered path is exactly what makes `git worktree add` refuse.
//
// The cleanup is `worktree remove <path>` and never `worktree prune`. Prune
// acts on EVERY registration in the shared repo, so it would drop other agents'
// worktrees whose directories happen to be absent at that moment; remove names
// the one path this benchmark owns and clears a stale registration just as well.
func addBaselineWorktree(ctx context.Context, repoRoot, ref, dir string) (*baselineSrc, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("clear the baseline checkout %s: %w", dir, err)
	}
	if _, err := git(ctx, repoRoot, "worktree", "remove", "--force", dir); err != nil {
		// The normal case: nothing was registered at that path.
		slog.Debug("no previous baseline worktree to clear", "dir", dir, "err", err)
	}
	if _, err := git(ctx, repoRoot, "worktree", "add", "--detach", dir, ref); err != nil {
		return nil, fmt.Errorf("check out the baseline ref %q: %w", ref, err)
	}
	return &baselineSrc{repoRoot: repoRoot, dir: dir, ref: ref}, nil
}

// remove deletes the checkout and its registration. It reports failures rather
// than returning them: this runs on the way out, after the runs are captured,
// and a stray worktree must be visible without costing the results.
func (b *baselineSrc) remove(ctx context.Context) {
	if _, err := git(ctx, b.repoRoot, "worktree", "remove", "--force", b.dir); err != nil {
		fmt.Fprintf(os.Stderr, "seambench: could not remove the baseline worktree %s: %v\n", b.dir, err)
		if err := os.RemoveAll(b.dir); err != nil {
			fmt.Fprintf(os.Stderr, "seambench: could not delete %s either: %v\n", b.dir, err)
		}
	}
}

// migrationNames lists a checkout's migration files, in order.
func migrationNames(repoRoot string) ([]string, error) {
	dir := filepath.Join(repoRoot, migrationsRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations in %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// migrationSkew compares two checkouts' migration sets and returns the ones the
// candidate has beyond the baseline.
//
// The baseline's list must be a PREFIX of the candidate's. Anything else means
// the two refs disagree about history that has already been applied -- an
// edited migration, or a baseline holding migrations the candidate does not --
// and no amount of care at run time makes a comparison across that meaningful.
func migrationSkew(baseline, candidate []string) ([]string, error) {
	if len(baseline) > len(candidate) {
		return nil, fmt.Errorf("the baseline has %d migrations and the candidate %d: "+
			"the candidate is behind the baseline, so the two refs cannot be compared",
			len(baseline), len(candidate))
	}
	for i, name := range baseline {
		if candidate[i] != name {
			return nil, fmt.Errorf("migration %d differs between the refs (%s vs %s): "+
				"an already-applied migration was edited, so a shared fixture schema is not safe",
				i+1, name, candidate[i])
		}
	}
	extra := candidate[len(baseline):]
	if len(extra) == 0 {
		return nil, nil // same schema on both refs: nothing to warn about
	}
	return extra, nil
}

// versionSlugRE matches everything a version label may not put in a path.
var versionSlugRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// versionSlug turns a version label into one path element. Labels come from
// `git describe`, so they are usually already safe -- but a ref like
// "feature/x" is not, and silently creating a nested directory for it would
// scatter one version's runs across two levels.
func versionSlug(label string) string {
	s := versionSlugRE.ReplaceAllString(label, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "version"
	}
	return s
}
