package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldForwardPostToolUse(t *testing.T) {
	plans := filepath.Join("/Users", "x", ".claude", "plans")
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"exit-plan-mode", `{"tool_name":"ExitPlanMode","tool_input":{}}`, true},
		{"write-plan-file", `{"tool_name":"Write","tool_input":{"file_path":"/Users/x/.claude/plans/foo.md"}}`, true},
		{"edit-plan-file", `{"tool_name":"Edit","tool_input":{"file_path":"/Users/x/.claude/plans/foo.md"}}`, true},
		{"multiedit-plan-file", `{"tool_name":"MultiEdit","tool_input":{"file_path":"/Users/x/.claude/plans/foo.md"}}`, true},
		{"write-elsewhere", `{"tool_name":"Write","tool_input":{"file_path":"/work/demo/main.go"}}`, false},
		{"write-nested-under-plans", `{"tool_name":"Write","tool_input":{"file_path":"/Users/x/.claude/plans/sub/foo.md"}}`, false},
		{"write-traversal", `{"tool_name":"Write","tool_input":{"file_path":"/Users/x/.claude/plans/../secrets.md"}}`, false},
		{"write-no-path", `{"tool_name":"Write","tool_input":{}}`, false},
		{"other-tool", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, false},
		{"malformed", `{not json`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldForwardPostToolUse([]byte(tt.payload), plans))
		})
	}
}

func TestUnderDirTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.True(t, underDir("~/.claude/plans/foo.md", filepath.Join(home, ".claude", "plans")))
	require.False(t, underDir("~/.claude/other/foo.md", filepath.Join(home, ".claude", "plans")))
	require.False(t, underDir("", filepath.Join(home, ".claude", "plans")))
	require.False(t, underDir("~/.claude/plans/foo.md", ""))
}
