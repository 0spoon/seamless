//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// spawnHelperEnv carries the report path to TestSpawnHelperProcess, which
// runs only in a process TestStartDetached starts.
const spawnHelperEnv = "SEAMLESS_TEST_SPAWN_REPORT"

// spawnReport is what the helper process says about itself.
type spawnReport struct {
	PID, PGID, SID int
	// Outside is whether outsideServiceCheck's darwin rule lets it run, and
	// Verdict its error text when not.
	Outside bool
	Verdict string
}

// TestStartDetached_LeadsItsOwnSessionAndGroup re-executes this test binary as
// a helper twice: through startDetached, the darwin spawner's start, and as a
// plain child. The Setsid child must lead a session and a process group of its
// own -- which is what puts it out of reach of launchd's group SIGTERM -- and
// pass the darwin self-check; the plain child, in the parent's group, must
// fail it.
func TestStartDetached_LeadsItsOwnSessionAndGroup(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	helper := []string{"-test.run=^TestSpawnHelperProcess$"}

	detached := filepath.Join(dir, "detached.json")
	t.Setenv(spawnHelperEnv, detached)
	require.NoError(t, startDetached(exe, helper))
	var got spawnReport
	require.Eventually(t, func() bool { return readSpawnReport(detached, &got) }, 20*time.Second, 10*time.Millisecond,
		"the detached helper never reported")
	require.Equal(t, got.PID, got.PGID, "a process group of its own")
	require.Equal(t, got.PID, got.SID, "a session of its own")
	require.True(t, got.Outside, got.Verdict)

	plain := filepath.Join(dir, "plain.json")
	t.Setenv(spawnHelperEnv, plain)
	require.NoError(t, exec.Command(exe, helper...).Run())
	require.True(t, readSpawnReport(plain, &got), "the plain helper never reported")
	require.NotEqual(t, got.PID, got.PGID, "a plain child stays in its parent's group")
	require.NotEqual(t, got.PID, got.SID)
	require.False(t, got.Outside)
	require.Contains(t, got.Verdict, errInsideService.Error())
}

// readSpawnReport reads a helper's report once it exists.
func readSpawnReport(path string, r *spawnReport) bool {
	raw, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(raw, r) == nil
}

// TestSpawnHelperProcess is the helper TestStartDetached starts. It reports
// its pid, process group and session, and the darwin self-check's verdict on
// itself, then exits.
func TestSpawnHelperProcess(t *testing.T) {
	path := os.Getenv(spawnHelperEnv)
	if path == "" {
		t.Skip("a helper process: it runs only when TestStartDetached starts it")
	}
	pgid, err := unix.Getpgid(0)
	require.NoError(t, err)
	sid, err := unix.Getsid(0)
	require.NoError(t, err)
	r := spawnReport{PID: os.Getpid(), PGID: pgid, SID: sid}
	if verdict := checkOutsideService("darwin", nil, ownProcess); verdict != nil {
		r.Verdict = verdict.Error()
	} else {
		r.Outside = true
	}
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	// Written whole, then renamed into place, so a reader never sees half.
	require.NoError(t, os.WriteFile(path+".tmp", raw, 0o600))
	require.NoError(t, os.Rename(path+".tmp", path))
}

func TestRunHandoff(t *testing.T) {
	ctx := context.Background()

	t.Run("combined output on success", func(t *testing.T) {
		out, err := runHandoff(ctx, handoffTimeout, "/bin/sh", "-c", "echo 'Running as unit: seamless-update-x.service'; echo more >&2")
		require.NoError(t, err)
		require.Equal(t, "Running as unit: seamless-update-x.service\nmore\n", string(out))
	})

	t.Run("a failure keeps its output", func(t *testing.T) {
		out, err := runHandoff(ctx, handoffTimeout, "/bin/sh", "-c", "echo 'Failed to connect to bus: No medium found' >&2; exit 1")
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, 1, exit.ExitCode())
		require.Equal(t, "Failed to connect to bus: No medium found\n", string(out))
	})

	t.Run("a missing command", func(t *testing.T) {
		_, err := runHandoff(ctx, handoffTimeout, "seamless-no-such-command")
		require.ErrorIs(t, err, exec.ErrNotFound)
	})

	t.Run("the timeout kills it", func(t *testing.T) {
		start := time.Now()
		_, err := runHandoff(ctx, 200*time.Millisecond, "/bin/sh", "-c", "exec sleep 30")
		require.ErrorIs(t, err, errHandoffTimeout)
		require.Less(t, time.Since(start), 10*time.Second)
	})

	t.Run("a cancelled ctx returns at once and leaves the command running", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "finished")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := runHandoff(cancelled, handoffTimeout, "/bin/sh", "-c", "sleep 0.3; echo done > \"$0\"", marker)
		require.ErrorIs(t, err, context.Canceled)
		require.Eventually(t, func() bool {
			_, err := os.Stat(marker)
			return err == nil
		}, 10*time.Second, 10*time.Millisecond, "the daemon's ctx must not kill a hand-off in flight")
	})
}
