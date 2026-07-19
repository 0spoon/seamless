package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// serviceAction is one lifecycle verb for the installed per-user service.
type serviceAction string

const (
	actionStart   serviceAction = "start"
	actionStop    serviceAction = "stop"
	actionRestart serviceAction = "restart"
	actionStatus  serviceAction = "status"
)

// serviceControlPlan is the OS-specific set of steps that start, stop, restart,
// or report the per-user service. Like serviceTeardownPlan it is a pure value, so
// the argv can be asserted in tests without executing anything.
//
// These verbs control an ALREADY-installed service; they never author one (that
// stays with `make install` and the installer scripts). Installation is detected
// by DefFile (a stat) on darwin/linux, or ProbeCmd (a query) on Windows, where
// the registration lives in the Task Scheduler rather than in a file.
type serviceControlPlan struct {
	Label       string      // human label for the service kind
	DefFile     string      // service-definition file to stat for the install check ("" -> use ProbeCmd)
	ProbeCmd    *exec.Cmd   // Windows: `schtasks /Query`; nil where DefFile suffices
	Cmds        []*exec.Cmd // the action's commands, in order; the last one's exit is the verdict
	Fallback    []*exec.Cmd // tried when Cmds fail (darwin restart -> bootstrap); nil elsewhere
	InstallHint string      // shown when not installed: how to install on this OS
}

// serviceControl builds the steps for action on goos. It mirrors serviceTeardown
// (same identifiers, same gui/<uid>/<label> target and unit path); the two are
// the only Go code that manages the service. The launchd/systemd/schtasks handles
// are the shared consts from uninstall.go -- no new definition site.
//
// darwin uses launchctl because the plist declares KeepAlive: bootout truly stops
// the job (a plain kill would be resurrected), kickstart -k restarts a loaded job
// in place, and bootstrap (re)loads it. systemd and schtasks map to their verbs
// directly. Windows restart has no single verb, so it is /End then /Run.
func serviceControl(action serviceAction, goos, home string, uid int) serviceControlPlan {
	switch goos {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		domain := fmt.Sprintf("gui/%d", uid)
		target := fmt.Sprintf("gui/%d/%s", uid, launchdLabel)
		p := serviceControlPlan{
			Label:       "launchd (" + launchdLabel + ")",
			DefFile:     plist,
			InstallHint: "run 'make install' in the repo, or: curl -fsSL https://thereisnospoon.org/install | sh",
		}
		switch action {
		case actionStart:
			p.Cmds = []*exec.Cmd{exec.Command("launchctl", "bootstrap", domain, plist)}
		case actionStop:
			p.Cmds = []*exec.Cmd{exec.Command("launchctl", "bootout", target)}
		case actionRestart:
			p.Cmds = []*exec.Cmd{exec.Command("launchctl", "kickstart", "-k", target)}
			p.Fallback = []*exec.Cmd{exec.Command("launchctl", "bootstrap", domain, plist)}
		case actionStatus:
			p.Cmds = []*exec.Cmd{exec.Command("launchctl", "print", target)}
		}
		return p
	case "windows":
		p := serviceControlPlan{
			Label:       "Scheduled Task (" + scheduledTask + ")",
			ProbeCmd:    exec.Command("schtasks", "/Query", "/TN", scheduledTask),
			InstallHint: "run the installer: irm https://thereisnospoon.org/install.ps1 | iex",
		}
		switch action {
		case actionStart:
			p.Cmds = []*exec.Cmd{exec.Command("schtasks", "/Run", "/TN", scheduledTask)}
		case actionStop:
			p.Cmds = []*exec.Cmd{exec.Command("schtasks", "/End", "/TN", scheduledTask)}
		case actionRestart:
			p.Cmds = []*exec.Cmd{
				exec.Command("schtasks", "/End", "/TN", scheduledTask),
				exec.Command("schtasks", "/Run", "/TN", scheduledTask),
			}
		case actionStatus:
			p.Cmds = []*exec.Cmd{exec.Command("schtasks", "/Query", "/TN", scheduledTask, "/V", "/FO", "LIST")}
		}
		return p
	default: // linux and other unixes run the systemd --user unit
		unit := filepath.Join(home, ".config", "systemd", "user", systemdUnit)
		p := serviceControlPlan{
			Label:       "systemd --user (" + systemdUnit + ")",
			DefFile:     unit,
			InstallHint: "run the installer: curl -fsSL https://thereisnospoon.org/install | sh",
		}
		switch action {
		case actionStart:
			p.Cmds = []*exec.Cmd{exec.Command("systemctl", "--user", "start", systemdUnit)}
		case actionStop:
			p.Cmds = []*exec.Cmd{exec.Command("systemctl", "--user", "stop", systemdUnit)}
		case actionRestart:
			p.Cmds = []*exec.Cmd{exec.Command("systemctl", "--user", "restart", systemdUnit)}
		case actionStatus:
			p.Cmds = []*exec.Cmd{exec.Command("systemctl", "--user", "status", systemdUnit)}
		}
		return p
	}
}

