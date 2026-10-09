package main

// The OS reads behind update.Detect. Detect is a pure function of an
// update.Probe; this file gathers the probe -- the service definition the
// installer would own, the config in use, the environment, whether this
// process is the supervised instance -- so the classification itself stays
// testable on any OS while every system call lives here. The parsers are
// plain functions over bytes, so the darwin, linux and windows fixtures all
// run on the Linux CI.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/arctop/seamless/internal/update"
)

// installProbeEnv is the slice of the operating system probeInstall reads,
// injected so tests can stand up any OS's layout from fixtures.
type installProbeEnv struct {
	goos     string
	home     string
	exe      string   // this executable, symlinks resolved; "" when unknown
	environ  []string // KEY=VALUE, as os.Environ
	ppid     int
	euid     int
	readFile func(path string) ([]byte, error)
	sameFile func(a, b string) bool
	writable func(dir string) bool
	// taskXML returns the Scheduled Task's definition (`schtasks /Query /TN
	// Seamless /XML`); an error means the task is absent or unreadable.
	taskXML func() ([]byte, error)
}

// realInstallProbeEnv reads the live process and machine.
func realInstallProbeEnv() installProbeEnv {
	env := installProbeEnv{
		goos:     runtime.GOOS,
		environ:  os.Environ(),
		ppid:     os.Getppid(),
		euid:     os.Geteuid(),
		readFile: os.ReadFile,
		sameFile: sameFile,
		writable: dirWritable,
		taskXML: func() ([]byte, error) {
			return exec.Command("schtasks", "/Query", "/TN", scheduledTask, "/XML").Output()
		},
	}
	if home, err := os.UserHomeDir(); err == nil {
		env.home = home
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		env.exe = exe
	}
	return env
}

// probeInstall gathers the probe for this process: configPath is the config
// file it loaded ("" for none), client whether that config is role: client.
func probeInstall(distribution, ver, configPath string, client bool) update.Probe {
	return probeInstallWith(realInstallProbeEnv(), distribution, ver, configPath, client)
}

// probeInstallWith is probeInstall over an injected environment. A build that
// is not a release stops after the build facts: Detect decides from those
// alone, and a developer's build has no business being probed (the probe
// writes a scratch file to test the executable's directory).
func probeInstallWith(env installProbeEnv, distribution, ver, configPath string, client bool) update.Probe {
	p := update.Probe{
		Distribution: distribution,
		Version:      ver,
		Client:       client,
		Root:         env.euid == 0,
		Exe:          env.exe,
		ConfigPath:   configPath,
	}
	if !update.IsReleaseBuild(distribution, ver) || client {
		return p
	}
	if p.Exe != "" {
		p.ExeDirWritable = env.writable(filepath.Dir(p.Exe))
	}
	if configPath != "" && env.home != "" {
		installerConfig := filepath.Join(env.home, ".config", "seamless", "seamless.yaml")
		p.ConfigIsInstallers = env.sameFile(configPath, installerConfig)
	}
	p.ForeignEnv = foreignSeamlessEnv(env.goos, env.environ)
	p.Service = probeService(env)
	p.Supervised, p.SupervisedReason = probeSupervised(env)
	return p
}

