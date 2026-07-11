package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
)

// runInstallHooks installs the Seamless SessionStart/UserPromptSubmit hooks into
// a Claude Code settings.json (default ~/.claude/settings.json). For the P2
// dogfood, point --settings at THIS repo's project-scoped .claude/settings.json
// so v2 hooks fire only here.
func runInstallHooks(args []string) error {
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	settings := fs.String("settings", "~/.claude/settings.json", "settings.json to install into")
	urlFlag := fs.String("url", "", "base URL of seamlessd (default derived from config addr)")
	seamFlag := fs.String("seam", "", "path to the seam CLI for command hooks (default: sibling of this binary, else PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("seamlessd.install-hooks: %w", err)
	}
	if strings.TrimSpace(cfg.MCP.APIKey) == "" {
		return fmt.Errorf("seamlessd.install-hooks: mcp.api_key is empty; set it in seamless.yaml (openssl rand -hex 32)")
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
	return nil
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
