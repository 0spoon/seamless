package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const macOSCodexAppRuntime = "/Applications/Codex.app/Contents/Resources/codex"

type codexRuntimeCandidate struct {
	name string
	path string
}

type codexVersionReader func(context.Context, string) (string, error)

// codexRuntimeChecks reports each locally discoverable Codex runtime
// independently. The PATH CLI and desktop app can bundle different versions,
// so collapsing them into one value would hide the compatibility boundary the
// operator is trying to diagnose.
func codexRuntimeChecks() []check {
	candidates := discoverCodexRuntimes()
	checks := make([]check, 0, len(candidates))
	for _, candidate := range candidates {
		checks = append(checks, codexRuntimeCheck(candidate, readCodexVersion))
	}
	return checks
}

func discoverCodexRuntimes() []codexRuntimeCandidate {
	var candidates []codexRuntimeCandidate
	if path, err := exec.LookPath("codex"); err == nil {
		candidates = append(candidates, codexRuntimeCandidate{
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
			candidates = append(candidates, codexRuntimeCandidate{
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

func codexRuntimeCheck(candidate codexRuntimeCandidate, readVersion codexVersionReader) check {
	ctx, cancel := context.WithTimeout(context.Background(), mcpCommandTimeout)
	defer cancel()

	version, err := readVersion(ctx, candidate.path)
	if err != nil {
		return check{statusWarn, candidate.name,
			fmt.Sprintf("cannot run %q --version: %v", candidate.path, err)}
	}
	return check{statusOK, candidate.name, fmt.Sprintf("%s (%s)", version, candidate.path)}
}

func readCodexVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
				return "", fmt.Errorf("%w: %s", err, detail)
			}
		}
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", errors.New("empty version output")
	}
	return version, nil
}
