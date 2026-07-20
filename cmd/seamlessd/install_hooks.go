package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
	agentskills "github.com/0spoon/seamless/internal/skills"
)

// runInstallHooks wires an agent client to Seamless in one command: it installs
// the hooks into that client's config file and, unless --mcp=false, registers
// the MCP server with the client's CLI. --client selects which: claude
// (~/.claude/settings.json + `claude mcp add`), codex ($CODEX_HOME/hooks.json +
// `codex mcp add ... seam mcp-proxy`), all (both), or detect (the default: the
// clients present on this machine, the same selection the curl installer
// makes; an error when none are, so nothing is wired without an explicit
// choice). For the P2 dogfood, point --settings at THIS repo's project-scoped
// .claude/settings.json so v2 hooks fire only here.
func runInstallHooks(args []string) error {
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	clientFlag := fs.String("client", "detect", "which agent client to install for: claude|codex|all|detect")
	settings := fs.String("settings", "~/.claude/settings.json", "Claude Code settings.json to install into")
	codexHooksFlag := fs.String("codex-hooks", "", "Codex hooks.json to install into (default $CODEX_HOME/hooks.json, else ~/.codex/hooks.json)")
	urlFlag := fs.String("url", "", "base URL of seamlessd (default derived from config addr)")
	seamFlag := fs.String("seam", "", "path to the seam CLI for command hooks (default: sibling of this binary, else PATH)")
	mcpFlag := fs.Bool("mcp", true, "register the MCP server with the client's CLI (claude/codex mcp add)")
	skillsFlag := fs.Bool("skills", true, "install the client's seam-onboard and seam-research skills")
	if err := fs.Parse(args); err != nil {
		return err
	}
	clientSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "client" {
			clientSet = true
		}
	})
	clients, err := resolveInstallClients(*clientFlag, clientSet)
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}
	var keyPath string
	cfg, keyPath, err = config.EnsureAPIKey(cfg)
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}
	if keyPath != "" {
		fmt.Printf("%s generated mcp.api_key %s\n", green("first run:"), dim("-> "+tildePath(keyPath)))
	}
	if strings.TrimSpace(cfg.MCP.APIKey) == "" {
		src := cfg.SourcePath()
		if src == "" {
			src = "the environment (SEAMLESS_MCP_API_KEY)"
		}
		return fmt.Errorf("seamlessd.install-hooks: mcp.api_key is empty in %s; set it (openssl rand -hex 32)", src)
	}
	baseURL := *urlFlag
	if baseURL == "" {
		baseURL = hookBaseURL(cfg.Addr)
	}
	seamBin := resolveSeamBin(*seamFlag)
	if _, lookErr := exec.LookPath(seamBin); lookErr != nil {
		fmt.Printf("%s seam CLI not found (%q); command hooks fail until it is installed\n%s%s\n",
			yellow("warning:"), seamBin, fieldCont, dim("go install github.com/0spoon/seamless/cmd/seam@latest"))
	}
	configPath := absConfigPath(cfg.SourcePath())
	var skillOpts agentskills.Options
	if *skillsFlag {
		skillOpts, err = agentskills.OptionsFromEnvironment()
		if err != nil {
			return fmt.Errorf("seamlessd.install-hooks: %w", err)
		}
	}

	for _, client := range clients {
		switch client {
		case hooks.ClientCodex:
			if err := installCodexHooks(cfg, *codexHooksFlag, baseURL, seamBin, configPath, *mcpFlag); err != nil {
				return fmt.Errorf("seamlessd.install-hooks: %w", err)
			}
		default:
			if err := installClaudeHooks(cfg, *settings, baseURL, seamBin, configPath, *mcpFlag); err != nil {
				return fmt.Errorf("seamlessd.install-hooks: %w", err)
			}
		}
		if *skillsFlag {
			// Skills are an optional convenience layer, so they degrade like the
			// missing-seam-binary and MCP-registration steps rather than aborting.
			// install-hooks runs from `curl | sh` under `set -eu` before the service
			// is registered: a non-writable ~/.claude/skills must not cost the user
			// their daemon, nor stop a later client in the --client all loop.
			if err := installClientSkills(client, skillOpts); err != nil {
				fmt.Printf("%s skills for %s: %v\n%s%s\n", yellow("warning:"), client, err,
					fieldCont, dim("set SEAMLESS_NO_ONBOARD_SKILL=1 / SEAMLESS_NO_RESEARCH_SKILL=1 to skip, or rerun install-hooks --skills"))
			}
		}
	}
	return nil
}

