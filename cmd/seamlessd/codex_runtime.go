package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const macOSCodexAppRuntime = "/Applications/Codex.app/Contents/Resources/codex"

// codexRuntimeChecks reports each locally discoverable Codex runtime
// independently. The PATH CLI and desktop app can bundle different versions,
// so collapsing them into one value would hide the compatibility boundary the
// operator is trying to diagnose.
func codexRuntimeChecks() []check {
	candidates := discoverCodexRuntimes()
	checks := make([]check, 0, len(candidates))
	for _, candidate := range candidates {
		checks = append(checks, runtimeVersionCheck(candidate, readRuntimeVersion))
	}
	return checks
}

func discoverCodexRuntimes() []runtimeCandidate {
	var candidates []runtimeCandidate
	if path, err := exec.LookPath("codex"); err == nil {
		candidates = append(candidates, runtimeCandidate{
			name: "codex CLI runtime",
			path: path,
		})
	}

	// The current public Codex manual documents this retained compatibility
	// binary on macOS. Do not guess a Windows package path. Also skip it when
	// CODEX_HOME selects a different host: the GUI app normally uses the default
	// home, so presenting its runtime beside a custom-home CLI would imply shared
	// state we have not established.
	if path := codexAppRuntimePath(); path != "" {
		if info, err := os.Lstat(path); err == nil && !info.IsDir() {
			candidates = append(candidates, runtimeCandidate{
				name: "codex app runtime",
				path: path,
			})
		}
	}
	return candidates
}

func codexAppRuntimePath() string {
	if runtime.GOOS != "darwin" || !codexAppSharesSelectedHome() {
		return ""
	}
	return macOSCodexAppRuntime
}

func codexAppSharesSelectedHome() bool {
	selected := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if selected == "" {
		return true
	}
	selected, err := expandHome(selected)
	if err != nil {
		return false
	}
	defaultHome, err := expandHome("~/.codex")
	if err != nil {
		return false
	}
	return filepath.Clean(selected) == filepath.Clean(defaultHome)
}
