package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceControl(t *testing.T) {
	const (
		home  = "/home/tester"
		uid   = 501
		label = "org.thereisnospoon.seamless"
	)
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	domain := "gui/501"
	target := "gui/501/" + label

	tests := []struct {
		name      string
		goos      string
		action    serviceAction
		wantLabel string
		wantCmds  [][]string
		wantFall  [][]string
	}{
		// darwin -- launchctl on the KeepAlive LaunchAgent
		{"darwin start", "darwin", actionStart, "launchd (" + label + ")",
			[][]string{{"launchctl", "bootstrap", domain, plist}}, nil},
		{"darwin stop", "darwin", actionStop, "launchd (" + label + ")",
			[][]string{{"launchctl", "bootout", target}}, nil},
		{"darwin restart", "darwin", actionRestart, "launchd (" + label + ")",
			[][]string{{"launchctl", "kickstart", "-k", target}},
			[][]string{{"launchctl", "bootstrap", domain, plist}}},
		{"darwin status", "darwin", actionStatus, "launchd (" + label + ")",
			[][]string{{"launchctl", "print", target}}, nil},
		// linux -- systemd --user
		{"linux start", "linux", actionStart, "systemd --user (seamless.service)",
			[][]string{{"systemctl", "--user", "start", "seamless.service"}}, nil},
		{"linux stop", "linux", actionStop, "systemd --user (seamless.service)",
			[][]string{{"systemctl", "--user", "stop", "seamless.service"}}, nil},
		{"linux restart", "linux", actionRestart, "systemd --user (seamless.service)",
			[][]string{{"systemctl", "--user", "restart", "seamless.service"}}, nil},
		{"linux status", "linux", actionStatus, "systemd --user (seamless.service)",
			[][]string{{"systemctl", "--user", "status", "seamless.service"}}, nil},
		// windows -- Scheduled Task (restart is End then Run: no single verb)
		{"windows start", "windows", actionStart, "Scheduled Task (Seamless)",
			[][]string{{"schtasks", "/Run", "/TN", "Seamless"}}, nil},
		{"windows stop", "windows", actionStop, "Scheduled Task (Seamless)",
			[][]string{{"schtasks", "/End", "/TN", "Seamless"}}, nil},
		{"windows restart", "windows", actionRestart, "Scheduled Task (Seamless)",
			[][]string{{"schtasks", "/End", "/TN", "Seamless"}, {"schtasks", "/Run", "/TN", "Seamless"}}, nil},
		{"windows status", "windows", actionStatus, "Scheduled Task (Seamless)",
			[][]string{{"schtasks", "/Query", "/TN", "Seamless", "/V", "/FO", "LIST"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := serviceControl(tt.action, tt.goos, home, uid)
			require.Equal(t, tt.wantLabel, p.Label)
			require.Equal(t, tt.wantCmds, argvs(p.Cmds))
			require.Equal(t, tt.wantFall, argvs(p.Fallback))
		})
	}
}

// TestServiceControlProbe checks the install-detection seam: darwin/linux carry a
// definition file to stat, Windows carries a query command instead.
func TestServiceControlProbe(t *testing.T) {
	const home = "/home/tester"

	dar := serviceControl(actionStatus, "darwin", home, 501)
	require.Equal(t, filepath.Join(home, "Library", "LaunchAgents", "org.thereisnospoon.seamless.plist"), dar.DefFile)
	require.Nil(t, dar.ProbeCmd)

	lin := serviceControl(actionStatus, "linux", home, 501)
	require.Equal(t, filepath.Join(home, ".config", "systemd", "user", "seamless.service"), lin.DefFile)
	require.Nil(t, lin.ProbeCmd)

	win := serviceControl(actionStatus, "windows", home, 501)
	require.Empty(t, win.DefFile)
	require.NotNil(t, win.ProbeCmd)
	require.Equal(t, []string{"schtasks", "/Query", "/TN", "Seamless"}, win.ProbeCmd.Args)
}

// TestServiceInstalled covers the file-stat path (darwin/linux) and the
// no-way-to-check fallthrough.
func TestServiceInstalled(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "seamless.service")
	require.NoError(t, os.WriteFile(present, []byte("x"), 0o644))

	require.True(t, serviceInstalled(serviceControlPlan{DefFile: present}))
	require.False(t, serviceInstalled(serviceControlPlan{DefFile: filepath.Join(dir, "absent.service")}))
	// Neither a DefFile nor a ProbeCmd -> nothing to check, so it does not block.
	require.True(t, serviceInstalled(serviceControlPlan{}))
}

func TestPastTense(t *testing.T) {
	require.Equal(t, "started", pastTense(actionStart))
	require.Equal(t, "stopped", pastTense(actionStop))
	require.Equal(t, "restarted", pastTense(actionRestart))
	require.Equal(t, "status", pastTense(actionStatus))
}