// foreignSeamlessEnv lists the SEAMLESS_* variables in environ other than
// SEAMLESS_CONFIG, sorted. Windows environment names are case-insensitive.
func foreignSeamlessEnv(goos string, environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		name := k
		if goos == "windows" {
			name = strings.ToUpper(k)
		}
		if strings.HasPrefix(name, "SEAMLESS_") && name != "SEAMLESS_CONFIG" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// envValue is os.Getenv over an injected environ (last value wins, as in the
// real environment).
func envValue(environ []string, key string) string {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// probeService reads the service definition the installer writes on this OS
// and checks it is the installer's and runs this executable.
func probeService(env installProbeEnv) update.ServiceProbe {
	var (
		sp      update.ServiceProbe
		data    []byte
		program string
	)
	switch env.goos {
	case "darwin":
		sp.Path = filepath.Join(env.home, "Library", "LaunchAgents", launchdLabel+".plist")
		var err error
		if data, err = env.readFile(sp.Path); err != nil {
			return absentOrErr(sp, err)
		}
		program = plistProgram(data)
	case "windows":
		sp.Path = "Scheduled Task " + scheduledTask
		raw, err := env.taskXML()
		if err != nil {
			return sp // schtasks fails for a missing task; nothing to read
		}
		def, err := parseTaskXML(raw)
		if err != nil {
			sp.Err = err.Error()
			return sp
		}
		sp.Found = true
		sp.Marker = strings.Contains(def.Description, update.InstallerMarker)
		sp.Program = def.Command
		sp.RunsThisExe = def.Command != "" && env.exe != "" && env.sameFile(def.Command, env.exe)
		return sp
	default: // linux and other unixes run the systemd --user unit
		sp.Path = filepath.Join(env.home, ".config", "systemd", "user", systemdUnit)
		var err error
		if data, err = env.readFile(sp.Path); err != nil {
			return absentOrErr(sp, err)
		}
		program = unitExecStart(data)
	}
	sp.Found = true
	sp.Marker = bytes.Contains(data, []byte(update.InstallerMarker))
	sp.Program = program
	sp.RunsThisExe = program != "" && env.exe != "" && env.sameFile(program, env.exe)
	return sp
}

// absentOrErr records a failed definition read: a missing file is simply not
// found, anything else is an error worth naming.
func absentOrErr(sp update.ServiceProbe, err error) update.ServiceProbe {
	if !errors.Is(err, fs.ErrNotExist) {
		sp.Err = err.Error()
	}
	return sp
}

// probeSupervised reports whether this process is the service's own instance,
// not a release binary someone started by hand next to it.
//
//   - darwin: launchd names the job in XPC_SERVICE_NAME and is the parent
//     (ppid 1) of a job it spawned.
//   - linux: systemd places the unit's processes in its cgroup
//     (/proc/self/cgroup ends in /seamless.service) and sets INVOCATION_ID.
//   - windows: not decided yet -- how the Task Scheduler's process tree looks
//     from inside is what spike 2.01 of the auto-update plan measures, so no
//     Windows install counts as supervised until then.
func probeSupervised(env installProbeEnv) (bool, string) {
	switch env.goos {
	case "darwin":
		if envValue(env.environ, "XPC_SERVICE_NAME") != launchdLabel {
			return false, "not running as the launchd job " + launchdLabel
		}
		if env.ppid != 1 {
			return false, "the launchd job's process is not a child of launchd"
		}
		return true, ""
	case "windows":
		return false, "the Scheduled Task check is not available on Windows yet"
	default:
		cg, err := env.readFile("/proc/self/cgroup")
		if err != nil {
			return false, "could not read /proc/self/cgroup"
		}
		if !inSystemdUnit(cg, systemdUnit) {
			return false, "not running in the " + systemdUnit + " unit"
		}
		if envValue(env.environ, "INVOCATION_ID") == "" {
			return false, "not started by systemd (no INVOCATION_ID)"
		}
		return true, ""
	}
}

// inSystemdUnit reports whether a /proc/self/cgroup listing places the
// process in unit: the cgroup v2 line ("0::/.../seamless.service") or, on a
// cgroup v1 host, the name=systemd hierarchy's line.
func inSystemdUnit(cgroup []byte, unit string) bool {
	for line := range strings.SplitSeq(string(cgroup), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] != "" && parts[1] != "name=systemd" {
			continue // a v1 controller hierarchy other than systemd's
		}
		if strings.HasSuffix(parts[2], "/"+unit) {
			return true
		}
	}
	return false
}

// plistProgram returns ProgramArguments[0] from a launchd plist, or "" when
// the plist has none or does not parse.
func plistProgram(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	lastKey, inArgs := "", false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return ""
				}
				lastKey = strings.TrimSpace(k)
				continue
			case "array":
				inArgs = lastKey == "ProgramArguments"
			case "string":
				if inArgs {
					var v string
					if err := dec.DecodeElement(&v, &t); err != nil {
						return ""
					}
					return strings.TrimSpace(v)
				}
			}
			lastKey = ""
		case xml.EndElement:
			if t.Name.Local == "array" && inArgs {
				return "" // an empty ProgramArguments
			}
		}
	}
}

// unitExecStart returns the executable of a systemd unit's ExecStart=, or ""
// when there is none. systemd's executable prefixes (-, @, :, +, !) are
// dropped and a quoted path is unquoted.
func unitExecStart(data []byte) string {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		val, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		val = strings.TrimLeft(strings.TrimSpace(val), "-@:+!")
		if val == "" {
			return ""
		}
		if q := val[0]; q == '"' || q == '\'' {
			if end := strings.IndexByte(val[1:], q); end >= 0 {
				return val[1 : end+1]
			}
			return ""
		}
		prog, _, _ := strings.Cut(val, " ")
		prog, _, _ = strings.Cut(prog, "\t")
		return prog
	}
	return ""
}

// taskDefinition is the sliver of a Task Scheduler XML definition the probe
// reads.
type taskDefinition struct {
	Description string `xml:"RegistrationInfo>Description"`
	Command     string `xml:"Actions>Exec>Command"`
	UserID      string `xml:"Principals>Principal>UserId"`
}

// parseTaskXML decodes `schtasks /Query /XML` output. schtasks writes UTF-16
// with a byte-order mark and declares encoding="UTF-16"; the decoder only
// speaks UTF-8, so the bytes are converted first and the declaration is then
// accepted as already handled.
func parseTaskXML(raw []byte) (taskDefinition, error) {
	text := decodeUTF16(raw)
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var def taskDefinition
	if err := dec.Decode(&def); err != nil {
		return taskDefinition{}, err
	}
	def.Command = strings.Trim(strings.TrimSpace(def.Command), `"`)
	def.Description = strings.TrimSpace(def.Description)
	def.UserID = strings.TrimSpace(def.UserID)
	return def, nil
}

// decodeUTF16 turns BOM-marked UTF-16 into a Go string; bytes without a
// UTF-16 BOM are taken as UTF-8.
func decodeUTF16(b []byte) string {
	var order binary.ByteOrder
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		order = binary.LittleEndian
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		order = binary.BigEndian
	default:
		return strings.TrimPrefix(string(b), "\uFEFF")
	}
	b = b[2:]
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, order.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}

// sameFile reports whether a and b name the same existing file, the way the
// filesystem decides it (case-insensitive volumes, hard links, symlinks).
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// dirWritable reports whether dir accepts a new file, by creating and removing
// one: permission bits do not tell the whole story (ACLs, read-only mounts,
// Windows), and replacing a binary in place needs exactly this.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".seamless-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return false
	}
	return os.Remove(name) == nil
}
