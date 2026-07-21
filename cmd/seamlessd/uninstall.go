package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
	agentskills "github.com/0spoon/seamless/internal/skills"
)

// Service identifiers, duplicated on purpose from the three install surfaces that
// each own a copy: the Makefile + deploy/launchd/org.thereisnospoon.seamless.plist
// (launchd), docs/install (systemd --user), and docs/install.ps1 (Scheduled Task).
// Each installer is fetched standalone with no shared template to read, so the
// names live at every site; keep these in sync when any installer renames one.
const (
	launchdLabel  = "org.thereisnospoon.seamless"
	systemdUnit   = "seamless.service"
	scheduledTask = "Seamless"
)

// runUninstall reverses a full install on any supported OS: it stops and removes
// the per-user service, strips the Claude Code / Codex hook entries, deregisters
// the MCP server, removes the installed skills, and deletes the binaries. Config
// and the data dir (~/.seamless -- memories and notes are markdown that outlive
// the program) are kept unless --purge is passed. Every external step is
// best-effort: an already-gone file or a missing client CLI is a note, never a
// failure, so uninstall is idempotent and safe to re-run.
func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	clientFlag := fs.String("client", "all", "which agent client to remove hooks/MCP for: claude|codex|claude-desktop|all|detect")
	purge := fs.Bool("purge", false, "also delete the config dir (~/.config/seamless) and data dir (~/.seamless)")
	dryRun := fs.Bool("dry-run", false, "print what would be removed and exit without changing anything")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	settings := fs.String("settings", "~/.claude/settings.json", "Claude Code settings.json to remove hooks from")
	codexHooksFlag := fs.String("codex-hooks", "", "Codex hooks.json to remove hooks from (default $CODEX_HOME/hooks.json, else ~/.codex/hooks.json)")
	desktopConfigFlag := fs.String("desktop-config", "", "Claude desktop app claude_desktop_config.json to remove the MCP bridge from (default: the app's per-OS location)")
	urlFlag := fs.String("url", "", "base URL of seamlessd (default derived from config addr)")
	installDirFlag := fs.String("install-dir", "", "directory the binaries were installed to (default $SEAMLESS_INSTALL_DIR, else ~/.local/bin)")
	mcpFlag := fs.Bool("mcp", true, "also deregister the MCP server (claude/codex mcp remove)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The Claude app chat surface is uninstalled by removing its desktop-config
	// entry -- no hooks, no skills, no client CLI. --client claude-desktop scopes
	// the client step to that alone; "all" (the default) includes it after the
	// hook clients, so a full uninstall never leaves the app pointed at a deleted
	// bridge binary. An absent file or entry stays a quiet note.
	rawClient := strings.ToLower(strings.TrimSpace(*clientFlag))
	desktopOnly := isClaudeDesktopSelector(rawClient)
	includeDesktop := desktopOnly || rawClient == "all" || rawClient == "both"
	var clients []hooks.Client
	if !desktopOnly {
		var err error
		clients, err = parseInstallClients(*clientFlag, claudeDetected(), codexDetected())
		if err != nil {
			return fmt.Errorf("seamlessd.uninstall: %w", err)
		}
	}

	// Config is best-effort: uninstall must work when the config is broken or
	// gone, and must NEVER mint a key file (no EnsureAPIKey). A load failure falls
	// back to defaults for the data dir and the hook base URL.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		fmt.Printf("%s could not load config (%v); using defaults\n", yellow("warning:"), cfgErr)
		cfg = config.Defaults()
		if dd, e := expandHome(cfg.DataDir); e == nil {
			cfg.DataDir = dd
		}
	}
	baseURL := strings.TrimSpace(*urlFlag)
	if baseURL == "" {
		baseURL = hookBaseURL(cfg.Addr)
	}
	installDir := resolveInstallDir(*installDirFlag)
	configDir, cerr := expandHome("~/.config/seamless")
	if cerr != nil {
		configDir = "~/.config/seamless"
	}
	dataDir := cfg.DataDir

	names := clientNames(clients)
	if includeDesktop {
		names = append(names, "Claude app (chat)")
	}
	printUninstallPreamble(names, installDir, configDir, dataDir, *purge, *dryRun)

	if !*dryRun && !*yes && stdinIsTerminal() && !confirmUninstall(os.Stdin, os.Stdout) {
		fmt.Println(dim("aborted -- nothing was changed"))
		return nil
	}

	// Service first: stopping it releases the running binary (notably the Windows
	// image lock) before the binaries are removed.
	fmt.Printf("\n%s\n", bold("Service"))
	runTeardown(serviceTeardown(runtime.GOOS, homeDir(), os.Getuid(), installDir), *dryRun)

	for _, client := range clients {
		path, cli := *settings, "claude"
		if client == hooks.ClientCodex {
			path, cli = strings.TrimSpace(*codexHooksFlag), "codex"
			if path == "" {
				path = defaultCodexHooksPath()
			}
		}
		statusOpts := hooks.InstallOptions{
			Client: client, BaseURL: baseURL, APIKey: cfg.MCP.APIKey,
			SeamBin: filepath.Join(installDir, seamBinName()), ConfigPath: absConfigPath(cfg.SourcePath()),
		}
		if err := uninstallClientHooks(client, path, statusOpts, *dryRun); err != nil {
			return fmt.Errorf("seamlessd.uninstall: %w", err)
		}
		if *mcpFlag {
			deregisterMCP(cli, *dryRun)
		}
	}

	if includeDesktop && *mcpFlag {
		fmt.Printf("\n%s\n", bold("Claude app (chat)"))
		deregisterClaudeDesktopMCP(*desktopConfigFlag, *dryRun)
	}

	if len(clients) > 0 {
		removeSkills(clients, *dryRun)
	}
	removeBinaries(installDir, runtime.GOOS, *dryRun)
	if *purge {
		purgeData(configDir, dataDir, *dryRun)
	}

	fmt.Printf("\n%s\n", green("Seamless uninstalled."+dryRunTag(*dryRun)))
	if !*purge {
		fmt.Printf("%s%s\n", fieldCont, dim("kept "+tildePath(configDir)+" and "+tildePath(dataDir)+" -- re-run with --purge to delete them"))
	}
	return nil
}