// installClientSkills follows the same client selection as hooks and MCP. The
// embedded assets make this work from an installed release binary; no checkout
// or archive path is needed at runtime.
func installClientSkills(client hooks.Client, opts agentskills.Options) error {
	skillClient, err := agentSkillClient(client)
	if err != nil {
		return err
	}
	result, err := agentskills.Install(skillClient, opts)
	if err != nil {
		return err
	}
	printSkillAction("onboard", agentskills.OnboardName, result.Root, result.Onboard,
		"SEAMLESS_NO_ONBOARD_SKILL")
	printSkillAction("research", agentskills.ResearchName, result.Root, result.Research,
		"SEAMLESS_NO_RESEARCH_SKILL")
	return nil
}

func agentSkillClient(client hooks.Client) (agentskills.Client, error) {
	switch client {
	case hooks.ClientClaudeCode:
		return agentskills.ClientClaude, nil
	case hooks.ClientCodex:
		return agentskills.ClientCodex, nil
	default:
		return "", fmt.Errorf("invalid skill client %q: valid values are claude, codex", client)
	}
}

func printSkillAction(label, name, root string, action agentskills.Action, skipEnv string) {
	dst := tildePath(filepath.Join(root, name))
	switch action {
	case agentskills.ActionInstalled:
		fieldRow(label, green("installed")+dim("  · "+dst))
	case agentskills.ActionUpdated:
		fieldRow(label, green("updated")+dim("  · "+dst))
	case agentskills.ActionAlreadyDelivered:
		fieldRow(label, dim("already used; one-shot skill not reinstalled"))
	case agentskills.ActionSkipped:
		fieldRow(label, dim("skipped ("+skipEnv+")"))
	default:
		fieldRow(label, yellow("unknown install result "+string(action)))
	}
}

// runInstallSummary prints the final "Seamless" block that closes `make install`:
// the build version and where the binaries and config landed. It is invoked by
// the Makefile (which owns those paths), not typed by a user, so it stays out of
// usage(). Missing flags simply drop their row, so it degrades cleanly.
func runInstallSummary(args []string) error {
	fs := flag.NewFlagSet("install-summary", flag.ContinueOnError)
	binDir := fs.String("bin-dir", "", "directory the binaries were installed into")
	configPath := fs.String("config", "", "path to the active seamless.yaml")
	bins := fs.String("bins", "seamlessd,seam", "comma-separated installed binary filenames")
	if err := fs.Parse(args); err != nil {
		return err
	}
	printInstallSummary(*binDir, *configPath, splitBins(*bins))
	return nil
}

// printInstallSummary renders the install-complete block in the same styled
// shape as the per-client blocks above it: a bold header and aligned label rows.
func printInstallSummary(binDir, configPath string, bins []string) {
	fmt.Printf("\n%s\n", bold("Seamless"))
	fieldRow("version", buildVersion())
	if binDir != "" {
		names := strings.Join(bins, ", ")
		if len(bins) > 1 {
			names = "{" + names + "}"
		}
		fieldRow("bin", tildePath(binDir)+"/"+names)
	}
	if configPath != "" {
		fieldRow("config", tildePath(configPath))
	}
	// A quiet call-to-action footer: the two doors to "what now?", the actionable
	// anchors lifted out of the dim guidance so the eye lands on them.
	fmt.Printf("\n  %s %s %s %s\n",
		dim("Next:"), green("seam --help"), dim("or"), green("https://thereisnospoon.org/docs"))
}

