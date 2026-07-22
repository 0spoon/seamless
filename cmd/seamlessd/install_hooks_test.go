package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
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

func TestInstallCodexHooks_TrustGuidanceOnlyWhenDefinitionsChange(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.MCP.APIKey = "test-key"
	hooksPath := filepath.Join(dir, "hooks.json")

	first := captureStdout(t, func() error {
		return installCodexHooks(cfg, hooksPath, "http://127.0.0.1:8081",
			filepath.Join(dir, "seam"), filepath.Join(dir, "seamless.yaml"), false)
	})
	require.Contains(t, first, "5 added")
	require.Contains(t, first, "trust")
	require.Contains(t, first, "unverified")
	require.Contains(t, first, "inspect and approve the current definitions with /hooks")

	second := captureStdout(t, func() error {
		return installCodexHooks(cfg, hooksPath, "http://127.0.0.1:8081",
			filepath.Join(dir, "seam"), filepath.Join(dir, "seamless.yaml"), false)
	})
	require.Contains(t, second, "5 unchanged")
	require.NotContains(t, second, "trust")
	require.NotContains(t, second, "inspect and approve the current definitions with /hooks")
	require.NotContains(t, second, "<seam-briefing>")
}

func TestInstallClientSkills_ReportsUnchangedOnIdenticalReinstall(t *testing.T) {
	opts := agentskills.Options{HomeDir: t.TempDir()}

	first := captureStdout(t, func() error {
		return installClientSkills(hooks.ClientClaudeCode, opts)
	})
	require.Contains(t, first, "onboard  installed")
	require.Contains(t, first, "research  installed")

	second := captureStdout(t, func() error {
		return installClientSkills(hooks.ClientClaudeCode, opts)
	})
	require.Contains(t, second, "onboard  unchanged")
	require.Contains(t, second, "research  unchanged")
	require.NotContains(t, second, "updated")
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	runErr := fn()
	os.Stdout = original
	require.NoError(t, w.Close())
	require.NoError(t, runErr)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return string(raw)
}

func TestSplitBins(t *testing.T) {
	require.Equal(t, []string{"seamlessd", "seam"}, splitBins("seamlessd,seam"))
	require.Equal(t, []string{"seamlessd", "seam"}, splitBins(" seamlessd , seam "))
	require.Equal(t, []string{"seamlessd"}, splitBins("seamlessd,,"))
	require.Empty(t, splitBins(""))
}

// Selection fixtures: a macOS/Windows-shaped machine (desktop supported) and a
// Linux-shaped one (no Claude app build exists there).
func detTargets(claude, codex, desktop bool) detectedTargets {
	return detectedTargets{claude: claude, codex: codex, desktop: desktop, desktopSupported: true}
}

func detTargetsNoDesktop(claude, codex bool) detectedTargets {
	return detectedTargets{claude: claude, codex: codex}
}

