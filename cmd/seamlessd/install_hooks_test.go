package main

import (
	"strings"
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

func TestDefaultClientChoice(t *testing.T) {
	require.Equal(t, "1", defaultClientChoice(false, false)) // nothing detected -> historical default
	require.Equal(t, "1", defaultClientChoice(true, false))
	require.Equal(t, "2", defaultClientChoice(false, true)) // codex-only machine defaults to codex
	require.Equal(t, "3", defaultClientChoice(true, true))  // both detected -> both
}

func TestPromptInstallClients(t *testing.T) {
	cc := []hooks.Client{hooks.ClientClaudeCode}
	cx := []hooks.Client{hooks.ClientCodex}
	both := []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}

	for _, tt := range []struct {
		name     string
		input    string
		claudeOK bool
		codexOK  bool
		want     []hooks.Client
	}{
		{"pick claude", "1\n", true, false, cc},
		{"pick codex", "2\n", true, true, cx},
		{"pick both", "3\n", false, false, both},
		{"word alias", "codex\n", false, true, cx},
		{"empty takes default codex", "\n", false, true, cx},
		{"empty takes default claude", "\n", false, false, cc},
		{"reprompt then valid", "9\nboth\n", false, false, both},
		{"eof falls back to default", "", false, true, cx},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := promptInstallClients(strings.NewReader(tt.input), &out, tt.claudeOK, tt.codexOK)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			// The menu always renders all three options with a detection tag.
			require.Contains(t, out.String(), "[1] Claude Code")
			require.Contains(t, out.String(), "[2] Codex")
		})
	}
}

func TestDetectedTag(t *testing.T) {
	require.Equal(t, "(detected)", detectedTag(true))
	require.Equal(t, "(not detected)", detectedTag(false))
}