// splitBins parses the comma-separated --bins list into filenames, dropping
// blanks so a trailing comma or empty value yields no phantom entry.
func splitBins(raw string) []string {
	var out []string
	for b := range strings.SplitSeq(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// parseInstallClients maps the --client flag to the client profiles to install,
// in a stable order (Claude Code before Codex for "all"). "detect" (also the
// meaning of an empty value) resolves to the detected client set via the
// claudeOK/codexOK arguments -- the same selection the curl installer's
// select_agent_client makes. With neither client present, detect is an error,
// never a silent Claude Code default: wiring a client the user does not run
// creates its config directory and makes doctor report a phantom install.
func parseInstallClients(raw string, claudeOK, codexOK bool) ([]hooks.Client, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "detect", "auto":
		def := defaultClientChoice(claudeOK, codexOK)
		if def == "" {
			return nil, errors.New("neither Claude Code nor Codex was detected on this machine; pass --client claude|codex|all to install anyway")
		}
		clients, _ := clientsForChoice(def)
		return clients, nil
	case "claude", "claude-code", "cc":
		return []hooks.Client{hooks.ClientClaudeCode}, nil
	case "codex", "cx":
		return []hooks.Client{hooks.ClientCodex}, nil
	case "all", "both":
		return []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}, nil
	default:
		return nil, fmt.Errorf("unknown --client %q: valid values are claude, codex, all, detect", raw)
	}
}

// resolveInstallClients decides which client profiles to install for. An
// explicit --client always wins and drives every non-interactive path, so
// scripts, CI, and any piped invocation keep their exact prior behavior. When
// --client was omitted AND stdin is a terminal, it prompts the user to pick
// Claude Code, Codex, or both (annotating each with whether it was detected on
// this machine). When --client was omitted and stdin is not a terminal, it
// falls back to the flag default (detect: the detected client set, an error
// when neither is present) with no prompt, so a redirected or automated run
// never blocks and a codex-only machine still gets the right profile.
func resolveInstallClients(clientFlag string, clientSet bool) ([]hooks.Client, error) {
	claudeOK, codexOK := claudeDetected(), codexDetected()
	if clientSet || !stdinIsTerminal() {
		return parseInstallClients(clientFlag, claudeOK, codexOK)
	}
	return promptInstallClients(os.Stdin, os.Stdout, claudeOK, codexOK)
}

// promptInstallClients asks an interactive user which client(s) to wire up. It
// annotates each option with whether that client was detected, defaults to the
// detected set, re-prompts on an unrecognized answer, and takes the default on
// EOF so a closed stdin cannot loop forever. When neither client was detected
// it first warns and asks whether to install at all (default no), then offers
// the menu with no default -- the user must name the client they are opting
// into, because there is no detected set to fall back on.
func promptInstallClients(in io.Reader, out io.Writer, claudeOK, codexOK bool) ([]hooks.Client, error) {
	reader := bufio.NewReader(in)
	if !claudeOK && !codexOK {
		if err := confirmInstallWithoutClients(reader, out); err != nil {
			return nil, err
		}
	}
	def := defaultClientChoice(claudeOK, codexOK)
	fmt.Fprintln(out, bold("Install Seamless hooks for which agent client?"))
	fmt.Fprintf(out, "  %s Claude Code %s\n", dim("[1]"), detectedColor(claudeOK))
	fmt.Fprintf(out, "  %s Codex %s\n", dim("[2]"), detectedColor(codexOK))
	fmt.Fprintf(out, "  %s Both\n", dim("[3]"))
	for {
		if def == "" {
			fmt.Fprint(out, "Enter 1, 2, or 3: ")
		} else {
			fmt.Fprintf(out, "Enter 1, 2, or 3 [%s]: ", def)
		}
		line, err := reader.ReadString('\n')
		choice := strings.TrimSpace(line)
		if choice == "" {
			choice = def
		}
		if clients, ok := clientsForChoice(choice); ok {
			return clients, nil
		}
		if err != nil {
			// EOF/read error with an unusable answer: take the default rather
			// than loop on a stdin that will never yield more input. With no
			// default there is nothing safe to take, so abort.
			if def == "" {
				return nil, errors.New("no agent client selected")
			}
			clients, _ := clientsForChoice(def)
			return clients, nil
		}
		fmt.Fprintln(out, "  please enter 1, 2, or 3")
	}
}