// printUninstallPreamble prints the header block: which client targets, where
// the binaries live, and whether user data is kept or purged.
func printUninstallPreamble(names []string, installDir, configDir, dataDir string, purge, dryRun bool) {
	fmt.Printf("\n%s %s\n", bold("Seamless"), dim("uninstall"+dryRunTag(dryRun)))
	fieldRow("clients", strings.Join(names, ", "))
	fieldRow("bin", tildePath(installDir))
	if purge {
		fieldRow("purge", yellow("will delete ")+dim(tildePath(configDir)+", "+tildePath(dataDir)))
	} else {
		fieldRow("keep", dim(tildePath(configDir)+", "+tildePath(dataDir)+" (use --purge to delete)"))
	}
}

// clientNames maps client profiles to their display labels, in order.
func clientNames(clients []hooks.Client) []string {
	out := make([]string, len(clients))
	for i, c := range clients {
		if c == hooks.ClientCodex {
			out[i] = "Codex"
		} else {
			out[i] = "Claude Code"
		}
	}
	return out
}

// confirmUninstall asks for confirmation, defaulting to No so a stray Enter is
// safe. Only called on an interactive terminal without --yes/--dry-run.
func confirmUninstall(in io.Reader, out io.Writer) bool {
	fmt.Fprintf(out, "\n%s remove Seamless from this machine? [y/N]: ", bold("uninstall:"))
	line, _ := bufio.NewReader(in).ReadString('\n') //nolint:errcheck // EOF/short read still yields the text read; an empty line declines, the safe default
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// resolveInstallDir picks where the binaries were installed: an explicit
// --install-dir wins, then $SEAMLESS_INSTALL_DIR (the installers' override), else
// ~/.local/bin. A leading ~ is expanded.
func resolveInstallDir(flagVal string) string {
	for _, cand := range []string{strings.TrimSpace(flagVal), strings.TrimSpace(os.Getenv("SEAMLESS_INSTALL_DIR"))} {
		if cand != "" {
			if p, err := expandHome(cand); err == nil {
				return p
			}
			return cand
		}
	}
	if p, err := expandHome("~/.local/bin"); err == nil {
		return p
	}
	return filepath.Join(homeDir(), ".local", "bin")
}

// homeDir returns the user's home directory, or "" if undiscoverable (callers
// build paths that simply won't match anything, which is a safe no-op).
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// serviceTeardownPlan is the OS-specific set of steps that stop and deregister
// the per-user service. It is a pure value so it can be asserted in tests
// without executing anything, mirroring browserCommand's shape.
type serviceTeardownPlan struct {
	Label       string      // human label for the service kind
	StopCmds    []*exec.Cmd // run first, best-effort (stop + deregister)
	RemoveFiles []string    // os.Remove after StopCmds (a missing file is fine)
	ReloadCmds  []*exec.Cmd // run after RemoveFiles, best-effort
	PathEdits   []*exec.Cmd // Windows only: strip the install dir from the user PATH
}

// serviceTeardown builds the teardown steps for goos. This is the first place
// service management lives in Go; install writes the plist/unit/task from the
// Makefile and the two installer scripts, so the identifiers are re-derived here.
func serviceTeardown(goos, home string, uid int, installDir string) serviceTeardownPlan {
	switch goos {
	case "darwin":
		plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
		return serviceTeardownPlan{
			Label:       "launchd (" + launchdLabel + ")",
			StopCmds:    []*exec.Cmd{exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, launchdLabel))},
			RemoveFiles: []string{plist},
		}
	case "windows":
		return serviceTeardownPlan{
			Label: "Scheduled Task (" + scheduledTask + ")",
			StopCmds: []*exec.Cmd{
				exec.Command("schtasks", "/End", "/TN", scheduledTask),
				exec.Command("schtasks", "/Delete", "/TN", scheduledTask, "/F"),
			},
			PathEdits: []*exec.Cmd{exec.Command("powershell", "-NoProfile", "-Command", windowsPathRemoveScript(installDir))},
		}
	default: // linux and other unixes run the systemd --user unit
		unit := filepath.Join(home, ".config", "systemd", "user", systemdUnit)
		return serviceTeardownPlan{
			Label:       "systemd --user (" + systemdUnit + ")",
			StopCmds:    []*exec.Cmd{exec.Command("systemctl", "--user", "disable", "--now", systemdUnit)},
			RemoveFiles: []string{unit},
			ReloadCmds:  []*exec.Cmd{exec.Command("systemctl", "--user", "daemon-reload")},
		}
	}
}

