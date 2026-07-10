package main

import (
	"flag"
	"fmt"
	"net"
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

	res, err := hooks.Install(hooks.InstallOptions{SettingsPath: path, BaseURL: baseURL, APIKey: cfg.MCP.APIKey})
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