// confirmInstallWithoutClients gates the interactive install when no agent
// client was detected: it warns and asks for an explicit yes, defaulting to no
// on an empty answer or EOF, so pressing Enter (or a closed stdin) aborts
// rather than wiring a client that is not there.
func confirmInstallWithoutClients(reader *bufio.Reader, out io.Writer) error {
	fmt.Fprintf(out, "%s neither Claude Code nor Codex was detected on this machine\n", yellow("warning:"))
	for {
		fmt.Fprint(out, "Install hooks anyway? [y/N]: ")
		line, err := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return nil
		case "", "n", "no":
			return errors.New("aborted: no agent client detected (answer y, or rerun with --client claude|codex|all)")
		}
		if err != nil {
			return errors.New("aborted: no agent client detected (answer y, or rerun with --client claude|codex|all)")
		}
		fmt.Fprintln(out, "  please answer y or n")
	}
}

// clientsForChoice maps a menu answer (number or client word) to its profiles.
// The second return is false for an unrecognized answer, so the caller can
// re-prompt.
func clientsForChoice(s string) ([]hooks.Client, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "claude", "cc":
		return []hooks.Client{hooks.ClientClaudeCode}, true
	case "2", "codex", "cx":
		return []hooks.Client{hooks.ClientCodex}, true
	case "3", "both", "all":
		return []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}, true
	}
	return nil, false
}

// defaultClientChoice is the menu default: the detected set, empty when
// nothing is detected -- there is deliberately no Claude Code fallback, so
// callers must either ask the user or fail, never silently wire a client
// that is not installed.
func defaultClientChoice(claudeOK, codexOK bool) string {
	switch {
	case claudeOK && codexOK:
		return "3"
	case codexOK:
		return "2"
	case claudeOK:
		return "1"
	default:
		return ""
	}
}

func detectedTag(ok bool) string {
	if ok {
		return "(detected)"
	}
	return "(not detected)"
}

// detectedColor is detectedTag tinted for the interactive menu: green when the
// client is present, dim when it is not.
func detectedColor(ok bool) string {
	if ok {
		return green(detectedTag(ok))
	}
	return dim(detectedTag(ok))
}

// stdinIsTerminal reports whether stdin is an interactive terminal, so the
// installer prompts a human but never blocks (or silently picks a
// detection-based default for) a piped, /dev/null, or otherwise redirected run.
// term.IsTerminal does the real ioctl -- unlike an os.ModeCharDevice heuristic,
// which misreads /dev/null (also a character device) as a terminal.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// claudeDetected reports whether Claude Code appears installed on this machine:
// the `claude` CLI on PATH, or a ~/.claude directory (Claude Code creates it on
// first run). It keeps install/doctor from nagging a user who does not run CC.
// A directory that a prior `install-hooks --client claude` created also counts,
// which is correct: that user opted into the Claude Code profile.
func claudeDetected() bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	if home, err := expandHome("~/.claude"); err == nil {
		if info, statErr := os.Stat(home); statErr == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// codexDetected reports whether Codex appears installed: the `codex` CLI on
// PATH, or a $CODEX_HOME/~/.codex directory. Symmetric with claudeDetected.
func codexDetected() bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	dir := "~/.codex"
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		dir = home
	}
	if path, err := expandHome(dir); err == nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// installClaudeHooks installs the Claude Code hook profile into settings.json and