// windowsPathRemoveScript is the PowerShell that removes installDir from the User
// Path -- the exact inverse of install.ps1's SetEnvironmentVariable('Path', ...,
// 'User'), which also rewrites the registry and broadcasts WM_SETTINGCHANGE.
func windowsPathRemoveScript(installDir string) string {
	d := strings.ReplaceAll(installDir, "'", "''") // PowerShell single-quote escape
	return fmt.Sprintf(`$d='%s'; $p=[Environment]::GetEnvironmentVariable('Path','User'); `+
		`if ($p) { $n=(($p -split ';') | Where-Object { $_ -and $_ -ne $d }) -join ';'; `+
		`[Environment]::SetEnvironmentVariable('Path',$n,'User') }`, d)
}

// runTeardown executes (or, in dry-run, describes) a service teardown plan. Stop,
// reload, and PATH commands are best-effort: an unloaded service, an absent task,
// or a locked-down box are all expected and must not fail the uninstall.
func runTeardown(p serviceTeardownPlan, dryRun bool) {
	fieldRow("kind", p.Label)
	if dryRun {
		for _, c := range p.StopCmds {
			contDim("would run " + strings.Join(c.Args, " "))
		}
		for _, f := range p.RemoveFiles {
			contDim("would remove " + tildePath(f))
		}
		for _, c := range p.ReloadCmds {
			contDim("would run " + strings.Join(c.Args, " "))
		}
		if len(p.PathEdits) > 0 {
			contDim("would remove the install dir from the user PATH")
		}
		return
	}
	runBestEffort(p.StopCmds)
	for _, f := range p.RemoveFiles {
		switch err := os.Remove(f); {
		case err == nil:
			cont(green("removed ") + dim(tildePath(f)))
		case os.IsNotExist(err):
			// nothing installed here
		default:
			cont(yellow("could not remove ") + dim(tildePath(f)))
		}
	}
	runBestEffort(p.ReloadCmds)
	runBestEffort(p.PathEdits)
	cont(green("stopped & deregistered"))
}

// runBestEffort runs each command and ignores failures: during teardown an
// unloaded service, an absent Scheduled Task, or a locked-down box are all
// expected, not errors that should stop the uninstall.
func runBestEffort(cmds []*exec.Cmd) {
	for _, c := range cmds {
		_ = c.Run() //nolint:errcheck // teardown commands are best-effort by design
	}
}