// serviceInstalled reports whether the service is registered. On darwin/linux
// that is a stat of the definition file; on Windows there is no file, so a
// best-effort ProbeCmd (`schtasks /Query`) stands in -- a non-zero exit (task
// absent) reads as not installed. With no way to check, it does not block.
func serviceInstalled(p serviceControlPlan) bool {
	if p.DefFile != "" {
		_, err := os.Stat(p.DefFile)
		return err == nil
	}
	if p.ProbeCmd != nil {
		return p.ProbeCmd.Run() == nil
	}
	return true
}

// runServiceAction runs one lifecycle verb against the installed service. It is
// the entrypoint for `seamlessd start|stop|restart|status`. When the service is
// not installed it returns a clear install hint instead of a cryptic tool error.
func runServiceAction(action serviceAction, args []string) error {
	fs := flag.NewFlagSet(string(action), flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	plan := serviceControl(action, runtime.GOOS, homeDir(), os.Getuid())

	if !serviceInstalled(plan) {
		where := plan.Label
		if plan.DefFile != "" {
			where = plan.Label + " [" + tildePath(plan.DefFile) + "]"
		}
		return fmt.Errorf("service not installed: %s -- %s", where, plan.InstallHint)
	}

	if action == actionStatus {
		return reportServiceStatus(plan)
	}

	fmt.Printf("\n%s %s\n", bold("Seamless"), dim(string(action)))
	fieldRow("kind", plan.Label)

	ok, out := runControlCmds(plan.Cmds)
	if !ok && len(plan.Fallback) > 0 {
		ok, out = runControlCmds(plan.Fallback)
	}
	if ok {
		fieldRow(string(action), green(pastTense(action)))
	} else {
		fieldRow(string(action), yellow("could not "+string(action)))
		if out != "" {
			contDim(out)
		}
	}
	return nil
}

// runControlCmds runs each command, best-effort, and returns whether the LAST
// command succeeded plus its trimmed output. The last command establishes the
// desired end state (e.g. schtasks /Run after /End on restart); earlier commands
// are setup whose failure is ignored.
func runControlCmds(cmds []*exec.Cmd) (bool, string) {
	if len(cmds) == 0 {
		return true, ""
	}
	var ok bool
	var out string
	for i, c := range cmds {
		o, err := c.CombinedOutput()
		if i == len(cmds)-1 {
			ok = err == nil
			out = strings.TrimSpace(string(o))
		}
	}
	return ok, out
}

// reportServiceStatus streams the platform tool's own status output verbatim
// (launchctl print / systemctl --user status / schtasks /Query). A non-zero exit
// -- an installed-but-not-loaded service -- is a dim hint, not an error, because
// status is informational.
func reportServiceStatus(p serviceControlPlan) error {
	if len(p.Cmds) == 0 {
		return nil
	}
	out, err := p.Cmds[0].CombinedOutput()
	if s := strings.TrimRight(string(out), "\n"); s != "" {
		fmt.Println(s)
	}
	if err != nil {
		fmt.Printf("%s %s\n", dim(p.Label+":"), dim("not currently loaded (run 'seamlessd start')"))
	}
	return nil
}

// pastTense is the confirmation verb printed after a completed action.
func pastTense(action serviceAction) string {
	switch action {
	case actionStart:
		return "started"
	case actionStop:
		return "stopped"
	case actionRestart:
		return "restarted"
	default:
		return string(action)
	}
}