// (unless doMCP is false) registers the MCP server with the claude CLI.
func installClaudeHooks(cfg config.Config, settings, baseURL, seamBin, configPath string, doMCP bool) error {
	path, err := expandHome(settings)
	if err != nil {
		return err
	}
	res, err := hooks.Install(hooks.InstallOptions{
		Client: hooks.ClientClaudeCode, SettingsPath: path, BaseURL: baseURL,
		APIKey: cfg.MCP.APIKey, SeamBin: seamBin, ConfigPath: configPath,
	})
	if err != nil {
		return err
	}
	printClientBlock(res, "Claude Code", path)
	if doMCP {
		registerClaudeMCP(baseURL, seamBin, configPath)
	}
	return nil
}

// installCodexHooks installs the Codex hook profile into $CODEX_HOME/hooks.json
// and (unless doMCP is false) registers the seam mcp-proxy stdio bridge with the
// codex CLI. It closes by pointing at Codex's hook trust gate, which no config we
// write can satisfy on the user's behalf (see the codex-hook-contract memory).
func installCodexHooks(cfg config.Config, codexHooks, baseURL, seamBin, configPath string, doMCP bool) error {
	target := codexHooks
	if strings.TrimSpace(target) == "" {
		target = defaultCodexHooksPath()
	}
	path, err := expandHome(target)
	if err != nil {
		return err
	}
	res, err := hooks.Install(hooks.InstallOptions{
		Client: hooks.ClientCodex, SettingsPath: path, BaseURL: baseURL,
		APIKey: cfg.MCP.APIKey, SeamBin: seamBin, ConfigPath: configPath,
	})
	if err != nil {
		return err
	}
	printClientBlock(res, "Codex", path)
	if doMCP {
		if err := registerCodexMCP(seamBin, configPath); err != nil {
			return err
		}
	}
	// Codex ignores hooks until the user trusts them; no config we write can do
	// that on their behalf (see the codex-hook-contract memory), so flag it.
	fieldRow("trust", yellow("approve hooks on the next `codex` run"))
	fmt.Printf("%s%s\n", fieldCont, dim("headless: pass --dangerously-bypass-hook-trust"))
	return nil
}

// printClientBlock renders one client's install as a compact, colored block: a
// bold client header, a one-line hook summary (action counts + the settings
// path), the specific hooks that changed (omitted when nothing did), and any
// backup. The caller adds the mcp (and, for Codex, trust) rows beneath it, so
// both client paths report identically.
func printClientBlock(res hooks.InstallResult, client, path string) {
	fmt.Printf("\n%s\n", bold(client))
	summary, changed := summarizeActions(res.Actions)
	fieldRow("hooks", summary+"  "+dim("· "+tildePath(path)))
	for _, line := range changed {
		fmt.Printf("%s%s\n", fieldCont, line)
	}
	if res.BackupPath != "" {
		fieldRow("backup", dim(tildePath(res.BackupPath)))
	}
}

// summarizeActions turns per-hook action strings ("SessionStart: added") into a
// colored count summary ("2 added, 4 unchanged") plus one detail line per action
// that changed something ("added: SessionStart, PostToolUse"). Unchanged hooks
// are counted but never enumerated -- they are the boring, scannable majority.
func summarizeActions(actions []string) (summary string, changed []string) {
	// Stable display order; unchanged trails so the eye lands on changes first.
	order := []string{"added", "updated", "adopted", "deduped", "unchanged"}
	counts := map[string]int{}
	events := map[string][]string{}
	for _, a := range actions {
		event, act, ok := strings.Cut(a, ": ")
		if !ok {
			continue
		}
		counts[act]++
		events[act] = append(events[act], event)
	}
	var segs []string
	for _, act := range order {
		if counts[act] == 0 {
			continue
		}
		segs = append(segs, colorAction(act, fmt.Sprintf("%d %s", counts[act], act)))
		if act != "unchanged" {
			changed = append(changed, colorAction(act, act+": ")+strings.Join(events[act], ", "))
		}
	}
	return strings.Join(segs, dim(", ")), changed
}

