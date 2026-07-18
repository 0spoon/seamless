package main

import (
	"bufio"
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
)

// runInstallHooks wires an agent client to Seamless in one command: it installs
// the hooks into that client's config file and, unless --mcp=false, registers
// the MCP server with the client's CLI. --client selects which: claude (default,
// ~/.claude/settings.json + `claude mcp add`), codex ($CODEX_HOME/hooks.json +
// `codex mcp add ... seam mcp-proxy`), or all (both). The default is unchanged
// Claude Code behavior. For the P2 dogfood, point --settings at THIS repo's
// project-scoped .claude/settings.json so v2 hooks fire only here.
func runInstallHooks(args []string) error {
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	clientFlag := fs.String("client", "claude", "which agent client to install for: claude|codex|all")
	settings := fs.String("settings", "~/.claude/settings.json", "Claude Code settings.json to install into")
	codexHooksFlag := fs.String("codex-hooks", "", "Codex hooks.json to install into (default $CODEX_HOME/hooks.json, else ~/.codex/hooks.json)")
	urlFlag := fs.String("url", "", "base URL of seamlessd (default derived from config addr)")
	seamFlag := fs.String("seam", "", "path to the seam CLI for command hooks (default: sibling of this binary, else PATH)")
	mcpFlag := fs.Bool("mcp", true, "register the MCP server with the client's CLI (claude/codex mcp add)")
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
	}
	return nil
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
// in a stable order (Claude Code before Codex for "all"). An empty value is the
// default Claude Code, so an omitted flag keeps the pre-Codex behavior.
func parseInstallClients(raw string) ([]hooks.Client, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "claude", "claude-code", "cc":
		return []hooks.Client{hooks.ClientClaudeCode}, nil
	case "codex", "cx":
		return []hooks.Client{hooks.ClientCodex}, nil
	case "all", "both":
		return []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}, nil
	default:
		return nil, fmt.Errorf("unknown --client %q: valid values are claude, codex, all", raw)
	}
}

// resolveInstallClients decides which client profiles to install for. An
// explicit --client always wins and drives every non-interactive path, so
// scripts, CI, and any piped invocation keep their exact prior behavior. When
// --client was omitted AND stdin is a terminal, it prompts the user to pick
// Claude Code, Codex, or both (annotating each with whether it was detected on
// this machine). When --client was omitted and stdin is not a terminal, it
// falls back to the flag default (Claude Code) with no prompt, so a redirected
// or automated run never blocks.
func resolveInstallClients(clientFlag string, clientSet bool) ([]hooks.Client, error) {
	if clientSet || !stdinIsTerminal() {
		return parseInstallClients(clientFlag)
	}
	return promptInstallClients(os.Stdin, os.Stdout, claudeDetected(), codexDetected())
}

// promptInstallClients asks an interactive user which client(s) to wire up. It
// annotates each option with whether that client was detected, defaults to the
// detected set (Claude Code when nothing is detected, preserving the historical
// default), re-prompts on an unrecognized answer, and takes the default on EOF
// so a closed stdin cannot loop forever.
func promptInstallClients(in io.Reader, out io.Writer, claudeOK, codexOK bool) ([]hooks.Client, error) {
	def := defaultClientChoice(claudeOK, codexOK)
	fmt.Fprintln(out, bold("Install Seamless hooks for which agent client?"))
	fmt.Fprintf(out, "  %s Claude Code %s\n", dim("[1]"), detectedColor(claudeOK))
	fmt.Fprintf(out, "  %s Codex %s\n", dim("[2]"), detectedColor(codexOK))
	fmt.Fprintf(out, "  %s Both\n", dim("[3]"))
	reader := bufio.NewReader(in)
	for {
		fmt.Fprintf(out, "Enter 1, 2, or 3 [%s]: ", def)
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
			// than loop on a stdin that will never yield more input.
			clients, _ := clientsForChoice(def)
			return clients, nil
		}
		fmt.Fprintln(out, "  please enter 1, 2, or 3")
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

// defaultClientChoice is the menu default: the detected set, falling back to
// Claude Code when nothing is detected so the historical default is unchanged.
func defaultClientChoice(claudeOK, codexOK bool) string {
	switch {
	case claudeOK && codexOK:
		return "3"
	case codexOK && !claudeOK:
		return "2"
	default:
		return "1"
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
		registerClaudeMCP(baseURL, cfg.MCP.APIKey)
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
		registerCodexMCP(seamBin, configPath)
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

// claudeMCPAddArgs builds the claude CLI argv that registers the Seamless MCP
// server. --scope user is deliberate: the default local scope ties the
// registration to the directory it ran from, and the tools then vanish in
// every other repo.
func claudeMCPAddArgs(baseURL, key string) []string {
	return []string{"mcp", "add", "--scope", "user", "--transport", "http", "seamless",
		baseURL + "/api/mcp", "--header", "Authorization: Bearer " + key}
}

// registerClaudeMCP registers /api/mcp with the Claude Code CLI so hooks and
// MCP tools land in one command. Best-effort by design: the hooks are already
// installed at this point, so a missing or failing claude CLI degrades to
// printing the manual command rather than failing the install.
func registerClaudeMCP(baseURL, key string) {
	manual := fmt.Sprintf("claude mcp add --scope user --transport http seamless %s/api/mcp --header \"Authorization: Bearer <mcp.api_key>\"", baseURL)
	claude, err := exec.LookPath("claude")
	if err != nil {
		fieldRow("mcp", yellow("claude CLI not found"))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	if exec.Command(claude, "mcp", "get", "seamless").Run() == nil {
		fieldRow("mcp", dim("already registered"))
		return
	}
	if out, aerr := exec.Command(claude, claudeMCPAddArgs(baseURL, key)...).CombinedOutput(); aerr != nil {
		fieldRow("mcp", yellow("registration failed"))
		fmt.Printf("%s%s\n", fieldCont, dim(strings.TrimSpace(string(out))))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	fieldRow("mcp", green("registered")+dim(" (--scope user)"))
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

// registerCodexMCP registers the seam mcp-proxy stdio bridge with the Codex CLI.
// Best-effort by design, symmetric with registerClaudeMCP: the hooks are already
// installed, so a missing or failing codex CLI degrades to printing the manual
// command rather than failing the install. An already-present entry is left as-is.
func registerCodexMCP(seamBin, configPath string) {
	manual := fmt.Sprintf("codex mcp add seamless -- %s mcp-proxy --config <abs seamless.yaml>", seamBin)
	codex, err := exec.LookPath("codex")
	if err != nil {
		fieldRow("mcp", yellow("codex CLI not found"))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	if exec.Command(codex, "mcp", "get", "seamless").Run() == nil {
		fieldRow("mcp", dim("already registered"))
		return
	}
	if out, aerr := exec.Command(codex, codexMCPAddArgs(seamBin, configPath)...).CombinedOutput(); aerr != nil {
		fieldRow("mcp", yellow("registration failed"))
		fmt.Printf("%s%s\n", fieldCont, dim(strings.TrimSpace(string(out))))
		fmt.Printf("%s%s\n", fieldCont, dim(manual))
		return
	}
	fieldRow("mcp", green("registered")+dim(" (stdio bridge: seam mcp-proxy)"))
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
// hook. An explicit --seam wins; otherwise it prefers the seam binary sitting
// next to this seamlessd (the normal `make build` layout) so the hook works
// regardless of PATH, falling back to the bare binary name resolved at hook time.
// The name carries .exe on Windows: exec-form command hooks spawn the binary
// directly (no PATHEXT resolution of a bare name), and require a real .exe.
func resolveSeamBin(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	name := seamBinName()
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
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
