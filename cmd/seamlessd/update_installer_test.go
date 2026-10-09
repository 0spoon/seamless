//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The real runner over the real /bin/sh: the script arrives on stdin, the
// environment is exactly the one given (nothing of the test process leaks in),
// and the exit status comes back.
func TestRunInstallerScript_RunsTheScriptInExactlyItsEnvironment(t *testing.T) {
	t.Setenv("UPDATER_TEST_LEAK", "from the test process")
	dir := t.TempDir()
	script := []byte(`printf 'version=%s\n' "${SEAMLESS_VERSION:-}"
printf 'leak=%s\n' "${UPDATER_TEST_LEAK:-}"
printf 'home=%s\n' "$HOME"
echo to-stderr >&2
exit 3
`)
	var out bytes.Buffer
	err := runInstallerScript(installerRun{
		script: script, out: &out, unattended: true, timeout: 30 * time.Second,
		env: []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "SEAMLESS_VERSION=0.7.3"},
	})
	require.ErrorContains(t, err, "exit status 3")
	got := out.String()
	require.Contains(t, got, "version=0.7.3\n")
	require.Contains(t, got, "leak=\n", "the runner's env is the whole environment")
	require.Contains(t, got, "home="+dir+"\n")
	require.Contains(t, got, "to-stderr")
}

func TestRunInstallerScript_ZeroExitIsNil(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runInstallerScript(installerRun{
		script: []byte("echo ok\n"), out: &out, env: []string{"PATH=/usr/bin:/bin"}, timeout: 30 * time.Second,
	}))
	require.Equal(t, "ok\n", out.String())
}

// A hung installer is stopped at its timeout, with what it started.
func TestRunInstallerScript_StopsAHungInstaller(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-ran")
	var out bytes.Buffer
	start := time.Now()
	err := runInstallerScript(installerRun{
		script:     []byte("touch '" + marker + "'\nsleep 30\n"),
		out:        &out,
		env:        []string{"PATH=/usr/bin:/bin"},
		unattended: true,
		timeout:    300 * time.Millisecond,
	})
	require.ErrorIs(t, err, errInstallerTimeout)
	require.Less(t, time.Since(start), 20*time.Second)
	require.FileExists(t, marker)
}

func TestOpenUpdateLog_OneFilePerAttemptAndPruned(t *testing.T) {
	dataDir := t.TempDir()
	var ids []string
	for range updateLogKeep + 3 {
		id := testAttemptID(t)
		ids = append(ids, id)
		f, path, err := openUpdateLog(dataDir, id)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dataDir, "update", "logs", id+".log"), path)
		require.NoError(t, f.Close())
	}
	require.NoError(t, os.WriteFile(filepath.Join(updateLogDir(dataDir), "notes.txt"), nil, 0o600))
	_, _, err := openUpdateLog(dataDir, testAttemptID(t))
	require.NoError(t, err)

	entries, err := os.ReadDir(updateLogDir(dataDir))
	require.NoError(t, err)
	var logs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	require.Len(t, logs, updateLogKeep)
	require.NotContains(t, logs, ids[0]+".log", "the oldest go")
	require.FileExists(t, filepath.Join(updateLogDir(dataDir), "notes.txt"), "only attempt logs are pruned")
	fi, err := os.Stat(updateLogDir(dataDir))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}
