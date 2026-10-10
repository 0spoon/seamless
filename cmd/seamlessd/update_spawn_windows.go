//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/arctop/seamless/internal/update"
)

// handoffProcAttr gives a hand-off command (schtasks) no console window: the
// daemon runs in the background, and a window flashing up on the owner's
// screen is worse than none.
func handoffProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

// osUpdateSpawner is Windows' spawner: the per-user update task, else a
// detached child.
func osUpdateSpawner(t spawnTarget, logger *slog.Logger) update.Spawner {
	return taskSpawner{target: t, log: logger}
}

// taskSpawner starts the updater from the per-user on-demand task
// SeamlessUpdate-<SID>, registered afresh for each attempt, and falls back to
// a detached child of the daemon. Task Scheduler's stop -- Stop-ScheduledTask,
// schtasks /End -- ends only the Seamless task's own process, `serve`, and
// both paths survived it (2.01, Windows 11, S4U stand-in): the task's updater
// is no descendant of the daemon at all, and the child leaves the daemon's
// job when it may and outlives it when it may not.
type taskSpawner struct {
	target spawnTarget
	log    *slog.Logger
}

// Spawn starts req's updater. nil means schtasks /Run accepted the task or
// the child process exists.
func (s taskSpawner) Spawn(ctx context.Context, req update.SpawnRequest) error {
	if err := req.Validate(); err != nil {
		return fmt.Errorf("seamlessd.spawn: %w", err)
	}
	args := s.target.args(req)
	taskErr := s.runTask(ctx, args)
	if taskErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		// Shutting down mid hand-off: the task may still start, and a child
		// on top of it would be a second updater.
		return fmt.Errorf("seamlessd.spawn: the update task: %w", taskErr)
	}
	if err := startDetachedChild(s.target.exe, args); err != nil {
		return fmt.Errorf("seamlessd.spawn: the update task: %w; a detached process: %w", taskErr, err)
	}
	s.log.Warn("update: the update task did not start the updater, so it runs as a detached process", "err", taskErr)
	return nil
}

// runTask registers the update task from a definition written to the update
// state dir -- never logs/, which holds only attempt logs -- removes the file,
// and runs the task.
func (s taskSpawner) runTask(ctx context.Context, args []string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	plan, err := newUpdateTaskPlan(sid, s.target.exe, taskArguments(args))
	if err != nil {
		return err
	}
	path, err := writeTaskDefinition(s.target.dataDir, plan.definition)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(path) //nolint:errcheck // a leftover definition is inert, and the next attempt writes its own
	}()
	for _, argv := range [][]string{plan.create(path), plan.run()} {
		if out, err := runHandoff(ctx, handoffTimeout, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("%s %s: %w", argv[0], argv[1], withFirstLine(err, out))
		}
	}
	return nil
}

// writeTaskDefinition writes a task definition into the update state dir and
// returns its path.
func writeTaskDefinition(dataDir string, def []byte) (string, error) {
	dir := update.StateDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "update-task-*.xml")
	if err != nil {
		return "", fmt.Errorf("write the update task definition: %w", err)
	}
	_, werr := f.Write(def)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name()) //nolint:errcheck // the write already failed; a leftover partial file is inert
		return "", fmt.Errorf("write the update task definition: %w", werr)
	}
	return f.Name(), nil
}

// taskArguments joins args into the one argument string a task action
// carries, each quoted the way Windows splits a command line back up
// (syscall.EscapeArg): a config path with a space in it stays one argument.
func taskArguments(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	return strings.Join(quoted, " ")
}

// startDetachedChild starts exe with args as the fallback: no console window,
// a process group of its own, out of the daemon's job (detachedChildFlags),
// once more without leaving the job when the job refuses (withoutBreakaway).
// Its stdio is NUL, never a pipe the daemon would have to drain; the daemon
// reaps it.
func startDetachedChild(exe string, args []string) error {
	flags := uint32(detachedChildFlags)
	for {
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		err := cmd.Start()
		if err == nil {
			go func() {
				_ = cmd.Wait() //nolint:errcheck // the updater reports through its attempt record; this only reaps it
			}()
			return nil
		}
		next, retry := withoutBreakaway(flags, err)
		if !retry {
			return err
		}
		flags = next
	}
}

// ownUpdateTask names this user's update task, or "" when the user's SID
// cannot be read (uninstall then leaves the task alone, best-effort like the
// rest of its teardown).
func ownUpdateTask() string {
	sid, err := currentUserSID()
	if err != nil {
		return ""
	}
	return updateTaskName(sid)
}
