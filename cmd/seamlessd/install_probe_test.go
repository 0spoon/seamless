package main

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/arctop/seamless/internal/update"
	"github.com/stretchr/testify/require"
)

// installerPlist is the plist docs/install writes, rendered for a home of /h.
const installerPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by https://thereisnospoon.org/install. Re-run it to update. -->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>org.thereisnospoon.seamless</string>
  <key>ProgramArguments</key>
  <array>
    <string>/h/.local/bin/seamlessd</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SEAMLESS_CONFIG</key>
    <string>/h/.config/seamless/seamless.yaml</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
`

// installerUnit is the unit docs/install writes, rendered for a home of /h.
const installerUnit = `# Written by https://thereisnospoon.org/install. Re-run it to update.
[Unit]
Description=Seamless -- local-first memory and coordination substrate for AI agents

[Service]
Type=simple
Environment=SEAMLESS_CONFIG=/h/.config/seamless/seamless.yaml
ExecStart=/h/.local/bin/seamlessd serve
Restart=always
`

const cgroupV2 = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/seamless.service\n"

func TestPlistProgram(t *testing.T) {
	require.Equal(t, "/h/.local/bin/seamlessd", plistProgram([]byte(installerPlist)))
	require.Equal(t, "/a&b/seamlessd", plistProgram([]byte(
		`<plist><dict><key>ProgramArguments</key><array><string>/a&amp;b/seamlessd</string></array></dict></plist>`)),
		"entities decode")
	require.Empty(t, plistProgram([]byte(`<plist><dict><key>Label</key><string>x</string></dict></plist>`)))
	require.Empty(t, plistProgram([]byte(`<plist><dict><key>ProgramArguments</key><array></array></dict></plist>`)))
	require.Empty(t, plistProgram([]byte(`not xml at all`)))
	// A string in another array is not the program.
	require.Empty(t, plistProgram([]byte(
		`<plist><dict><key>WatchPaths</key><array><string>/tmp</string></array></dict></plist>`)))
}

func TestUnitExecStart(t *testing.T) {
	tests := []struct {
		unit string
		want string
	}{
		{installerUnit, "/h/.local/bin/seamlessd"},
		{"[Service]\nExecStart=-/usr/bin/seamlessd serve\n", "/usr/bin/seamlessd"},
		{"[Service]\nExecStart=\"/opt/my apps/seamlessd\" serve\n", "/opt/my apps/seamlessd"},
		{"[Service]\nExecStart=/x/seamlessd\tserve\n", "/x/seamlessd"},
		{"[Service]\nType=simple\n", ""},
		{"[Service]\nExecStart=\n", ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, unitExecStart([]byte(tt.unit)), tt.unit)
	}
}

func TestInSystemdUnit(t *testing.T) {
	require.True(t, inSystemdUnit([]byte(cgroupV2), "seamless.service"))
	require.True(t, inSystemdUnit([]byte(
		"12:cpu,cpuacct:/user.slice\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/seamless.service\n"),
		"seamless.service"), "cgroup v1 reads the name=systemd hierarchy")
	require.False(t, inSystemdUnit([]byte("0::/user.slice/user-1000.slice/session-3.scope\n"), "seamless.service"))
	require.False(t, inSystemdUnit([]byte("0::/system.slice/not-seamless.service\n"), "seamless.service"))
	require.False(t, inSystemdUnit([]byte("12:cpu:/x/seamless.service\n"), "seamless.service"),
		"a non-systemd v1 controller does not count")
}

// utf16LE renders s the way schtasks writes it: UTF-16 little endian with a BOM.
func utf16LE(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

const taskXML = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Written by https://thereisnospoon.org/install.ps1. Re-run it to update.</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>S-1-5-21-1-2-3-1001</UserId>
      <LogonType>InteractiveToken</LogonType>
    </Principal>
  </Principals>
  <Actions Context="Author">
    <Exec>
      <Command>"C:\Users\me\AppData\Local\Programs\Seamless\seamlessd.exe"</Command>
      <Arguments>serve --config "C:\Users\me\.config\seamless\seamless.yaml"</Arguments>
    </Exec>
  </Actions>
</Task>`

func TestParseTaskXML(t *testing.T) {
	for name, raw := range map[string][]byte{"utf16": utf16LE(taskXML), "utf8": []byte(taskXML)} {
		t.Run(name, func(t *testing.T) {
			def, err := parseTaskXML(raw)
			require.NoError(t, err)
			require.Equal(t, `C:\Users\me\AppData\Local\Programs\Seamless\seamlessd.exe`, def.Command)
			require.Contains(t, def.Description, update.InstallerMarker)
			require.Equal(t, "S-1-5-21-1-2-3-1001", def.UserID)
		})
	}
	_, err := parseTaskXML([]byte("garbage"))
	require.Error(t, err)
}