// colorAction tints an action word by outcome: green for a real change (added /
// updated / adopted), yellow for a dedupe, dim for unchanged.
func colorAction(act, text string) string {
	switch act {
	case "unchanged":
		return dim(text)
	case "deduped":
		return yellow(text)
	default:
		return green(text)
	}
}

// defaultCodexHooksPath is where the Codex installer writes when --codex-hooks is
// omitted: $CODEX_HOME/hooks.json if CODEX_HOME is set (Codex's own override),
// else ~/.codex/hooks.json. The "~" is expanded by the caller (expandHome).
func defaultCodexHooksPath() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "hooks.json")
	}
	return "~/.codex/hooks.json"
}

// claudeHeadersHelper builds the command line Claude Code runs at connect time
// to obtain the Authorization header (see `seam mcp-headers`). --config is baked
// in for the same reason the command hooks and the codex bridge bake it in: the
// client records this line with no environment, so it must resolve config from
// any cwd on its own.
func claudeHeadersHelper(seamBin, configPath string) string {
	helper := seamBin + " mcp-headers"
	if configPath != "" {
		helper += " --config " + configPath
	}
	return helper
}

// claudeMCPAddArgs builds the claude CLI argv that registers the Seamless MCP
// server.
//
// It registers via `mcp add-json` with a headersHelper rather than `mcp add
// --header "Authorization: Bearer <key>"`, because that form put the daemon's
// sole credential into this subprocess's argv, readable by any local account
// via `ps auxww` for as long as the call ran (audit L4). Nothing in this argv
// is secret now: it names a command, and the key is read from the 0600 config
// by that command at connection time. This is the same trade the Codex
// registration already makes with the mcp-proxy bridge.
//
// --scope user is deliberate: the default local scope ties the registration to
// the directory it ran from, and the tools then vanish in every other repo.
func claudeMCPAddArgs(baseURL, seamBin, configPath string) []string {
	spec := map[string]any{
		"type":          "http",
		"url":           baseURL + "/api/mcp",
		"headersHelper": claudeHeadersHelper(seamBin, configPath),
	}
	// A map with fixed keys and string values cannot fail to marshal.
	blob, _ := json.Marshal(spec) //nolint:errcheck // static map of strings; marshal cannot fail
	return []string{"mcp", "add-json", "--scope", "user", "seamless", string(blob)}
}

