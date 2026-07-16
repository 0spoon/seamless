package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
)

// runInstallHooks wires Claude Code to Seamless in one command: it installs the
// hooks into a settings.json (default ~/.claude/settings.json) and, unless
// --mcp=false, registers the MCP server with the claude CLI. For the P2
// dogfood, point --settings at THIS repo's project-scoped .claude/settings.json
// so v2 hooks fire only here.
func runInstallHooks(args []string) error {
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	settings := fs.String("settings", "~/.claude/settings.json", "settings.json to install into")
	urlFlag := fs.String("url", "", "base URL of seamlessd (default derived from config addr)")
	seamFlag := fs.String("seam", "", "path to the seam CLI for command hooks (default: sibling of this binary, else PATH)")
	mcpFlag := fs.Bool("mcp", true, "register the MCP server with the claude CLI (claude mcp add --scope user)")
	if err := fs.Parse(args); err != nil {
		return err
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
		fmt.Printf("first run: generated mcp.api_key into %s\n", keyPath)
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
	path, err := expandHome(*settings)
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}
	seamBin := resolveSeamBin(*seamFlag)
	if _, lookErr := exec.LookPath(seamBin); lookErr != nil {
		fmt.Printf("warning: seam CLI not found (%q); the command hooks will fail until it is installed:\n  go install github.com/0spoon/seamless/cmd/seam@latest\n", seamBin)
	}
	configPath := absConfigPath(cfg.SourcePath())

	res, err := hooks.Install(hooks.InstallOptions{
		SettingsPath: path, BaseURL: baseURL, APIKey: cfg.MCP.APIKey,
		SeamBin: seamBin, ConfigPath: configPath,
	})
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}
	for _, a := range res.Actions {
		fmt.Printf("  %s\n", a)
	}
	if res.BackupPath != "" {
		fmt.Printf("backed up original to %s\n", res.BackupPath)
	}
	if res.Changed {
		fmt.Printf("installed Seamless hooks into %s (url %s)\n", path, baseURL)
	} else {
		fmt.Printf("Seamless hooks already up to date in %s\n", path)
	}
	if *mcpFlag {
		registerClaudeMCP(baseURL, cfg.MCP.APIKey)
	}
	return nil
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
		fmt.Printf("claude CLI not found; register the MCP endpoint with your client yourself:\n  %s\n", manual)
		return
	}
	if exec.Command(claude, "mcp", "get", "seamless").Run() == nil {
		fmt.Println("MCP server \"seamless\" already registered with claude; if the key or URL changed, run: claude mcp remove seamless --scope user, then rerun this command")
		return
	}
	if out, aerr := exec.Command(claude, claudeMCPAddArgs(baseURL, key)...).CombinedOutput(); aerr != nil {
		fmt.Printf("claude mcp add failed (%v): %s\nregister it yourself:\n  %s\n", aerr, strings.TrimSpace(string(out)), manual)
		return
	}
	fmt.Printf("registered MCP server \"seamless\" with claude (--scope user, %s/api/mcp)\n", baseURL)
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
// regardless of PATH, falling back to a bare "seam" resolved at hook time.
func resolveSeamBin(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "seam")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
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
