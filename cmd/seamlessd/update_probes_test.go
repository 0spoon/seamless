package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/update"
)

// The probe answers from the OS lock alone: free when no handle holds it, held
// while another does (a second handle in this process stands in for the
// updater, as in filelock_test.go), and free again -- released by the probe
// itself -- for whoever comes next.
func TestUpdaterLockHeld_TracksTheUpdaterLock(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(update.StateDir(dataDir), 0o700))
	path := update.LockPath(dataDir)
	require.NoError(t, os.WriteFile(path, []byte("4242\n"), 0o600))

	held, err := updaterLockHeld(dataDir)
	require.NoError(t, err)
	require.False(t, held, "nobody holds it")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "4242\n", string(content), "the probe never writes the holder's file")

	updater, err := tryLockFile(path)
	require.NoError(t, err, "the probe let go of the lock it took")
	held, err = updaterLockHeld(dataDir)
	require.NoError(t, err)
	require.True(t, held, "an updater holds it")
	require.NoError(t, updater.Close())

	held, err = updaterLockHeld(dataDir)
	require.NoError(t, err)
	require.False(t, held, "the updater let go")
	again, err := tryLockFile(path)
	require.NoError(t, err, "and so did the probe")
	require.NoError(t, again.Close())
}

// A daemon that never spawned an updater has no lock file, and the probe
// creates none, nor the update directory -- which a source build must never
// have (constraint dev-and-fixture-daemons-never-self-update).
func TestUpdaterLockHeld_CreatesNothing(t *testing.T) {
	dataDir := t.TempDir()
	held, err := updaterLockHeld(dataDir)
	require.NoError(t, err)
	require.False(t, held)
	require.NoDirExists(t, update.StateDir(dataDir))

	require.NoError(t, os.MkdirAll(update.StateDir(dataDir), 0o700))
	held, err = updaterLockHeld(dataDir)
	require.NoError(t, err)
	require.False(t, held)
	require.NoFileExists(t, update.LockPath(dataDir))
}

// A lock file the probe cannot open is an error, not a guess either way.
func TestUpdaterLockHeld_UnopenableIsAnError(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(update.LockPath(dataDir), 0o700), "a directory where the lock file belongs")
	held, err := updaterLockHeld(dataDir)
	require.Error(t, err)
	require.False(t, held)
}

// sha256Fingerprint is the fingerprint the format promises for content.
func sha256Fingerprint(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The fingerprint is the fixed absent token, else the SHA-256 of the file's
// bytes: equal content, equal fingerprint; any change, a different one. The
// format is pinned literally because it outlives a release (it is compared
// with what an earlier daemon saved).
func TestCodexHooksFingerprint_Format(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "hooks.json")

	got, err := codexHooksFingerprint()
	require.NoError(t, err)
	require.Equal(t, "absent", got, "no hooks.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{}}`), 0o600))
	first, err := codexHooksFingerprint()
	require.NoError(t, err)
	require.Equal(t, sha256Fingerprint(`{"hooks":{}}`), first)

	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{}}`), 0o600))
	same, err := codexHooksFingerprint()
	require.NoError(t, err)
	require.Equal(t, first, same, "rewritten with the same bytes")

	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[]}}`), 0o600))
	changed, err := codexHooksFingerprint()
	require.NoError(t, err)
	require.NotEqual(t, first, changed)

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	got, err = fileFingerprint(empty)
	require.NoError(t, err)
	require.Equal(t, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", got,
		"an empty file is the SHA-256 of nothing, never the absent token")
}

// Without CODEX_HOME the file is ~/.codex/hooks.json in the daemon's own
// environment; a blank CODEX_HOME counts as unset, as install-hooks reads it.
func TestCodexHooksFingerprint_HomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("CODEX_HOME", "  ")

	got, err := codexHooksFingerprint()
	require.NoError(t, err)
	require.Equal(t, "absent", got)

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "hooks.json"), []byte("x"), 0o600))
	got, err = codexHooksFingerprint()
	require.NoError(t, err)
	require.Equal(t, sha256Fingerprint("x"), got)
}

// A hooks.json that exists but cannot be read is unknown -- an error -- never
// the absent token, which would read as a change once it is readable again.
func TestCodexHooksFingerprint_UnreadableIsAnError(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "hooks.json")

	require.NoError(t, os.Mkdir(path, 0o700), "a directory in the file's place")
	got, err := codexHooksFingerprint()
	require.Error(t, err)
	require.Empty(t, got)
	require.NoError(t, os.Remove(path))

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // no mode bits to deny a read with, or a user they do not bind
	}
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	got, err = codexHooksFingerprint()
	require.Error(t, err)
	require.Empty(t, got)
}