// uninstallClientHooks removes (or previews) one client's Seamless hook entries
// from its settings/hooks file, printing a block that matches install's shape.
func uninstallClientHooks(client hooks.Client, path string, statusOpts hooks.InstallOptions, dryRun bool) error {
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	label := "Claude Code"
	if client == hooks.ClientCodex {
		label = "Codex"
	}
	fmt.Printf("\n%s\n", bold(label))

	if dryRun {
		statusOpts.Client = client
		statusOpts.SettingsPath = expanded
		status, err := hooks.InstalledStatus(statusOpts)
		if err != nil {
			return err
		}
		printHookRow(len(status.Owned), status.Owned, expanded, true)
		return nil
	}
	res, err := hooks.Uninstall(hooks.UninstallOptions{Client: client, SettingsPath: expanded, BaseURL: statusOpts.BaseURL})
	if err != nil {
		return err
	}
	printHookRow(len(removedEvents(res.Actions)), removedEvents(res.Actions), expanded, false)
	if res.BackupPath != "" {
		fieldRow("backup", dim(tildePath(res.BackupPath)))
	}
	return nil
}

// printHookRow renders the "hooks" summary line for a client block.
func printHookRow(n int, events []string, path string, dryRun bool) {
	if n == 0 {
		fieldRow("hooks", dim("none installed  · "+tildePath(path)))
		return
	}
	verb := green(fmt.Sprintf("removed %d", n))
	if dryRun {
		verb = dim(fmt.Sprintf("would remove %d", n))
	}
	fieldRow("hooks", verb+"  "+dim("· "+tildePath(path)))
	contDim(strings.Join(events, ", "))
}

// removedEvents returns the event names an uninstall removed.
func removedEvents(actions []string) []string {
	var out []string
	for _, a := range actions {
		if event, act, ok := strings.Cut(a, ": "); ok && act == "removed" {
			out = append(out, event)
		}
	}
	return out
}

// mcpGetArgs / mcpRemoveArgs are the client-CLI argv for probing and removing the
// Seamless MCP server. Both clients name the server "seamless" (see
// claudeMCPAddArgs / codexMCPAddArgs) and `mcp remove` drops it from whichever
// scope holds it, so one pair serves both.
func mcpGetArgs() []string    { return []string{"mcp", "get", "seamless"} }
func mcpRemoveArgs() []string { return []string{"mcp", "remove", "seamless"} }

// deregisterMCP removes the Seamless MCP registration from a client's CLI,
// best-effort and symmetric with registerClaudeMCP/registerCodexMCP: a missing
// CLI or an absent registration is a quiet, dim note.
func deregisterMCP(cli string, dryRun bool) {
	bin, err := exec.LookPath(cli)
	if err != nil {
		fieldRow("mcp", dim(cli+" CLI not found"))
		return
	}
	if dryRun {
		fieldRow("mcp", dim("would run "+cli+" "+strings.Join(mcpRemoveArgs(), " ")))
		return
	}
	runner := execMCPCommandRunner{client: cli, path: bin, timeout: mcpCommandTimeout}
	ctx := context.Background()
	if _, getErr := runner.Run(ctx, mcpGetArgs()...); getErr != nil {
		fieldRow("mcp", dim("not registered"))
		return
	}
	if out, aerr := runner.Run(ctx, mcpRemoveArgs()...); aerr != nil {
		fieldRow("mcp", yellow("could not deregister"))
		contDim(strings.TrimSpace(string(out)))
		return
	}
	fieldRow("mcp", green("deregistered"))
}

// removeSkills removes only the selected clients' maintained packages and
// delivered-once markers. Claude and Codex homes are independent, including a
// custom CODEX_HOME, so --client=codex never disturbs Claude's skills.
func removeSkills(clients []hooks.Client, dryRun bool) {
	fmt.Printf("\n%s\n", bold("Skills"))
	opts, err := agentskills.OptionsFromEnvironment()
	if err != nil {
		fieldRow("skills", dim("none installed"))
		return
	}
	for _, client := range clients {
		label := "Claude Code"
		if client == hooks.ClientCodex {
			label = "Codex"
		}
		skillClient, clientErr := agentSkillClient(client)
		if clientErr != nil {
			fieldRow(label, yellow("could not resolve skill home"))
			contDim(clientErr.Error())
			continue
		}
		removed, removeErr := agentskills.Remove(skillClient, opts, dryRun)
		if removeErr != nil {
			fieldRow(label, yellow("could not remove skills"))
			contDim(removeErr.Error())
			continue
		}
		items := append([]string(nil), removed.Skills...)
		if removed.Marker {
			items = append(items, agentskills.OnboardMarker)
		}
		switch {
		case len(items) == 0:
			fieldRow(label, dim("none installed  · "+tildePath(removed.Root)))
		case dryRun:
			fieldRow(label, dim("would remove "+strings.Join(items, ", "))+dim("  · "+tildePath(removed.Root)))
		default:
			fieldRow(label, green("removed ")+dim(strings.Join(items, ", "))+dim("  · "+tildePath(removed.Root)))
		}
	}
}

