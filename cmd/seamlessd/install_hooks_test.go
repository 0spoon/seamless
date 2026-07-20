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

func TestCodexAppMCPSetupHint(t *testing.T) {
	hint := codexAppMCPSetupHint("/opt/seam", "/etc/seamless.yaml")
	require.Contains(t, hint, "Settings > MCP servers > Add server > STDIO")
	require.Contains(t, hint, `name "seamless"`)
	require.Contains(t, hint, `command "/opt/seam"`)
	require.Contains(t, hint, `arguments "mcp-proxy" "--config" "/etc/seamless.yaml"`)
	require.Contains(t, hint, "Save, then Restart")
	require.NotContains(t, hint, "Bearer")
	require.NotContains(t, hint, "api_key")
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
	cc := []hooks.Client{hooks.ClientClaudeCode}
	cx := []hooks.Client{hooks.ClientCodex}
	both := []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}

	for _, tt := range []struct {
		raw      string
		claudeOK bool
		codexOK  bool
		want     []hooks.Client
	}{
		// Explicit values ignore detection entirely.
		{"claude", false, true, cc},
		{"CC", false, true, cc},
		{"codex", true, false, cx},
		{"all", false, false, both},
		// detect (and its empty/auto spellings) follows the machine, matching
		// the curl installer's select_agent_client.
		{"detect", true, true, both},
		{"detect", false, true, cx},
		{"detect", true, false, cc},
		{"", false, true, cx},
		{"auto", false, true, cx},
	} {
		got, err := parseInstallClients(tt.raw, tt.claudeOK, tt.codexOK)
		require.NoError(t, err, "raw %q", tt.raw)
		require.Equal(t, tt.want, got, "raw %q claude=%v codex=%v", tt.raw, tt.claudeOK, tt.codexOK)
	}

	// detect with neither client present errors instead of silently wiring
	// Claude Code -- explicit values remain the only way to force an install.
	for _, raw := range []string{"detect", "", "auto"} {
		_, err := parseInstallClients(raw, false, false)
		require.ErrorContains(t, err, "neither Claude Code nor Codex", "raw %q", raw)
		require.ErrorContains(t, err, "--client", "raw %q", raw)
	}

	_, err := parseInstallClients("gemini", true, true)
	require.ErrorContains(t, err, "unknown --client")
	require.ErrorContains(t, err, "detect")
}

func TestDefaultClientChoice(t *testing.T) {
	require.Equal(t, "", defaultClientChoice(false, false)) // nothing detected -> no default
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
		{"pick both", "3\n", true, true, both},
		{"word alias", "codex\n", false, true, cx},
		{"empty takes default codex", "\n", false, true, cx},
		{"empty takes default both", "\n", true, true, both},
		{"reprompt then valid", "9\nboth\n", true, true, both},
		{"eof falls back to default", "", false, true, cx},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := promptInstallClients(strings.NewReader(tt.input), &out, tt.claudeOK, tt.codexOK)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			// The menu always renders all three options with a detection tag.
			require.Contains(t, out.String(), "[1] Claude Code")
			require.Contains(t, out.String(), "[2] Codex (app/CLI/IDE)")
		})
	}
}

func TestCodexSharedHostLabelStaysInInstallerMenus(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "docs", "install"),
		filepath.Join("..", "..", "docs", "install.ps1"),
	} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(raw), "[2] Codex (app/CLI/IDE)", path)
	}
}

func TestPromptInstallClients_NothingDetected(t *testing.T) {
	cx := []hooks.Client{hooks.ClientCodex}
	both := []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}

	// An explicit yes reaches the client menu, which then has no default: the
	// user must name the client they are opting into.
	for _, tt := range []struct {
		name  string
		input string
		want  []hooks.Client
	}{
		{"yes then codex", "y\n2\n", cx},
		{"yes then both", "yes\n3\n", both},
		{"yes reprompt then valid", "y\n\nboth\n", both},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := promptInstallClients(strings.NewReader(tt.input), &out, false, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Contains(t, out.String(), "neither Claude Code nor Codex")
		})
	}

	// The confirm gate defaults to no: empty answer, an explicit no, and EOF
	// all abort before any menu is shown.
	for _, tt := range []struct {
		name  string
		input string
	}{
		{"empty answer aborts", "\n"},
		{"explicit no aborts", "n\n"},
		{"eof aborts", ""},
		{"garbage then eof aborts", "maybe\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			_, err := promptInstallClients(strings.NewReader(tt.input), &out, false, false)
			require.ErrorContains(t, err, "aborted: no agent client detected")
		})
	}

	// Yes at the gate but EOF at the menu still aborts: with nothing detected
	// there is no default selection to fall back on.
	var out strings.Builder
	_, err := promptInstallClients(strings.NewReader("y\n"), &out, false, false)
	require.ErrorContains(t, err, "no agent client selected")
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
