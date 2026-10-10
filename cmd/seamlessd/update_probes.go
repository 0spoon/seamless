package main

// The automatic update's two reads of the machine beyond the database and the
// request tracker: whether an updater holds the update lock
// (update.Deps.UpdaterRunning) and a fingerprint of Codex's hooks.json
// (update.Deps.CodexHooksHash). updateCheckerDeps wires both.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/arctop/seamless/internal/update"
)

// updaterLockHeld reports whether an updater holds dataDir's update lock,
// update.LockPath. It try-locks the file through the same OS lock the updater
// takes (lockFile: flock, LockFileEx) and lets go at once; the updater waits
// up to updateLockWait for the lock (update_engine.go), so a probe landing in
// the instant an updater starts never makes it exit.
//
// Unlike tryLockFile, the probe creates and writes nothing. A missing lock
// file is "not held": the updater creates the file before it can lock it, so
// no updater holds a file that does not exist. That keeps a daemon that never
// spawned an updater from leaving <data_dir>/update/update.lock behind, and it
// cannot create <data_dir>/update at all. Nor does it record this process's
// PID in the file, which belongs to whoever holds the lock.
//
// It returns an error, never a guess, for anything else: a lock file it cannot
// open, and a file system that cannot lock (errLockUnsupported), where an
// updater can be neither seen nor ruled out. The checker reads an error as
// "no", leaving the attempt record's heartbeat to decide.
func updaterLockHeld(dataDir string) (bool, error) {
	path := update.LockPath(dataDir)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("open update lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	switch err := lockFile(f); {
	case err == nil:
		if err := unlockFile(f); err != nil {
			return false, fmt.Errorf("release update lock %s: %w", path, err)
		}
		return false, nil
	case errors.Is(err, errLockHeld):
		return true, nil
	default:
		return false, fmt.Errorf("lock %s: %w", path, err)
	}
}

// Codex hooks.json fingerprints. Only daemons compare them, each against what
// an earlier daemon saved in the update state (State.CodexHooks, Spawn
// .CodexHooks), so the format must never change: a release that spelled them
// differently would see every update across that release as a change and
// raise the Codex banner for nothing.
const (
	// codexHooksAbsent is the fingerprint of a hooks.json that does not
	// exist. It is not empty, because the state reads "" as unknown.
	codexHooksAbsent = "absent"
	// codexHooksSHA256 prefixes the hex SHA-256 of an existing file's bytes.
	codexHooksSHA256 = "sha256:"
)

// codexHooksFingerprint fingerprints the Codex hooks.json install-hooks writes
// by default: $CODEX_HOME/hooks.json, else ~/.codex/hooks.json
// (defaultCodexHooksPath, ~ expanded as doctor does). Both are resolved in the
// daemon's own environment, which may differ from the owner's shell; the
// daemon before an update and the one after it run in the same service
// environment, so the two fingerprints describe the same file.
//
// A missing file is codexHooksAbsent. Anything else that stops the read -- no
// home directory to expand ~ against, a file it may not read, a directory in
// the file's place -- is an error: the fingerprint is unknown, and the checker
// keeps the one it last saw.
func codexHooksFingerprint() (string, error) {
	path, err := expandHome(defaultCodexHooksPath())
	if err != nil {
		return "", fmt.Errorf("locate Codex's hooks.json: %w", err)
	}
	return fileFingerprint(path)
}

// fileFingerprint is codexHooksSHA256 plus the hex SHA-256 of path's bytes,
// codexHooksAbsent when path does not exist.
func fileFingerprint(path string) (string, error) {
	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return codexHooksAbsent, nil
	case err != nil:
		return "", fmt.Errorf("fingerprint %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", path, err)
	}
	return codexHooksSHA256 + hex.EncodeToString(h.Sum(nil)), nil
}
