package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/hooks"
	agentskills "github.com/0spoon/seamless/internal/skills"
)

// The skills step is an optional convenience layer: an unwritable skill root
// must cost a warning, not the daemon bootstrap (install-hooks runs from
// `curl | sh` under `set -eu`), and must not stop a later client in the
// --client all loop.
func TestRunInstallHooks_SkillFailureDegradesAndContinues(t *testing.T) {
	home := t.TempDir()
	codexHome := t.TempDir()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "seamless.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("mcp:\n  api_key: \"test-key\"\n"), 0o600))
	t.Setenv("SEAMLESS_CONFIG", cfgPath)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	// An unwritable Claude skill root: ~/.claude/skills is a regular file, so
	// creating skill directories under it fails for the first client wired.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "skills"), nil, 0o644))

	settings := filepath.Join(tmp, "settings.json")
	codexHooks := filepath.Join(tmp, "hooks.json")
	err := runInstallHooks([]string{
		"--client", "all", "--settings", settings, "--codex-hooks", codexHooks,
		"--mcp=false", "--seam", "/opt/seam", "--url", "http://127.0.0.1:8081",
	})
	require.NoError(t, err, "a failed skills step must degrade to a warning, not abort")

	// Both clients' hooks landed: the claude skills failure did not stop the loop.
	require.FileExists(t, settings)
	require.FileExists(t, codexHooks)
	// The later client's skills still installed; the failed root stayed a file.
	require.FileExists(t, filepath.Join(codexHome, "skills", agentskills.OnboardName, "SKILL.md"))
	require.FileExists(t, filepath.Join(codexHome, "skills", agentskills.ResearchName, "SKILL.md"))
	info, statErr := os.Stat(filepath.Join(home, ".claude", "skills"))
	require.NoError(t, statErr)
	require.False(t, info.IsDir())
}

// The Claude Code MCP registration names a headersHelper command instead of
// carrying the bearer key: the key would otherwise sit in this argv, readable
// via `ps auxww` during install (audit L4). Same trade as the Codex bridge
// below -- the key is read from the 0600 config at connection time.
func TestClaudeMCPAddArgs(t *testing.T) {
	args := claudeMCPAddArgs("http://127.0.0.1:8081", "/opt/seam", "/etc/seamless.yaml")
	require.Equal(t, []string{
		"mcp", "add-json", "--scope", "user", "seamless",
		`{"headersHelper":"/opt/seam mcp-headers --config /etc/seamless.yaml",` +
			`"type":"http","url":"http://127.0.0.1:8081/api/mcp"}`,
	}, args)

	// No config path -> no trailing --config, matching codexMCPAddArgs.
	require.Contains(t, claudeMCPAddArgs("http://127.0.0.1:8081", "/opt/seam", "")[5],
		`"headersHelper":"/opt/seam mcp-headers"`)
}

// The whole point of L4: no argv this installer builds may contain the key.
func TestClaudeMCPAddArgs_CarriesNoSecret(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	joined := strings.Join(claudeMCPAddArgs("http://127.0.0.1:8081", "/opt/seam", "/etc/seamless.yaml"), " ")
	require.NotContains(t, joined, key)
	require.NotContains(t, joined, "Bearer")
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

// summarizeActions runs with color disabled (stdout is not a tty under go
// test), so counts and detail lines compare as plain text.
func TestSummarizeActions(t *testing.T) {
	summary, changed := summarizeActions([]string{
		"SessionStart: added", "UserPromptSubmit: unchanged", "SessionEnd: added",
		"PostToolUse: deduped", "SubagentStop: unchanged",
	})
	// added precedes deduped precedes unchanged; unchanged is counted, not listed.
	require.Equal(t, "2 added, 1 deduped, 2 unchanged", summary)
	require.Equal(t, []string{
		"added: SessionStart, SessionEnd",
		"deduped: PostToolUse",
	}, changed)

	// All unchanged -> a single dim count, no detail lines.
	summary, changed = summarizeActions([]string{"A: unchanged", "B: unchanged"})
	require.Equal(t, "2 unchanged", summary)
	require.Empty(t, changed)

	// A malformed entry (no ": ") is skipped, not counted.
	summary, _ = summarizeActions([]string{"garbage", "A: added"})
	require.Equal(t, "1 added", summary)
}

func TestSplitBins(t *testing.T) {
	require.Equal(t, []string{"seamlessd", "seam"}, splitBins("seamlessd,seam"))
	require.Equal(t, []string{"seamlessd", "seam"}, splitBins(" seamlessd , seam "))
	require.Equal(t, []string{"seamlessd"}, splitBins("seamlessd,,"))
	require.Empty(t, splitBins(""))
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

func TestAgentSkillClient_FollowsHookSelection(t *testing.T) {
	claude, err := agentSkillClient(hooks.ClientClaudeCode)
	require.NoError(t, err)
	require.Equal(t, agentskills.ClientClaude, claude)
	codex, err := agentSkillClient(hooks.ClientCodex)
	require.NoError(t, err)
	require.Equal(t, agentskills.ClientCodex, codex)
	_, err = agentSkillClient(hooks.Client("gemini"))
	require.ErrorContains(t, err, "valid values are claude, codex")
}
