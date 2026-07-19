package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceTeardown(t *testing.T) {
	home := "/home/tester"
	tests := []struct {
		name         string
		goos         string
		wantLabel    string
		wantStop     [][]string
		wantRemove   []string
		wantReload   [][]string
		wantPathEdit bool
	}{
		{
			name:      "darwin launchd",
			goos:      "darwin",
			wantLabel: "launchd (org.thereisnospoon.seamless)",
			wantStop:  [][]string{{"launchctl", "bootout", "gui/501/org.thereisnospoon.seamless"}},
			wantRemove: []string{
				filepath.Join(home, "Library", "LaunchAgents", "org.thereisnospoon.seamless.plist"),
			},
		},
		{
			name:      "linux systemd --user",
			goos:      "linux",
			wantLabel: "systemd --user (seamless.service)",
			wantStop:  [][]string{{"systemctl", "--user", "disable", "--now", "seamless.service"}},
			wantRemove: []string{
				filepath.Join(home, ".config", "systemd", "user", "seamless.service"),
			},
			wantReload: [][]string{{"systemctl", "--user", "daemon-reload"}},
		},
		{
			name:      "windows scheduled task",
			goos:      "windows",
			wantLabel: "Scheduled Task (Seamless)",
			wantStop: [][]string{
				{"schtasks", "/End", "/TN", "Seamless"},
				{"schtasks", "/Delete", "/TN", "Seamless", "/F"},
			},
			wantPathEdit: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := serviceTeardown(tt.goos, home, 501, "/opt/bin")
			require.Equal(t, tt.wantLabel, p.Label)
			require.Equal(t, tt.wantStop, argvs(p.StopCmds))
			require.Equal(t, tt.wantRemove, p.RemoveFiles)
			require.Equal(t, tt.wantReload, argvs(p.ReloadCmds))
			require.Equal(t, tt.wantPathEdit, len(p.PathEdits) == 1)
		})
	}
}

// argvs flattens a slice of commands to their argv, for assertion. nil in -> nil
// out, so an absent phase compares cleanly against a nil expectation.
func argvs(cmds []*exec.Cmd) [][]string {
	if len(cmds) == 0 {
		return nil
	}
	out := make([][]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Args
	}
	return out
}

func TestWindowsPathRemoveScript(t *testing.T) {
	s := windowsPathRemoveScript(`C:\Users\me\.local\bin`)
	require.Contains(t, s, `$d='C:\Users\me\.local\bin'`)
	require.Contains(t, s, "GetEnvironmentVariable('Path','User')")
	require.Contains(t, s, "SetEnvironmentVariable('Path',$n,'User')")
	// A single quote in the path is escaped so it cannot break the script.
	require.Contains(t, windowsPathRemoveScript(`C:\a'b`), `$d='C:\a''b'`)
}

func TestBinFileNames(t *testing.T) {
	require.Equal(t, []string{"seamlessd.exe", "seam.exe"}, binFileNames("windows"))
	require.Equal(t, []string{"seamlessd", "seam"}, binFileNames("darwin"))
	require.Equal(t, []string{"seamlessd", "seam"}, binFileNames("linux"))
}

func TestMCPArgs(t *testing.T) {
	require.Equal(t, []string{"mcp", "get", "seamless"}, mcpGetArgs())
	require.Equal(t, []string{"mcp", "remove", "seamless"}, mcpRemoveArgs())
}

func TestResolveInstallDir(t *testing.T) {
	t.Setenv("SEAMLESS_INSTALL_DIR", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// Explicit flag wins and expands ~.
	require.Equal(t, "/custom/bin", resolveInstallDir("/custom/bin"))
	require.Equal(t, filepath.Join(home, "x", "bin"), resolveInstallDir("~/x/bin"))

	// Env is next.
	t.Setenv("SEAMLESS_INSTALL_DIR", "/env/bin")
	require.Equal(t, "/env/bin", resolveInstallDir(""))

	// Default is ~/.local/bin.
	t.Setenv("SEAMLESS_INSTALL_DIR", "")
	require.Equal(t, filepath.Join(home, ".local", "bin"), resolveInstallDir(""))
}

func TestPurgeGuard(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	for _, bad := range []string{"", "   ", "/", ".", "~", home} {
		require.Error(t, purgeGuard(bad), "purgeGuard(%q) must refuse", bad)
	}
	require.NoError(t, purgeGuard(filepath.Join(home, ".seamless")))
	require.NoError(t, purgeGuard("/opt/seamless-data"))
}

func TestRemovedEvents(t *testing.T) {
	got := removedEvents([]string{"SessionStart: removed", "SessionEnd: absent", "PostToolUse: removed"})
	require.Equal(t, []string{"SessionStart", "PostToolUse"}, got)
	require.Empty(t, removedEvents([]string{"SessionStart: absent"}))
}

// removeBinary unlinks an existing file and is a no-op when it is already gone.
func TestRemoveBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seamlessd")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o755))
	require.NoError(t, removeBinary(path))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err))
	// Second call (already gone) is a clean no-op.
	require.NoError(t, removeBinary(path))
}
