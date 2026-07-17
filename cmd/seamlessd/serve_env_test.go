package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// applyServeEnv is what lets a Windows Scheduled Task -- which cannot carry an
// env prefix the way a launchd plist or systemd unit does -- pin the config and
// reach a log file. These pin both halves.

func TestApplyServeEnv_ConfigExportsEnv(t *testing.T) {
	t.Setenv("SEAMLESS_CONFIG", "") // isolate; t.Setenv restores after the test
	want := filepath.Join(t.TempDir(), "seamless.yaml")

	cleanup, err := applyServeEnv(want, "")
	require.NoError(t, err)
	defer cleanup()

	require.Equal(t, want, os.Getenv("SEAMLESS_CONFIG"),
		"--config must export $SEAMLESS_CONFIG so config.Load's search order stays the one code path")
}

func TestApplyServeEnv_EmptyConfigLeavesEnvUntouched(t *testing.T) {
	t.Setenv("SEAMLESS_CONFIG", "/pre/existing.yaml")

	cleanup, err := applyServeEnv("", "")
	require.NoError(t, err)
	defer cleanup()

	require.Equal(t, "/pre/existing.yaml", os.Getenv("SEAMLESS_CONFIG"),
		"an empty --config must not clobber an inherited $SEAMLESS_CONFIG")
}

func TestApplyServeEnv_LogFileReceivesLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "seamlessd.log")

	cleanup, err := applyServeEnv("", logPath)
	require.NoError(t, err)

	slog.Info("hello from serve", "marker", "windows-log-file")
	cleanup() // close the file before reading it back

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "windows-log-file",
		"--log-file must tee the daemon's logs to the path the Scheduled Task points at")

	// Restore the default logger so a file handle in a temp dir does not linger
	// as the process-wide slog sink for later tests.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func TestApplyServeEnv_NoLogFileReturnsSafeCleanup(t *testing.T) {
	cleanup, err := applyServeEnv("", "")
	require.NoError(t, err)
	require.NotPanics(t, cleanup, "the no-op cleanup must be safe to call")
}
