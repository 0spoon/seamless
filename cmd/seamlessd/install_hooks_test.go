package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/hooks"
)

func TestClaudeMCPAddArgs(t *testing.T) {
	args := claudeMCPAddArgs("http://127.0.0.1:8081", "k3y")
	require.Equal(t, []string{
		"mcp", "add", "--scope", "user", "--transport", "http", "seamless",
		"http://127.0.0.1:8081/api/mcp", "--header", "Authorization: Bearer k3y",
	}, args)
}

// The Codex MCP registration is a stdio bridge (codex mcp add ... -- <cmd>), not
// a streamable-HTTP URL: no bearer key is duplicated into codex config, and the
// bridge loads it from --config at runtime (design decision D6).
func TestCodexMCPAddArgs(t *testing.T) {
	args := codexMCPAddArgs("/opt/seam", "/etc/seamless.yaml")
	require.Equal(t, []string{
		"mcp", "add", "seamless", "--", "/opt/seam", "mcp-proxy", "--config", "/etc/seamless.yaml",
	}, args)
	// No config path -> no trailing --config (matches the command-hook builder).
	require.Equal(t, []string{
		"mcp", "add", "seamless", "--", "/opt/seam", "mcp-proxy",
	}, codexMCPAddArgs("/opt/seam", ""))
}

func TestParseInstallClients(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want []hooks.Client
	}{
		{"", []hooks.Client{hooks.ClientClaudeCode}},
		{"claude", []hooks.Client{hooks.ClientClaudeCode}},
		{"CC", []hooks.Client{hooks.ClientClaudeCode}},
		{"codex", []hooks.Client{hooks.ClientCodex}},
		{"all", []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}},
	} {
		got, err := parseInstallClients(tt.raw)
		require.NoError(t, err, "raw %q", tt.raw)
		require.Equal(t, tt.want, got, "raw %q", tt.raw)
	}

	_, err := parseInstallClients("gemini")
	require.ErrorContains(t, err, "unknown --client")
}
