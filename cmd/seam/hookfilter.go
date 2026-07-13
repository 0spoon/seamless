package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// shouldForwardPostToolUse is the local pre-filter for the machine-wide
// PostToolUse hook: forward only what plan capture can care about -- an
// ExitPlanMode approval, or a Write/Edit/MultiEdit touching a file directly
// under the Claude Code plans dir. Everything else exits before any config or
// network I/O, keeping the hot path to a process spawn. The daemon
// re-validates the path (defense in depth); this is only a cheap gate.
func shouldForwardPostToolUse(payload []byte, plansDir string) bool {
	var p struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return false
	}
	switch p.ToolName {
	case "ExitPlanMode":
		return true
	case "Write", "Edit", "MultiEdit":
		return underDir(p.ToolInput.FilePath, plansDir)
	}
	return false
}

// defaultPlansDir is where Claude Code plan mode writes its plan files.
func defaultPlansDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plans")
}

// underDir reports whether path (a ~/ prefix expands) is directly inside dir.
func underDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	if len(path) > 1 && path[0] == '~' && path[1] == '/' {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Dir(filepath.Clean(path)) == filepath.Clean(dir)
}