// registerClaudeMCP registers /api/mcp with the Claude Code CLI so hooks and
// MCP tools land in one command. Best-effort by design: the hooks are already
// installed at this point, so a missing or failing claude CLI degrades to
// printing the manual command rather than failing the install.
func registerClaudeMCP(baseURL, seamBin, configPath string) {
	args := claudeMCPAddArgs(baseURL, seamBin, configPath)
	manual := "claude " + strings.Join(args, " ")
	claude, err := exec.LookPath("claude")
	if err != nil {
		fieldRow("mcp", yellow("claude CLI not found"))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	runner := execMCPCommandRunner{client: "claude", path: claude, timeout: mcpCommandTimeout}
	ctx := context.Background()

	// Claude Code 2.1.215 exposes no JSON flag for mcp get/list. Do not turn its
	// human health report into a second brittle state parser: retain the narrow
	// stored-Authorization migration below, then verify existence after add.
	// Codex's machine-readable surface is reconciled exactly in
	// reconcileCodexMCP.
	// An existing registration is normally left alone -- except the one shape
	// this change exists to retire. Installs predating the headersHelper switch
	// stored the bearer key as a literal header in ~/.claude.json, and a plain
	// "already registered" would leave it there forever, so the fix would only
	// ever reach new users. Re-register those in place.
	upgrade := false
	if out, gerr := runner.Run(ctx, "mcp", "get", seamlessMCPName); gerr == nil {
		if !strings.Contains(string(out), "Authorization") {
			fieldRow("mcp", dim("already registered"))
			return
		}
		upgrade = true
		// add-json refuses an existing name, so the old entry goes first. If the
		// add below then fails, the manual command is printed -- which is why
		// this is not attempted unless the claude CLI is known to work.
		if out, rerr := runner.Run(ctx, "mcp", "remove", "--scope", "user", seamlessMCPName); rerr != nil {
			fieldRow("mcp", yellow("could not replace the stored-key registration"))
			fmt.Printf("%s%s\n", fieldCont, dim(strings.TrimSpace(string(out))))
			fmt.Printf("%s%s\n", fieldCont, dim(manual))
			return
		}
	} else if errors.Is(gerr, context.DeadlineExceeded) || errors.Is(gerr, context.Canceled) {
		fieldRow("mcp", yellow("registration check timed out"))
		fmt.Printf("%s%s\n", fieldCont, dim(gerr.Error()))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}

	if out, aerr := runner.Run(ctx, args...); aerr != nil {
		fieldRow("mcp", yellow("registration failed"))
		fmt.Printf("%s%s\n", fieldCont, dim(strings.TrimSpace(string(out))))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	if _, verr := runner.Run(ctx, "mcp", "get", seamlessMCPName); verr != nil {
		fieldRow("mcp", yellow("registration could not be verified"))
		fmt.Printf("%s%s\n", fieldCont, dim(verr.Error()))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	if upgrade {
		fieldRow("mcp", green("re-registered")+dim(" (--scope user; bearer key moved out of ~/.claude.json)"))
		return
	}
	fieldRow("mcp", green("registered")+dim(" (--scope user, key via headersHelper)"))
}

// codexMCPAddArgs builds the codex CLI argv that registers the Seamless MCP
// bridge as a stdio server (design decision D6): `codex mcp add seamless --
// <seam> mcp-proxy --config <yaml>`. The bridge reads the bearer key from
// --config at runtime, so -- unlike a streamable-HTTP registration -- no secret
// is duplicated into ~/.codex/config.toml. --config is baked in because codex
// records the argv with no environment (same reason the command hooks carry it).
func codexMCPAddArgs(seamBin, configPath string) []string {
	args := []string{"mcp", "add", "seamless", "--", seamBin, "mcp-proxy"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	return args
}

// absConfigPath makes the loaded config file absolute so it can be baked into
// the SessionStart command hook as SEAMLESS_CONFIG (the hook fires from any cwd,
// where a relative "seamless.yaml" would not resolve). "" (defaults+env, no
// file) stays "" so no SEAMLESS_CONFIG is emitted.
func absConfigPath(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	if abs, err := filepath.Abs(src); err == nil {
		return abs
	}
	return src
}

// resolveSeamBin picks the seam CLI path baked into the SessionStart command
// hook. An explicit --seam wins and is made absolute; otherwise it prefers the
// seam binary sitting next to this seamlessd (the normal `make build` layout),
// then resolves PATH. The final bare-name fallback is only for the preflight
// warning/manual-repair path when seam is not installed. The name carries .exe
// on Windows: exec-form command hooks require the real filename.
func resolveSeamBin(override string) string {
	if candidate := strings.TrimSpace(override); candidate != "" {
		if expanded, err := expandHome(candidate); err == nil {
			candidate = expanded
		}
		if !strings.ContainsAny(candidate, `/\`) {
			if found, err := exec.LookPath(candidate); err == nil {
				candidate = found
			}
		}
		if abs, err := filepath.Abs(candidate); err == nil {
			return abs
		}
		return candidate
	}
	name := seamBinName()
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		if abs, absErr := filepath.Abs(found); absErr == nil {
			return abs
		}
		return found
	}
	return name
}

// seamBinName is the seam CLI's filename for the OS install-hooks runs on --
// which is also the OS the hooks will fire on, so runtime.GOOS is correct.
func seamBinName() string {
	if runtime.GOOS == "windows" {
		return "seam.exe"
	}
	return "seam"
}

// hookBaseURL turns a bind address into a reachable base URL, mapping a
// bind-all host to loopback.
func hookBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8081"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
