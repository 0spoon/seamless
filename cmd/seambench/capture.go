// Capturing one finished run into its artifact directory, in the layout frozen
// by internal/bench/artifacts.go. The run dir is the entire handoff to the
// grader and the report: nothing else is shared, and nothing here reaches back
// into a live arm.
//
// Capture runs whether or not the run itself succeeded -- a timed-out or
// crashed agent still leaves a repo, an event log, and often a partial
// transcript, and what a partial capture means is the grader's call, not this
// file's.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/bench"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// maxDumpedEvents caps the event dump. A single scenario run produces tens to
// low hundreds of events, so this is a runaway guard, not a window.
const maxDumpedEvents = 100_000

// transcriptSlack is how far before the run's start a transcript file may have
// been last written and still count as this run's. Claude Code appends to the
// file as the session goes, so the mtime lands well after the start; the slack
// only absorbs clock jitter.
const transcriptSlack = 2 * time.Second

// capture writes every artifact for one run. Failures are collected rather than
// short-circuited, so a broken git or a missing transcript still leaves the
// rest of the evidence on disk, and the reason lands in the run record.
//
// The top-level artifacts are the FINAL session's; a multi-step run's earlier
// sessions land under steps/step-NN/ (their diff was taken by the runner
// before the next session's boundary destroyed that state, and their
// transcript is matched by each session's own id and start time).
func capture(ctx context.Context, a *arm, dir string, out runOutcome) error {
	var errs error

	diff, err := gitDiff(ctx, a.repo, a.snapshot)
	if err != nil {
		errs = errors.Join(errs, err)
	} else if err := os.WriteFile(filepath.Join(dir, bench.DiffFile), []byte(diff), 0o644); err != nil {
		errs = errors.Join(errs, fmt.Errorf("write %s: %w", bench.DiffFile, err))
	}

	if err := dumpEvents(ctx, a, filepath.Join(dir, bench.EventsFile)); err != nil {
		errs = errors.Join(errs, err)
	}

	if err := captureTranscript(a, dir, out.sessionID, out.startedAt, true); err != nil {
		errs = errors.Join(errs, err)
	}
	for i, so := range out.steps {
		if i == len(out.steps)-1 {
			break // the final session's artifacts are the top-level ones
		}
		stepDir := filepath.Join(dir, bench.StepDirName(i+1))
		if err := os.MkdirAll(stepDir, 0o755); err != nil {
			errs = errors.Join(errs, fmt.Errorf("create %s: %w", stepDir, err))
			continue
		}
		if so.repoDiff != "" {
			if err := os.WriteFile(filepath.Join(stepDir, bench.DiffFile), []byte(so.repoDiff), 0o644); err != nil {
				errs = errors.Join(errs, fmt.Errorf("write step %d %s: %w", i+1, bench.DiffFile, err))
			}
		}
		if err := captureTranscript(a, stepDir, so.sessionID, so.startedAt, false); err != nil {
			errs = errors.Join(errs, err)
		}
	}

	// The demo-repo copy is the working tree only: .git holds the fixture's own
	// scaffolding (including the scaffold marker that must never be visible to
	// an agent) and the diff already carries the history the grader needs.
	if err := copyTree(a.repo, filepath.Join(dir, bench.RepoDirName), func(rel string) bool {
		return rel == ".git"
	}); err != nil {
		errs = errors.Join(errs, err)
	}
	// The data dir is copied WHOLE -- in particular seam.db-wal and seam.db-shm
	// ride along with seam.db. The daemon runs in WAL mode, so the tail of the
	// event log (the session_end finding, the last tool calls, task
	// transitions) can still be sitting in the WAL; a copy of seam.db alone
	// would silently drop exactly the evidence that the mechanism fired.
	// Nothing here assumes stopping the daemon checkpointed anything.
	if a.seamless {
		if err := copyTree(a.dataDir, filepath.Join(dir, bench.DataDirName), nil); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// dumpEvents writes the arm's whole event log as a JSON array of core.Event,
// oldest first. A vanilla arm gets an empty array rather than no file: "this
// arm recorded nothing because it has no Seamless" is a fact the grader should
// read from the artifact, not infer from a missing one.
func dumpEvents(ctx context.Context, a *arm, path string) error {
	evs := []core.Event{}
	if a.seamless {
		db, err := store.Open(filepath.Join(a.dataDir, "seam.db"))
		if err != nil {
			return fmt.Errorf("open arm db for the event dump: %w", err)
		}
		defer func() { _ = db.Close() }()

		// No exclusions: the transport-level kinds (tool.call, hook.prompt) are
		// exactly the grader's mechanism signal.
		got, err := events.NewRecorder(db).RecentExcluding(ctx, maxDumpedEvents)
		if err != nil {
			return fmt.Errorf("read arm event log: %w", err)
		}
		slices.Reverse(got) // RecentExcluding is newest-first; a run log reads forwards
		if len(got) > 0 {
			evs = got
		}
	}
	b, err := json.MarshalIndent(evs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal event dump: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", bench.EventsFile, err)
	}
	return nil
}

// captureTranscript copies one session's transcript into its artifact dir. A
// session that never started (a run aborted before it, or the recall component
// check) has nothing to search for; warn is set only for the top-level call so
// a legitimately transcript-less earlier step does not double-log.
func captureTranscript(a *arm, dir, sessionID string, since time.Time, warn bool) error {
	if sessionID == "" && since.IsZero() {
		return nil
	}
	switch path, err := findTranscript(a.configDir, sessionID, since); {
	case err != nil:
		return err
	case path == "":
		// Not an error: a crashed agent, and any run of a client that flushes
		// its transcript only on a clean exit, legitimately leaves none.
		// LoadRun reports the artifact as absent.
		if warn {
			slog.Warn("no agent transcript found for this run",
				"condition", a.condition.Name, "configDir", a.configDir)
		}
		return nil
	default:
		return copyFile(path, filepath.Join(dir, bench.TranscriptFile))
	}
}

// findTranscript locates one agent session's transcript under the arm's config
// home. The agent names the file after its own session id, so that is the
// primary match; otherwise the newest jsonl written since the session started
// wins, which is what keeps a previous run's transcript in the same (re-used)
// arm -- or an earlier step's in the same run -- from being captured as this
// one's. A crashed session has no result JSON and so no session id; its
// fallback cannot mismatch a later step's file, because a crashed step aborts
// the run. "" means none was produced.
func findTranscript(configDir, sessionID string, since time.Time) (string, error) {
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", "*.jsonl"))
	if err != nil {
		return "", fmt.Errorf("scan transcripts under %s: %w", configDir, err)
	}
	cutoff := since.Add(-transcriptSlack)
	var newest string
	var newestAt time.Time
	for _, m := range matches {
		info, err := os.Lstat(m)
		if err != nil || !info.Mode().IsRegular() {
			continue // vanished, or a symlink out of the tree
		}
		if sessionID != "" && strings.TrimSuffix(filepath.Base(m), ".jsonl") == sessionID {
			return m, nil
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if newest == "" || info.ModTime().After(newestAt) {
			newest, newestAt = m, info.ModTime()
		}
	}
	return newest, nil
}

// gitSnapshot records the demo repo's HEAD, the commit every run of that arm is
// reset to and diffed against.
func gitSnapshot(ctx context.Context, repo string) (string, error) {
	out, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("demo repo %s has no HEAD commit", repo)
	}
	return sha, nil
}

// gitRestore puts the demo repo back to its snapshot so run N+1 starts from the
// same state run N did -- tracked changes reverted, and everything the agent
// created (ignored files included) removed.
func gitRestore(ctx context.Context, repo, sha string) error {
	if _, err := git(ctx, repo, "reset", "--hard", sha); err != nil {
		return err
	}
	if _, err := git(ctx, repo, "clean", "-fdx"); err != nil {
		return err
	}
	return nil
}

// gitDiff renders what the agent did to the demo repo, against the snapshot.
// Untracked files are staged intent-to-add first so files the agent CREATED
// appear in the diff instead of vanishing from it -- that touches .git/index
// only, never the working tree, so no benchmark scaffolding becomes visible to
// an agent (memory scene-demo-repo-must-be-seamless-free).
func gitDiff(ctx context.Context, repo, sha string) (string, error) {
	if _, err := git(ctx, repo, "add", "-A", "-N"); err != nil {
		return "", err
	}
	return git(ctx, repo, "diff", "--no-color", sha)
}

// gitUnstage drops the index entries gitDiff's intent-to-add staged, so a
// later agent session in the same working tree does not see phantom staged
// files. The working tree itself is untouched.
func gitUnstage(ctx context.Context, repo string) error {
	_, err := git(ctx, repo, "reset", "-q")
	return err
}

// git runs one git command in dir and returns its stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, msg)
		}
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return stdout.String(), nil
}

// copyTree copies src to dst, skipping any relative path skip reports and never
// following symlinks (an arm's tree is agent-writable, so a link out of it must
// not become a copy of whatever it pointed at).
func copyTree(src, dst string, skip func(rel string) bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", p, err)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", p, err)
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			return copyFile(p, target)
		default:
			return nil // sockets, fifos, devices: nothing a run artifact needs
		}
	})
}

// copyFile copies one regular file, creating the destination directory.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
