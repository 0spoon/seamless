package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	macOSClaudeAppBundle     = "/Applications/Claude.app"
	macOSClaudeAppRuntimeDir = "~/Library/Application Support/Claude/claude-code"
)

// claudeRuntimeChecks reports each locally discoverable Claude Code runtime
// independently: the PATH CLI and the desktop app's bundled runtime can carry
// different versions (observed skew: 2.1.216 CLI vs 2.1.215 app), and
// collapsing them would hide exactly the boundary an operator debugging an
// app-only failure needs to see. No discoverable runtime yields no lines --
// symmetric with codexRuntimeChecks, so a machine without Claude Code is not
// nagged about it.
func claudeRuntimeChecks() []check {
	candidates := discoverClaudeRuntimes()
	checks := make([]check, 0, len(candidates))
	for _, candidate := range candidates {
		checks = append(checks, runtimeVersionCheck(candidate, readRuntimeVersion))
	}
	return checks
}

// claudeDesktopAppDetected reports whether the Claude desktop app itself is
// present. Only macOS has a known install location to probe; on Windows the
// desktop config file's existence is the detection signal, so callers must
// treat false as "not proven present", never "proven absent".
func claudeDesktopAppDetected() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	info, err := os.Stat(macOSClaudeAppBundle)
	return err == nil && info.IsDir()
}

func discoverClaudeRuntimes() []runtimeCandidate {
	var candidates []runtimeCandidate
	if path, err := exec.LookPath("claude"); err == nil {
		candidates = append(candidates, runtimeCandidate{
			name: "claude CLI runtime",
			path: path,
		})
	}
	if dir := claudeAppRuntimeDir(); dir != "" {
		candidates = append(candidates, discoverClaudeAppRuntimes(dir)...)
	}
	return candidates
}

// claudeAppRuntimeDir returns the desktop app's bundled-runtime directory, or
// "" when the app is absent, the platform is not macOS (no documented Windows
// layout to guess at), or CLAUDE_CONFIG_DIR selects a different home: the app
// uses the default ~/.claude, so presenting its runtime beside a custom-home
// CLI would imply shared state we have not established.
func claudeAppRuntimeDir() string {
	if runtime.GOOS != "darwin" || !claudeAppSharesSelectedHome() {
		return ""
	}
	if info, err := os.Stat(macOSClaudeAppBundle); err != nil || !info.IsDir() {
		return ""
	}
	dir, err := expandHome(macOSClaudeAppRuntimeDir)
	if err != nil {
		return ""
	}
	return dir
}

func claudeAppSharesSelectedHome() bool {
	selected := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if selected == "" {
		return true
	}
	selected, err := expandHome(selected)
	if err != nil {
		return false
	}
	defaultHome, err := expandHome("~/.claude")
	if err != nil {
		return false
	}
	return filepath.Clean(selected) == filepath.Clean(defaultHome)
}

// discoverClaudeAppRuntimes enumerates the app's retained runtime versions:
// each <version>/ directory under dir holds a signed claude.app bundle whose
// binary self-reports its version. A version directory without the binary
// (partial download, future layout change) is skipped rather than guessed at.
// os.ReadDir sorts entries, so the report order is deterministic.
func discoverClaudeAppRuntimes(dir string) []runtimeCandidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var candidates []runtimeCandidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bin := filepath.Join(dir, entry.Name(), "claude.app", "Contents", "MacOS", "claude")
		if info, err := os.Lstat(bin); err != nil || info.IsDir() {
			continue
		}
		candidates = append(candidates, runtimeCandidate{
			name: "claude app runtime",
			path: bin,
		})
	}
	return candidates
}
