package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// fileLock is an exclusive lock held through one open handle on a lock file:
// flock(2) on Unix (filelock_unix.go), LockFileEx on Windows
// (filelock_windows.go). It is the primitive behind serve's one daemon per data
// dir, shaped for any other single-instance guard a subcommand needs:
// tryLockFile never blocks, and a caller that wants to wait polls it through
// waitLockFile.
//
// The OS drops the lock when the handle closes, and a process that dies in any
// way at all closes every handle it had, so a crashed holder never strands the
// lock and there is no staleness to judge. That is also why the lock file is
// never deleted, by Close or by anyone else: a process that opened the path
// before an unlink would go on locking the orphaned inode while a newcomer locks
// a fresh file at the same path, and each would believe it is alone.
type fileLock struct {
	path string
	f    *os.File
}

// errLockHeld reports that another handle holds the lock. The lock belongs to an
// open file (flock: the open file description; LockFileEx: the handle), not to a
// process, so even a second handle in the same process is refused; in practice
// the holder is another process.
var errLockHeld = errors.New("lock held by another process")

// errLockUnsupported reports a file system that refuses the lock call itself (an
// NFS mount without a lock manager, some network and FUSE file systems): the
// lock can be neither taken nor ruled out, which is not the same answer as
// errLockHeld.
var errLockUnsupported = errors.New("file locking is not supported here")

// tryLockFile opens path, creating it 0600 when absent, and takes the exclusive
// lock without waiting. It returns an error wrapping errLockHeld when another
// handle holds it, and one wrapping errLockUnsupported when the file system
// cannot lock at all. On success it records this process's PID as the file's
// whole content, so a process it refuses can name the holder (lockHolderPID).
func tryLockFile(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	l := &fileLock{path: path, f: f}
	if err := l.recordHolder(); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// recordHolder replaces the lock file's content with this process's PID. Only
// the holder writes, and only while it holds the lock, so the content belongs to
// whoever holds it -- except for the instant between a new holder's lock and
// this write, when it still names the previous one (see lockHolderPID).
func (l *fileLock) recordHolder() error {
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("record lock holder in %s: %w", l.path, err)
	}
	if _, err := l.f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return fmt.Errorf("record lock holder in %s: %w", l.path, err)
	}
	return nil
}

// Close releases the lock and closes its handle; the file stays (see fileLock).
// The explicit unlock matters on Windows, where a lock left to the handle close
// is released "when system resources permit" rather than at once. Closing an
// already closed lock is a no-op.
func (l *fileLock) Close() error {
	if l.f == nil {
		return nil
	}
	unlockErr := unlockFile(l.f)
	closeErr := l.f.Close()
	l.f = nil
	return errors.Join(unlockErr, closeErr)
}

// lockHolderPID reads the PID the lock's holder recorded at path. ok is false
// when the file is unreadable or carries no PID. It is a description for a
// message, never a liveness check: the lock is the only authority on whether the
// holder is alive, and the PID can briefly name the previous holder (see
// recordHolder).
func lockHolderPID(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// lockHolder describes the holder of the lock at path for a message: "pid N",
// or "another process" when no PID is readable.
func lockHolder(path string) string {
	if pid, ok := lockHolderPID(path); ok {
		return "pid " + strconv.Itoa(pid)
	}
	return "another process"
}

// waitLockFile takes the lock at path like tryLockFile but, while another
// process holds it, retries every poll until wait has passed or ctx ends.
// onWait, when non-nil, is called once with the path and the holder's
// description (lockHolder) the first time the lock is found held, so a caller
// can say why it is not running yet. Giving up returns an error wrapping
// errLockHeld that names the path and the holder; any other failure is returned
// at once.
func waitLockFile(ctx context.Context, path string, wait, poll time.Duration, onWait func(path, holder string)) (*fileLock, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	announced := false
	for {
		l, err := tryLockFile(path)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, errLockHeld) {
			return nil, err
		}
		if !announced && onWait != nil {
			onWait(path, lockHolder(path))
		}
		announced = true

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for %s: %w", path, ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("%s: %w (%s) for longer than %s", path, errLockHeld, lockHolder(path), wait)
		case <-ticker.C:
		}
	}
}

// dataDirLockName is the lock file serve holds inside its data dir for as long
// as it runs.
const dataDirLockName = "seamlessd.lock"

// dataDirLockWait bounds how long serve waits for a previous daemon to let go of
// the data dir. Restarts can overlap -- on Windows, `seamlessd restart`
// (`schtasks /End` then `/Run`) and install.ps1's Stop-Process return before the
// old process is gone -- and an old daemon in a graceful shutdown keeps the lock
// through runServe's 10s drain and the closes after it. 30s outlasts both and is
// still bounded, so a second daemon started by mistake fails naming the holder
// instead of hanging.
const dataDirLockWait = 30 * time.Second

// dataDirLockPoll is how often serve retries a held data dir lock.
const dataDirLockPoll = 100 * time.Millisecond

// lockDataDir takes serve's one-daemon-per-data-dir lock, <dataDir>/seamlessd.lock,
// creating the data dir first with the owner-only 0700 store.Open would give it.
// It waits up to dataDirLockWait for a previous holder (see waitLockFile for
// onWait), then fails naming the lock and its holder.
func lockDataDir(ctx context.Context, dataDir string, onWait func(path, holder string)) (*fileLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	l, err := waitLockFile(ctx, filepath.Join(dataDir, dataDirLockName), dataDirLockWait, dataDirLockPoll, onWait)
	if errors.Is(err, errLockHeld) {
		return nil, fmt.Errorf("data dir %s is in use by another seamlessd; stop it, or give this one its own data_dir: %w", dataDir, err)
	}
	return l, err
}