func TestParseInstallTargets(t *testing.T) {
	cc := []installTarget{targetClaudeCode}
	cx := []installTarget{targetCodex}
	dt := []installTarget{targetClaudeDesktop}
	hookPair := []installTarget{targetClaudeCode, targetCodex}
	everything := []installTarget{targetClaudeCode, targetCodex, targetClaudeDesktop}

	for _, tt := range []struct {
		raw  string
		det  detectedTargets
		want []installTarget
	}{
		// Explicit values ignore detection entirely.
		{"claude", detTargets(false, true, false), cc},
		{"CC", detTargets(false, true, false), cc},
		{"codex", detTargets(true, false, false), cx},
		{"claude-desktop", detTargets(false, false, false), dt},
		{"desktop", detTargets(false, false, false), dt},
		// Comma lists compose targets; order and duplicates do not matter.
		{"claude,claude-desktop", detTargets(false, false, false), []installTarget{targetClaudeCode, targetClaudeDesktop}},
		{"desktop, codex", detTargets(false, false, false), []installTarget{targetCodex, targetClaudeDesktop}},
		{"codex,claude,codex,", detTargets(false, false, false), hookPair},
		// all is every target the platform can host; both stays the hook pair.
		{"all", detTargets(false, false, false), everything},
		{"all", detTargetsNoDesktop(false, false), hookPair},
		{"both", detTargets(true, true, true), hookPair},
		// Naming the chat surface beside all keeps it even where unsupported
		// (the --desktop-config override case).
		{"all,claude-desktop", detTargetsNoDesktop(false, false), everything},
		// detect (and its empty/auto spellings) follows the machine, matching
		// the curl installer's select_agent_client.
		{"detect", detTargets(true, true, true), everything},
		{"detect", detTargets(false, true, false), cx},
		{"detect", detTargets(false, false, true), dt},
		{"", detTargets(true, false, true), []installTarget{targetClaudeCode, targetClaudeDesktop}},
		{"auto", detTargets(false, true, false), cx},
	} {
		got, err := parseInstallTargets(tt.raw, tt.det)
		require.NoError(t, err, "raw %q", tt.raw)
		require.Equal(t, tt.want, got, "raw %q det=%+v", tt.raw, tt.det)
	}

	// detect with nothing present errors instead of silently wiring Claude
	// Code -- explicit values remain the only way to force an install.
	for _, raw := range []string{"detect", "", "auto"} {
		_, err := parseInstallTargets(raw, detTargets(false, false, false))
		require.ErrorContains(t, err, "no agent client was detected", "raw %q", raw)
		require.ErrorContains(t, err, "--client", "raw %q", raw)
	}

	_, err := parseInstallTargets("gemini", detTargets(true, true, true))
	require.ErrorContains(t, err, "unknown --client")
	require.ErrorContains(t, err, "detect")

	// detect combined with a named target is contradictory, not a union.
	_, err = parseInstallTargets("detect,claude", detTargets(true, true, true))
	require.ErrorContains(t, err, "cannot be combined")

	// Present-but-empty (a bare comma) is uninterpretable, never a default.
	_, err = parseInstallTargets(",", detTargets(true, true, true))
	require.ErrorContains(t, err, "selects no target")
}

func TestHookClientsForAndTargetNames(t *testing.T) {
	everything := []installTarget{targetClaudeCode, targetCodex, targetClaudeDesktop}
	require.Equal(t, []hooks.Client{hooks.ClientClaudeCode, hooks.ClientCodex}, hookClientsFor(everything))
	require.Empty(t, hookClientsFor([]installTarget{targetClaudeDesktop}))
	require.Equal(t, []string{"Claude Code", "Codex", "Claude app (chat)"}, targetNames(everything))
}

func TestDefaultTargetChoice(t *testing.T) {
	require.Equal(t, "", defaultTargetChoice(detTargets(false, false, false))) // nothing detected -> no default
	require.Equal(t, "1", defaultTargetChoice(detTargets(true, false, false)))
	require.Equal(t, "2", defaultTargetChoice(detTargets(false, true, false))) // codex-only machine defaults to codex
	require.Equal(t, "3", defaultTargetChoice(detTargets(false, false, true))) // chat-only machine defaults to the app
	require.Equal(t, "1,2", defaultTargetChoice(detTargets(true, true, false)))
	require.Equal(t, "1,3", defaultTargetChoice(detTargets(true, false, true)))
	require.Equal(t, "4", defaultTargetChoice(detTargets(true, true, true))) // everything detected -> All
	// On a platform without the chat surface, both hook clients ARE everything.
	require.Equal(t, "4", defaultTargetChoice(detTargetsNoDesktop(true, true)))
	require.Equal(t, "1", defaultTargetChoice(detTargetsNoDesktop(true, false)))
}

func TestTargetsForChoice(t *testing.T) {
	det := detTargets(true, true, true)
	for _, tt := range []struct {
		in   string
		want []installTarget
	}{
		{"1", []installTarget{targetClaudeCode}},
		{"3", []installTarget{targetClaudeDesktop}},
		{"1,3", []installTarget{targetClaudeCode, targetClaudeDesktop}},
		{"3, 2", []installTarget{targetCodex, targetClaudeDesktop}},
		{"4", []installTarget{targetClaudeCode, targetCodex, targetClaudeDesktop}},
		{"all", []installTarget{targetClaudeCode, targetCodex, targetClaudeDesktop}},
		{"both", []installTarget{targetClaudeCode, targetCodex}},
		{"desktop", []installTarget{targetClaudeDesktop}},
		{"claude,codex", []installTarget{targetClaudeCode, targetCodex}},
	} {
		got, ok := targetsForChoice(tt.in, det)
		require.True(t, ok, "in %q", tt.in)
		require.Equal(t, tt.want, got, "in %q", tt.in)
	}

	// All on a Linux-shaped machine excludes the surface that cannot exist.
	got, ok := targetsForChoice("4", detTargetsNoDesktop(true, true))
	require.True(t, ok)
	require.Equal(t, []installTarget{targetClaudeCode, targetCodex}, got)

	for _, bad := range []string{"", ",", "9", "1,9", "gemini"} {
		_, ok := targetsForChoice(bad, det)
		require.False(t, ok, "in %q", bad)
	}
}