// removeBinaries deletes the installed seamlessd/seam binaries from installDir.
func removeBinaries(installDir, goos string, dryRun bool) {
	fmt.Printf("\n%s\n", bold("Binaries"))
	for _, name := range binFileNames(goos) {
		path := filepath.Join(installDir, name)
		if _, err := os.Stat(path); err != nil {
			fieldRow("bin", dim(tildePath(path)+" (absent)"))
			continue
		}
		if dryRun {
			fieldRow("bin", dim("would remove "+tildePath(path)))
			continue
		}
		if err := removeBinary(path); err != nil {
			fieldRow("bin", yellow("could not remove ")+dim(tildePath(path)+": "+err.Error()))
		} else {
			fieldRow("bin", green("removed ")+dim(tildePath(path)))
		}
	}
}

// binFileNames is the installed binary filenames for goos (.exe on Windows).
func binFileNames(goos string) []string {
	if goos == "windows" {
		return []string{"seamlessd.exe", "seam.exe"}
	}
	return []string{"seamlessd", "seam"}
}

// removeBinary deletes an installed binary. On POSIX os.Remove unlinks even the
// running seamlessd (the inode is freed at process exit). On Windows a running
// .exe image is locked and cannot be deleted -- when the user runs the INSTALLED
// seamlessd.exe uninstall, this process still holds its own image. Renaming a
// loaded exe IS allowed there, so fall back to renaming it aside (mirroring the
// installer's Install-OneBinary) and a best-effort delete; a still-locked
// leftover (<name>.old) is harmless and cleared by a later run.
func removeBinary(path string) error {
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return nil
	}
	aside := path + ".old"
	_ = os.Remove(aside)
	if err := os.Rename(path, aside); err != nil {
		return err
	}
	_ = os.Remove(aside) // may still be locked until this process exits; fine
	return nil
}

// purgeData deletes the config and data directories (only with --purge). A guard
// refuses obviously wrong targets so a misconfigured data_dir cannot turn --purge
// into a catastrophe.
func purgeData(configDir, dataDir string, dryRun bool) {
	fmt.Printf("\n%s\n", bold("Purge"))
	for _, d := range []string{configDir, dataDir} {
		if err := purgeGuard(d); err != nil {
			fieldRow("purge", yellow("refused ")+dim(tildePath(d)+": "+err.Error()))
			continue
		}
		if _, err := os.Stat(d); err != nil {
			fieldRow("purge", dim(tildePath(d)+" (absent)"))
			continue
		}
		if dryRun {
			fieldRow("purge", dim("would delete "+tildePath(d)))
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			fieldRow("purge", yellow("could not delete ")+dim(tildePath(d)))
		} else {
			fieldRow("purge", green("deleted ")+dim(tildePath(d)))
		}
	}
}

// purgeGuard refuses to delete a path that is empty, the filesystem root, or the
// user's home directory -- guarding against a misconfigured data_dir (e.g. "~"
// or "/") turning --purge into a disaster.
func purgeGuard(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("empty path")
	}
	clean := filepath.Clean(p)
	if clean == string(filepath.Separator) || clean == "." || clean == "~" {
		return fmt.Errorf("refuses %q", clean)
	}
	if home := homeDir(); home != "" && filepath.Clean(home) == clean {
		return fmt.Errorf("refuses the home directory")
	}
	return nil
}

// cont prints a continuation line aligned under a fieldRow's value column.
func cont(s string) { fmt.Printf("%s%s\n", fieldCont, s) }

// contDim prints a dimmed continuation line.
func contDim(s string) { cont(dim(s)) }

// dryRunTag annotates headers during a dry run.
func dryRunTag(dryRun bool) string {
	if dryRun {
		return " (dry run)"
	}
	return ""
}
