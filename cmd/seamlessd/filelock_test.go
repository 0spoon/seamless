package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fileLockHelperEnv hands TestFileLockHelperProcess the lock path to hold.
const fileLockHelperEnv = "SEAMLESSD_TEST_FILELOCK_HELPER"

// Two handles in one process stand in for two processes here: flock is per open
// file description and LockFileEx per handle, so a second open of the same path
// is refused exactly as another process would be.
func TestTryLockFile_ExcludesAnotherHandleUntilClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	// A previous holder's leftovers: the new holder's PID replaces them whole.
	require.NoError(t, os.WriteFile(path, []byte("999999999\nleftover from a crash\n"), 0o600))

	first, err := tryLockFile(path)
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(os.Getpid())+"\n", string(content))

	_, err = tryLockFile(path)
	require.ErrorIs(t, err, errLockHeld)
	require.ErrorContains(t, err, path, "the refusal names the lock")
	require.Equal(t, "pid "+strconv.Itoa(os.Getpid()), lockHolder(path),
		"a refused caller can name the holder")

	require.NoError(t, first.Close())
	require.NoError(t, first.Close(), "closing a released lock is a no-op")
	require.FileExists(t, path, "the lock file is never deleted")

	second, err := tryLockFile(path)
	require.NoError(t, err, "a released lock is free to take")
	require.NoError(t, second.Close())
}

func TestLockHolderPID_UnknownWithoutAPID(t *testing.T) {
	dir := t.TempDir()
	_, ok := lockHolderPID(filepath.Join(dir, "absent.lock"))
	require.False(t, ok)

	tests := []struct {
		name    string
		content string
		wantPID int
	}{
		{"pid with newline", "4242\n", 4242},
		{"pid padded", "  4242  ", 4242},
		{"empty: the instant before a new holder writes", "", 0},
		{"not a number", "seamlessd", 0},
		{"zero", "0\n", 0},
		{"negative", "-7\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "x.lock")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			pid, ok := lockHolderPID(path)
			require.Equal(t, tt.wantPID != 0, ok)
			require.Equal(t, tt.wantPID, pid)
			if tt.wantPID == 0 {
				require.Equal(t, "another process", lockHolder(path))
			}
		})
	}
}

// The lock exists to exclude another PROCESS, and to vanish when that process
// dies however it dies -- which is why nothing ever has to delete the lock file.
// A re-executed test binary plays the other process: it takes the lock, reports
// its PID, and is then killed without any chance to release it.
func TestTryLockFile_AnotherProcessHoldsItUntilItDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), dataDirLockName)
	exe, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.Command(exe, "-test.run=^TestFileLockHelperProcess$")
	cmd.Env = append(os.Environ(), fileLockHelperEnv+"="+path)
	cmd.Stderr = os.Stderr
	// The helper holds the lock until killed or until its stdin closes, so it can
	// never outlive this test even if the test dies before the kill below.
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	want := fmt.Sprintf("locked %d", cmd.Process.Pid)
	scanner := bufio.NewScanner(stdout)
	reported := false
	for !reported && scanner.Scan() {
		reported = scanner.Text() == want
	}
	require.True(t, reported, "the helper never reported holding the lock (scan error: %v)", scanner.Err())

	holder := "pid " + strconv.Itoa(cmd.Process.Pid)
	_, err = tryLockFile(path)
	require.ErrorIs(t, err, errLockHeld, "another process's lock refuses this one")
	require.Equal(t, holder, lockHolder(path))

	// Waiting on a holder that never lets go gives up in bounded time, naming
	// the lock and the holder.
	_, err = waitLockFile(context.Background(), path, 50*time.Millisecond, 10*time.Millisecond, nil)
	require.ErrorIs(t, err, errLockHeld)
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, holder)

	// Killed, not exited: no unlock, no close, nothing deleted.
	require.NoError(t, cmd.Process.Kill())
	var exitErr *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &exitErr)
	require.FileExists(t, path)

	// The OS released the dead holder's lock. Windows does that "when system
	// resources permit", so poll rather than expect it on the first try.
	var l *fileLock
	require.Eventually(t, func() bool {
		var lerr error
		l, lerr = tryLockFile(path)
		return lerr == nil
	}, 10*time.Second, 10*time.Millisecond, "a dead holder's lock must come free")
	require.NoError(t, l.Close())
}

