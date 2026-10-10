//go:build windows

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The fallback's flags, the error it retries on, and the job limit the
// self-check refuses are mirrored as plain constants so their tests run on any
// OS; these are the values Windows defines.
func TestWindowsConstantsMatchTheOS(t *testing.T) {
	require.Equal(t, uint32(windows.CREATE_NEW_PROCESS_GROUP), uint32(createNewProcessGroup))
	require.Equal(t, uint32(windows.CREATE_BREAKAWAY_FROM_JOB), uint32(createBreakawayFromJob))
	require.Equal(t, uint32(windows.CREATE_NO_WINDOW), uint32(createNoWindow))
	require.Equal(t, windows.ERROR_ACCESS_DENIED, errAccessDenied)
	require.Equal(t, uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE), uint32(jobObjectLimitKillOnJobClose))
}

// TestTaskArguments splits the update task's argument string back up the way
// Windows hands it to the updater, so every argument -- a config path with a
// space, a quote or a trailing backslash in it -- arrives whole.
func TestTaskArguments(t *testing.T) {
	req := spawnReq(t)
	for _, configPath := range []string{
		`C:\Users\me\.config\seamless\seamless.yaml`,
		`C:\Users\Jane Doe\.config\seamless\seamless.yaml`,
		`C:\Users\a"b\.config\seamless\seamless.yaml`,
		`C:\odd dir\`,
	} {
		args := updaterArgs(req, configPath)
		got, err := windows.DecomposeCommandLine(`C:\seamlessd.exe ` + taskArguments(args))
		require.NoError(t, err)
		require.Equal(t, args, got[1:], configPath)
	}
}
