// Building the arms: a shell-out to scripts/fixture/harness.sh --mode bench,
// which is the single owner of what an arm contains (throwaway home, demo-repo
// copy, seeded data dir, config + key + port, install-hooks pinned to
// --client claude under the arm's own HOME). The runner deliberately does not
// reimplement any of that -- in particular it never invokes install-hooks
// itself, which is what keeps the live ~/.claude, ~/.codex, and ~/.seamless out
// of reach (memory fixture-install-hooks-needs-home-override).
//
// Conditions are parsed Go-side first (bench.ParseConditions) and handed over
// as Condition.Spec() strings: the two parsers implement the same
// name[:profile[:client]] grammar on purpose, so validating here fails a bad
// list before any arm is built, and loadArm re-checks that what came back is
// what was asked for.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/0spoon/seamless/internal/bench"
)

// harnessScript is the fixture harness, relative to the repo root.
const harnessScript = "scripts/fixture/harness.sh"

// harnessOpts is what the runner passes through to the harness.
type harnessOpts struct {
	repoRoot   string
	base       string
	model      string // "" leaves the harness default
	port       int
	conditions []bench.Condition
	noBuild    bool
	w          io.Writer // harness output is streamed here
}

// buildArms runs the fixture harness once for the whole condition list.
func buildArms(ctx context.Context, o harnessOpts) error {
	script := filepath.Join(o.repoRoot, harnessScript)
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("fixture harness %s: %w", script, err)
	}
	specs := make([]string, len(o.conditions))
	for i, c := range o.conditions {
		specs[i] = c.Spec()
	}
	args := []string{
		"--mode", "bench",
		"--base", o.base,
		"--port", strconv.Itoa(o.port),
		"--conditions", strings.Join(specs, ","),
	}
	if o.model != "" {
		args = append(args, "--model", o.model)
	}
	if o.noBuild {
		args = append(args, "--no-build")
	}

	fmt.Fprintf(o.w, "==> building arms: %s %s\n", harnessScript, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Dir = o.repoRoot
	cmd.Stdout = o.w
	cmd.Stderr = o.w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s --mode bench: %w", harnessScript, err)
	}
	return nil
}

// resolveRepoRoot finds the Seamless repo root holding the fixture harness and
// the built binaries: the flag when set, otherwise the git top-level of the
// working directory.
func resolveRepoRoot(ctx context.Context, flagValue string) (string, error) {
	root := flagValue
	if root == "" {
		out, err := git(ctx, ".", "rev-parse", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("locate the repo root (pass --repo): %w", err)
		}
		root = strings.TrimSpace(out)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repo root %s: %w", root, err)
	}
	if _, err := os.Stat(filepath.Join(abs, harnessScript)); err != nil {
		return "", fmt.Errorf("%s is not a Seamless repo root (%s missing): %w", abs, harnessScript, err)
	}
	return abs, nil
}

// repoVersion labels the runs with the code under test: the git description of
// the repo root, dirty-marked, so a working-tree candidate is never confused
// with the tag it sits on.
func repoVersion(ctx context.Context, root string) string {
	out, err := git(ctx, root, "describe", "--tags", "--always", "--dirty")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}