// TestFileLockHelperProcess is not a test of its own: the test above re-executes
// the test binary into it to play a second process. It holds the lock named by
// fileLockHelperEnv until it is killed or its stdin closes.
func TestFileLockHelperProcess(t *testing.T) {
	path := os.Getenv(fileLockHelperEnv)
	if path == "" {
		t.Skip("helper process for TestTryLockFile_AnotherProcessHoldsItUntilItDies")
	}
	l, err := tryLockFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "filelock helper:", err)
		os.Exit(2)
	}
	fmt.Printf("locked %d\n", os.Getpid())
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = l.Close()
	os.Exit(0)
}

// A restart overlaps the old daemon's exit: the new one must wait for the lock
// instead of failing, say once what it is waiting for, and take the lock as soon
// as it is released.
func TestWaitLockFile_TakesTheLockOnceTheHolderLetsGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), dataDirLockName)
	holder, err := tryLockFile(path)
	require.NoError(t, err)

	var calls atomic.Int32
	waiting := make(chan string, 1)
	type result struct {
		l   *fileLock
		err error
	}
	done := make(chan result, 1)
	go func() {
		l, err := waitLockFile(context.Background(), path, 10*time.Second, 5*time.Millisecond,
			func(lock, holder string) {
				if calls.Add(1) == 1 {
					waiting <- lock + " | " + holder
				}
			})
		done <- result{l, err}
	}()

	select {
	case got := <-waiting:
		require.Equal(t, path+" | pid "+strconv.Itoa(os.Getpid()), got)
	case <-time.After(5 * time.Second):
		t.Fatal("waitLockFile never reported the held lock")
	}
	require.NoError(t, holder.Close())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.NoError(t, r.l.Close())
	case <-time.After(5 * time.Second):
		t.Fatal("waitLockFile did not take the released lock")
	}
	require.Equal(t, int32(1), calls.Load(), "the wait is announced once, not once per poll")
}

func TestWaitLockFile_GivesUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), dataDirLockName)
	holder, err := tryLockFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, holder.Close()) })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		wait    time.Duration
		wantErr error
		want    []string
	}{
		{
			name: "deadline names the lock, the holder and the wait", ctx: context.Background(),
			wait: 30 * time.Millisecond, wantErr: errLockHeld,
			want: []string{path, "pid " + strconv.Itoa(os.Getpid()), "30ms"},
		},
		{
			name: "a canceled context stops the wait", ctx: canceled,
			wait: time.Minute, wantErr: context.Canceled, want: []string{path},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			l, err := waitLockFile(tt.ctx, path, tt.wait, 5*time.Millisecond, nil)
			require.Nil(t, l)
			require.ErrorIs(t, err, tt.wantErr)
			for _, s := range tt.want {
				require.ErrorContains(t, err, s)
			}
			require.Less(t, time.Since(start), 10*time.Second)
		})
	}
}

// serve's lock lives in the data dir, which may not exist yet on a first start:
// lockDataDir creates it owner-only, as store.Open would, before locking.
func TestLockDataDir_CreatesTheDataDirAndHoldsItsLock(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")
	l, err := lockDataDir(context.Background(), dataDir, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, l.Close()) })

	lockPath := filepath.Join(dataDir, dataDirLockName)
	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		lockInfo, err := os.Stat(lockPath)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), lockInfo.Mode().Perm())
	}

	_, err = tryLockFile(lockPath)
	require.ErrorIs(t, err, errLockHeld, "a second serve on this data dir is refused")
}