func TestPromptInstallTargets(t *testing.T) {
	cc := []installTarget{targetClaudeCode}
	cx := []installTarget{targetCodex}
	hookPair := []installTarget{targetClaudeCode, targetCodex}
	everything := []installTarget{targetClaudeCode, targetCodex, targetClaudeDesktop}

	for _, tt := range []struct {
		name  string
		input string
		det   detectedTargets
		want  []installTarget
	}{
		{"pick claude", "1\n", detTargets(true, false, false), cc},
		{"pick codex", "2\n", detTargets(true, true, false), cx},
		{"pick all", "4\n", detTargets(true, true, true), everything},
		{"pick a pair", "1,3\n", detTargets(true, true, true), []installTarget{targetClaudeCode, targetClaudeDesktop}},
		{"word alias", "codex\n", detTargets(false, true, false), cx},
		{"both keeps the hook pair", "both\n", detTargets(true, true, true), hookPair},
		{"empty takes default codex", "\n", detTargets(false, true, false), cx},
		{"empty takes default everything", "\n", detTargets(true, true, true), everything},
		{"empty takes default detected pair", "\n", detTargets(true, false, true), []installTarget{targetClaudeCode, targetClaudeDesktop}},
		{"reprompt then valid", "9\nboth\n", detTargets(true, true, false), hookPair},
		{"eof falls back to default", "", detTargets(false, true, false), cx},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := promptInstallTargets(strings.NewReader(tt.input), &out, tt.det)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			// The menu always renders every target with a detection tag.
			require.Contains(t, out.String(), "[1] Claude Code")
			require.Contains(t, out.String(), "[2] Codex (app/CLI/IDE)")
			require.Contains(t, out.String(), "[3] Claude app (chat)")
			require.Contains(t, out.String(), "[4] All")
		})
	}

	// Where the platform cannot host the chat surface the entry stays visible
	// but says so, instead of the menu silently renumbering itself.
	var out strings.Builder
	_, err := promptInstallTargets(strings.NewReader("1\n"), &out, detTargetsNoDesktop(true, true))
	require.NoError(t, err)
	require.Contains(t, out.String(), "[3] Claude app (chat) (not supported on this OS)")
}

func TestInstallerMenuLabelsStayInSync(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "docs", "install"),
		filepath.Join("..", "..", "docs", "install.ps1"),
	} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(raw), "[2] Codex (app/CLI/IDE)", path)
		require.Contains(t, string(raw), "[3] Claude app (chat)", path)
		require.Contains(t, string(raw), "[4] All", path)
	}
}

func TestPromptInstallTargets_NothingDetected(t *testing.T) {
	cx := []installTarget{targetCodex}
	hookPair := []installTarget{targetClaudeCode, targetCodex}

	// An explicit yes reaches the target menu, which then has no default: the
	// user must name the target they are opting into.
	for _, tt := range []struct {
		name  string
		input string
		want  []installTarget
	}{
		{"yes then codex", "y\n2\n", cx},
		{"yes then desktop", "yes\n3\n", []installTarget{targetClaudeDesktop}},
		{"yes reprompt then valid", "y\n\nboth\n", hookPair},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := promptInstallTargets(strings.NewReader(tt.input), &out, detTargets(false, false, false))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Contains(t, out.String(), "no agent client was detected")
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
			_, err := promptInstallTargets(strings.NewReader(tt.input), &out, detTargets(false, false, false))
			require.ErrorContains(t, err, "aborted: no agent client detected")
		})
	}

	// Yes at the gate but EOF at the menu still aborts: with nothing detected
	// there is no default selection to fall back on.
	var out strings.Builder
	_, err := promptInstallTargets(strings.NewReader("y\n"), &out, detTargets(false, false, false))
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