func TestForeignSeamlessEnv(t *testing.T) {
	environ := []string{"PATH=/bin", "SEAMLESS_CONFIG=/c.yaml", "SEAMLESS_DATA_DIR=/d", "SEAMLESS_ADDR=x", "XSEAMLESS_Y=1"}
	require.Equal(t, []string{"SEAMLESS_ADDR", "SEAMLESS_DATA_DIR"}, foreignSeamlessEnv("linux", environ))
	require.Equal(t, []string{"SEAMLESS_ADDR"}, foreignSeamlessEnv("windows", []string{"Seamless_Config=c", "seamless_addr=x"}),
		"Windows names are case-insensitive")
	require.Empty(t, foreignSeamlessEnv("darwin", []string{"SEAMLESS_CONFIG=/c.yaml"}))
}

// fakeProbeEnv is an installer install on goos under home /h, every gate
// passing; tests break one fact at a time.
func fakeProbeEnv(goos string) installProbeEnv {
	files := map[string]string{
		"/h/Library/LaunchAgents/org.thereisnospoon.seamless.plist": installerPlist,
		"/h/.config/systemd/user/seamless.service":                  installerUnit,
		"/proc/self/cgroup": cgroupV2,
	}
	env := installProbeEnv{
		goos: goos,
		home: "/h",
		exe:  "/h/.local/bin/seamlessd",
		ppid: 1,
		euid: 501,
		readFile: func(p string) ([]byte, error) {
			if s, ok := files[filepath.ToSlash(p)]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		sameFile: func(a, b string) bool {
			norm := func(s string) string { return strings.ReplaceAll(s, `\`, "/") }
			return strings.EqualFold(norm(a), norm(b))
		},
		writable: func(string) bool { return true },
		taskXML:  func() ([]byte, error) { return nil, errors.New("not windows") },
	}
	switch goos {
	case "darwin":
		env.environ = []string{"XPC_SERVICE_NAME=org.thereisnospoon.seamless", "SEAMLESS_CONFIG=/h/.config/seamless/seamless.yaml"}
	case "linux":
		env.environ = []string{"INVOCATION_ID=abc", "SEAMLESS_CONFIG=/h/.config/seamless/seamless.yaml"}
	}
	return env
}

func TestProbeInstall(t *testing.T) {
	const cfgPath = "/h/.config/seamless/seamless.yaml"
	tests := []struct {
		name     string
		goos     string
		mutate   func(e *installProbeEnv)
		cfg      string
		dist     string
		wantKind update.Kind
		reason   string
	}{
		{"darwin installer", "darwin", nil, cfgPath, "release", update.KindInstaller, ""},
		{"linux installer", "linux", nil, cfgPath, "release", update.KindInstaller, ""},
		{"source build is never probed further", "darwin", func(e *installProbeEnv) {
			e.readFile = func(string) ([]byte, error) { panic("a source build must not read service files") }
			e.writable = func(string) bool { panic("a source build must not write a probe file") }
		}, cfgPath, "source", update.KindSource, "built from source"},
		{"make install plist (no marker)", "darwin", func(e *installProbeEnv) {
			e.readFile = func(p string) ([]byte, error) {
				return []byte(strings.Replace(installerPlist, "<!-- Written by https://thereisnospoon.org/install. Re-run it to update. -->\n", "", 1)), nil
			}
		}, cfgPath, "release", update.KindUnknown, "not written by the installer"},
		{"no unit (SEAMLESS_NO_SERVICE)", "linux", func(e *installProbeEnv) {
			e.readFile = func(string) ([]byte, error) { return nil, fs.ErrNotExist }
		}, cfgPath, "release", update.KindUnknown, "no installer-managed service"},
		{"unreadable unit", "linux", func(e *installProbeEnv) {
			e.readFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
		}, cfgPath, "release", update.KindUnknown, "could not be read"},
		{"service runs another copy", "darwin", func(e *installProbeEnv) { e.exe = "/opt/other/seamlessd" },
			cfgPath, "release", update.KindUnknown, "not this executable"},
		{"custom config", "linux", nil, "/srv/seamless.yaml", "release", update.KindUnknown, "/srv/seamless.yaml"},
		{"started by hand on darwin", "darwin", func(e *installProbeEnv) { e.environ = nil },
			cfgPath, "release", update.KindUnknown, "launchd job"},
		{"darwin parent is not launchd", "darwin", func(e *installProbeEnv) { e.ppid = 4242 },
			cfgPath, "release", update.KindUnknown, "not a child of launchd"},
		{"linux outside the unit", "linux", func(e *installProbeEnv) {
			e.readFile = func(p string) ([]byte, error) {
				if p == "/proc/self/cgroup" {
					return []byte("0::/user.slice/user-1000.slice/session-2.scope\n"), nil
				}
				return []byte(installerUnit), nil
			}
		}, cfgPath, "release", update.KindUnknown, "not running in the seamless.service unit"},
		{"linux without INVOCATION_ID", "linux", func(e *installProbeEnv) { e.environ = nil },
			cfgPath, "release", update.KindUnknown, "INVOCATION_ID"},
		{"env override the installer drops", "linux", func(e *installProbeEnv) {
			e.environ = append(e.environ, "SEAMLESS_ADDR=0.0.0.0:8081")
		}, cfgPath, "release", update.KindUnknown, "SEAMLESS_ADDR"},
		{"read-only bin dir", "darwin", func(e *installProbeEnv) { e.writable = func(string) bool { return false } },
			cfgPath, "release", update.KindUnknown, "not writable"},
		{"root", "linux", func(e *installProbeEnv) { e.euid = 0 }, cfgPath, "release", update.KindUnknown, "root"},
		{"homebrew", "darwin", func(e *installProbeEnv) { e.exe = "/opt/homebrew/Caskroom/seamless/0.7.2/seamlessd" },
			cfgPath, "release", update.KindHomebrew, "Homebrew"},
		{"windows task is the installer's but supervision is unverified", "windows", func(e *installProbeEnv) {
			e.exe = `C:\Users\me\AppData\Local\Programs\Seamless\seamlessd.exe`
			e.home = `C:\Users\me`
			e.taskXML = func() ([]byte, error) { return utf16LE(taskXML), nil }
		}, `C:\Users\me\.config\seamless\seamless.yaml`, "release", update.KindUnknown, "Scheduled Task check"},
		{"windows without the task", "windows", func(e *installProbeEnv) {
			e.home = `C:\Users\me`
		}, `C:\Users\me\.config\seamless\seamless.yaml`, "release", update.KindUnknown, "no installer-managed service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := fakeProbeEnv(tt.goos)
			if tt.mutate != nil {
				tt.mutate(&env)
			}
			got := update.Detect(probeInstallWith(env, tt.dist, "0.7.2", tt.cfg, false))
			require.Equal(t, tt.wantKind, got.Kind, got.Reason)
			if tt.reason == "" {
				require.Empty(t, got.Reason)
			} else {
				require.Contains(t, got.Reason, tt.reason)
			}
		})
	}
}

func TestProbeInstall_ClientStopsEarly(t *testing.T) {
	env := fakeProbeEnv("linux")
	env.readFile = func(string) ([]byte, error) { panic("a client machine has no service to read") }
	got := update.Detect(probeInstallWith(env, "release", "0.7.2", "/h/.config/seamless/seamless.yaml", true))
	require.Equal(t, update.KindClient, got.Kind)
}

// TestInstallerWritesTheMarker ties Detect to the real installer: the plist and
// the unit docs/install writes both carry update.InstallerMarker, and the
// plist `make install` renders does not -- which is the whole difference
// between an installer install and a developer's deploy.
func TestInstallerWritesTheMarker(t *testing.T) {
	install, err := os.ReadFile(filepath.Join("..", "..", "docs", "install"))
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(install), update.InstallerMarker),
		"docs/install writes the marker into both the launchd plist and the systemd unit")

	makePlist, err := os.ReadFile(filepath.Join("..", "..", "deploy", "launchd", launchdLabel+".plist"))
	require.NoError(t, err)
	require.NotContains(t, string(makePlist), update.InstallerMarker,
		"the make install plist must not look like the installer's")
}

func TestDirWritable(t *testing.T) {
	dir := t.TempDir()
	require.True(t, dirWritable(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "the probe file is removed")
	require.False(t, dirWritable(filepath.Join(dir, "missing")))
}

func TestSameFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o600))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(a, link); err == nil {
		require.True(t, sameFile(a, link))
	}
	require.True(t, sameFile(a, a))
	require.False(t, sameFile(a, filepath.Join(dir, "missing")))
}
