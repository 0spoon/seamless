package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeDesktopConfigPathFor(t *testing.T) {
	path, err := claudeDesktopConfigPathFor("darwin", "/Users/o", "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/Users/o", "Library", "Application Support", "Claude", "claude_desktop_config.json"), path)

	path, err = claudeDesktopConfigPathFor("windows", `C:\Users\o`, `C:\Users\o\AppData\Roaming`)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(`C:\Users\o\AppData\Roaming`, "Claude", "claude_desktop_config.json"), path)

	_, err = claudeDesktopConfigPathFor("darwin", "", "")
	require.ErrorContains(t, err, "home directory is unknown")
	_, err = claudeDesktopConfigPathFor("windows", `C:\Users\o`, "")
	require.ErrorContains(t, err, "APPDATA")
	// No Linux build of the app exists; an invented path would be a phantom
	// install that doctor then reports forever.
	_, err = claudeDesktopConfigPathFor("linux", "/home/o", "")
	require.ErrorContains(t, err, "no known location on linux")
}

func TestDesiredClaudeDesktopMCPServer(t *testing.T) {
	want, err := desiredClaudeDesktopMCPServer("/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, claudeDesktopMCPServer{
		Command: "/opt/seam",
		Args:    []string{"mcp-proxy", "--config", "/etc/seamless.yaml"},
	}, want)

	// No config path -> no trailing --config, matching codexMCPAddArgs.
	want, err = desiredClaudeDesktopMCPServer("/opt/seam", "")
	require.NoError(t, err)
	require.Equal(t, []string{"mcp-proxy"}, want.Args)

	// The app starts servers with an undefined cwd: relative paths never work.
	_, err = desiredClaudeDesktopMCPServer("seam", "/etc/seamless.yaml")
	require.ErrorContains(t, err, "not absolute")
	_, err = desiredClaudeDesktopMCPServer("/opt/seam", "seamless.yaml")
	require.ErrorContains(t, err, "not absolute")
}

// The registration entry names only paths -- like every other registration
// kind, the bearer key stays in the 0600 config the bridge reads at connect
// time, never in another tool's config file.
func TestDesiredClaudeDesktopMCPServer_CarriesNoSecret(t *testing.T) {
	want, err := desiredClaudeDesktopMCPServer("/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	blob, err := json.Marshal(want)
	require.NoError(t, err)
	require.NotContains(t, string(blob), "Bearer")
	require.NotContains(t, string(blob), "api_key")
	require.NotContains(t, string(blob), "env")
}

func TestParseClaudeDesktopMCPServer(t *testing.T) {
	entry, err := parseClaudeDesktopMCPServer(json.RawMessage(
		`{"command":"/opt/seam","args":["mcp-proxy"],"env":{"K":"v"},"disabled":true}`))
	require.NoError(t, err)
	require.Equal(t, "/opt/seam", entry.Command)
	require.Equal(t, []string{"mcp-proxy"}, entry.Args)
	require.True(t, entry.HasEnv)
	require.True(t, entry.Extra)

	entry, err = parseClaudeDesktopMCPServer(json.RawMessage(`{"command":"/opt/seam"}`))
	require.NoError(t, err)
	require.Empty(t, entry.Args)
	require.False(t, entry.HasEnv)
	require.False(t, entry.Extra)

	for name, raw := range map[string]string{
		"not an object":   `"http://127.0.0.1:8081/api/mcp"`,
		"null":            `null`,
		"missing command": `{"args":["x"]}`,
		"empty command":   `{"command":""}`,
		"command type":    `{"command":42}`,
		"args type":       `{"command":"/opt/seam","args":"mcp-proxy"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseClaudeDesktopMCPServer(json.RawMessage(raw))
			require.Error(t, err)
		})
	}
}

func TestClassifyClaudeDesktopMCP(t *testing.T) {
	want, err := desiredClaudeDesktopMCPServer("/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)

	class, drift := classifyClaudeDesktopMCP(claudeDesktopMCPEntry{
		Command: "/opt/seam", Args: []string{"mcp-proxy", "--config", "/etc/seamless.yaml"},
	}, want)
	require.Equal(t, mcpRegExact, class)
	require.Empty(t, drift)

	// A stale seam path elsewhere on disk is recognizably ours: repairable.
	class, drift = classifyClaudeDesktopMCP(claudeDesktopMCPEntry{
		Command: "/old/bin/seam", Args: []string{"mcp-proxy"}, HasEnv: true,
	}, want)
	require.Equal(t, mcpRegOwnedDrifted, class)
	require.Equal(t, []string{"bridge command differs", "bridge arguments differ", "bridge environment present"}, drift)

	// A foreign server squatting the reserved name is never repaired over.
	class, _ = classifyClaudeDesktopMCP(claudeDesktopMCPEntry{
		Command: "npx", Args: []string{"-y", "other-memory-server"},
	}, want)
	require.Equal(t, mcpRegIncompatible, class)
}

func TestReconcileClaudeDesktopMCP_CreatesFilePreservingNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Claude", "claude_desktop_config.json")
	res, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, mcpRegAdded, res.Action)

	// A file this command creates starts owner-only: other servers' entries may
	// later carry secrets in env.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	var top map[string]map[string]claudeDesktopMCPServer
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &top))
	require.Equal(t, claudeDesktopMCPServer{
		Command: "/opt/seam",
		Args:    []string{"mcp-proxy", "--config", "/etc/seamless.yaml"},
	}, top["mcpServers"]["seamless"])

	// No pre-existing file means no backup to take.
	matches, err := filepath.Glob(path + ".seamless-bak-*")
	require.NoError(t, err)
	require.Empty(t, matches)
}

// The file holds live app preferences; the merge may only ever touch the
// reserved entry. Foreign top-level values -- including number literals an
// any-typed round-trip would rewrite -- and foreign servers survive verbatim.
func TestReconcileClaudeDesktopMCP_PreservesUnknownKeysAndServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	original := `{
  "globalShortcut": "Ctrl+Space",
  "scale": 1e21,
  "mcpServers": {
    "other": {"command": "npx", "args": ["-y", "some-server"], "env": {"TOKEN": "keep-me"}}
  }
}`
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	res, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, mcpRegAdded, res.Action)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	require.Contains(t, text, `"globalShortcut": "Ctrl+Space"`)
	require.Contains(t, text, "1e21", "foreign number literals must round-trip verbatim")
	require.Contains(t, text, `"keep-me"`)

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	var servers map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["mcpServers"], &servers))
	require.Contains(t, servers, "other")
	require.Contains(t, servers, "seamless")

	// The pre-change original is preserved once, with its mode.
	matches, err := filepath.Glob(path + ".seamless-bak-*")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	backup, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, original, string(backup))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "an existing file keeps its mode")
}

func TestReconcileClaudeDesktopMCP_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	res, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, mcpRegAdded, res.Action)
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	res, err = reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, mcpRegUnchanged, res.Action)
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "an exact registration must not rewrite the file")
	// An unchanged run takes no backup either.
	matches, err := filepath.Glob(path + ".seamless-bak-*")
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestReconcileClaudeDesktopMCP_RepairsOwnedDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	stale := `{"mcpServers":{"seamless":{"command":"/old/bin/seam","args":["mcp-proxy"],"env":{"X":"1"}}}}`
	require.NoError(t, os.WriteFile(path, []byte(stale), 0o600))

	res, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.NoError(t, err)
	require.Equal(t, mcpRegRepaired, res.Action)
	require.Contains(t, res.Drift, "bridge command differs")
	require.Contains(t, res.Drift, "bridge environment present")

	var top map[string]map[string]claudeDesktopMCPServer
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &top))
	require.Equal(t, claudeDesktopMCPServer{
		Command: "/opt/seam",
		Args:    []string{"mcp-proxy", "--config", "/etc/seamless.yaml"},
	}, top["mcpServers"]["seamless"], "repair rewrites the entry to the canonical desired form")
}

func TestReconcileClaudeDesktopMCP_RefusesForeignEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	foreign := `{"mcpServers":{"seamless":{"command":"npx","args":["-y","other-server"]}}}`
	require.NoError(t, os.WriteFile(path, []byte(foreign), 0o600))

	_, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.ErrorContains(t, err, "incompatible configuration")
	require.ErrorContains(t, err, "Settings > Developer > Edit Config")

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, foreign, string(data), "an incompatible entry must never be touched")
}

func TestReconcileClaudeDesktopMCP_RefusesUnparseableEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	odd := `{"mcpServers":{"seamless":"http://127.0.0.1:8081/api/mcp"}}`
	require.NoError(t, os.WriteFile(path, []byte(odd), 0o600))

	_, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.ErrorContains(t, err, "unrecognized shape")

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, odd, string(data))
}

func TestReconcileClaudeDesktopMCP_RefusesNonObjectMCPServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o600))
	_, err := reconcileClaudeDesktopMCP(path, "/opt/seam", "/etc/seamless.yaml")
	require.ErrorIs(t, err, errMCPServersNotObject)
}

func TestRemoveClaudeDesktopMCP(t *testing.T) {
	t.Run("absent file is nothing to remove", func(t *testing.T) {
		removed, err := removeClaudeDesktopMCP(filepath.Join(t.TempDir(), "claude_desktop_config.json"))
		require.NoError(t, err)
		require.False(t, removed)
	})

	t.Run("no entry leaves the file untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
		original := `{"globalShortcut":"Ctrl+Space"}`
		require.NoError(t, os.WriteFile(path, []byte(original), 0o644))
		removed, err := removeClaudeDesktopMCP(path)
		require.NoError(t, err)
		require.False(t, removed)
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, original, string(data))
	})

	t.Run("non-object mcpServers holds nothing of ours", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o644))
		removed, err := removeClaudeDesktopMCP(path)
		require.NoError(t, err)
		require.False(t, removed)
	})

	t.Run("removes only the seamless entry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
		require.NoError(t, os.WriteFile(path, []byte(
			`{"globalShortcut":"Ctrl+Space","mcpServers":{"other":{"command":"npx"},"seamless":{"command":"/opt/seam","args":["mcp-proxy"]}}}`), 0o644))
		removed, err := removeClaudeDesktopMCP(path)
		require.NoError(t, err)
		require.True(t, removed)

		var top map[string]json.RawMessage
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NoError(t, json.Unmarshal(data, &top))
		require.Contains(t, top, "globalShortcut")
		var servers map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(top["mcpServers"], &servers))
		require.Contains(t, servers, "other")
		require.NotContains(t, servers, "seamless")

		matches, globErr := filepath.Glob(path + ".seamless-bak-*")
		require.NoError(t, globErr)
		require.Len(t, matches, 1)
	})

	t.Run("keeps an emptied mcpServers key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
		require.NoError(t, os.WriteFile(path, []byte(
			`{"mcpServers":{"seamless":{"command":"/opt/seam","args":["mcp-proxy"]}}}`), 0o644))
		removed, err := removeClaudeDesktopMCP(path)
		require.NoError(t, err)
		require.True(t, removed)
		var top map[string]json.RawMessage
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NoError(t, json.Unmarshal(data, &top))
		// Removal deletes only the entry; tidying the container would be editing
		// state uninstall does not own.
		require.Contains(t, top, "mcpServers")
	})
}

func TestClaudeDesktopMCPSetupHint(t *testing.T) {
	hint := claudeDesktopMCPSetupHint("/opt/seam", "/etc/seamless.yaml")
	require.Contains(t, hint, "Settings > Developer > Edit Config")
	require.Contains(t, hint, `"seamless"`)
	require.Contains(t, hint, `"command": "/opt/seam"`)
	require.Contains(t, hint, `"args": ["mcp-proxy", "--config", "/etc/seamless.yaml"]`)
	require.Contains(t, hint, "restart the app")
	require.NotContains(t, hint, "Bearer")
	require.NotContains(t, hint, "api_key")

	require.Contains(t, claudeDesktopMCPSetupHint("/opt/seam", ""), `"args": ["mcp-proxy"]`)
}

// --client claude-desktop wires only the chat surface's MCP bridge: the desktop
// config gains the stdio entry and neither hook file is created -- the surface
// has no hooks and no skills.
func TestRunInstallHooks_ClaudeDesktop(t *testing.T) {
	home := t.TempDir()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "seamless.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("mcp:\n  api_key: \"test-key\"\n"), 0o600))
	t.Setenv("SEAMLESS_CONFIG", cfgPath)
	t.Setenv("HOME", home)

	desktopConfig := filepath.Join(tmp, "claude_desktop_config.json")
	err := runInstallHooks([]string{
		"--client", "claude-desktop", "--desktop-config", desktopConfig, "--seam", "/opt/seam",
	})
	require.NoError(t, err)

	var top map[string]map[string]claudeDesktopMCPServer
	data, err := os.ReadFile(desktopConfig)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &top))
	require.Equal(t, claudeDesktopMCPServer{
		Command: "/opt/seam",
		Args:    []string{"mcp-proxy", "--config", cfgPath},
	}, top["mcpServers"]["seamless"])
	require.NotContains(t, string(data), "test-key", "no secret may reach the app config")

	require.NoFileExists(t, filepath.Join(home, ".claude", "settings.json"))
	require.NoDirExists(t, filepath.Join(home, ".claude", "skills"))
}

// The chat surface is MCP-only, so --mcp=false leaves nothing to install;
// present-but-contradictory flags are an error, never a silent no-op.
func TestRunInstallHooks_ClaudeDesktopRequiresMCP(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "seamless.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("mcp:\n  api_key: \"test-key\"\n"), 0o600))
	t.Setenv("SEAMLESS_CONFIG", cfgPath)
	t.Setenv("HOME", tmp)

	err := runInstallHooks([]string{"--client", "claude-desktop", "--mcp=false"})
	require.ErrorContains(t, err, "leaves nothing to install")
}

// A mixed selection naming the chat surface beside a hook client wires both in
// one run: the hook file lands AND the desktop config gains the bridge entry,
// with --mcp left on for neither being an error.
func TestRunInstallHooks_MixedSelectionIncludesDesktop(t *testing.T) {
	home := t.TempDir()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "seamless.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("mcp:\n  api_key: \"test-key\"\n"), 0o600))
	t.Setenv("SEAMLESS_CONFIG", cfgPath)
	t.Setenv("HOME", home)
	// No claude CLI on PATH: the second run (MCP on) must degrade to printing
	// the manual `claude mcp` command, never invoke the real client CLI.
	t.Setenv("PATH", tmp)

	settings := filepath.Join(tmp, "settings.json")
	desktopConfig := filepath.Join(tmp, "claude_desktop_config.json")
	err := runInstallHooks([]string{
		"--client", "claude,claude-desktop", "--settings", settings,
		"--desktop-config", desktopConfig, "--mcp=false", "--skills=false",
		"--seam", "/opt/seam", "--url", "http://127.0.0.1:8081",
	})
	require.NoError(t, err)

	// The hook client installed; the MCP-only chat surface skipped visibly
	// (--mcp=false) rather than erroring or writing a bridge entry.
	require.FileExists(t, settings)
	require.NoFileExists(t, desktopConfig)

	err = runInstallHooks([]string{
		"--client", "claude,claude-desktop", "--settings", settings,
		"--desktop-config", desktopConfig, "--skills=false",
		"--seam", "/opt/seam", "--url", "http://127.0.0.1:8081",
	})
	require.NoError(t, err)
	require.FileExists(t, desktopConfig)
}

func TestReconcileClaudeDesktopMCP_RejectsRelativePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	_, err := reconcileClaudeDesktopMCP(path, "seam", "/etc/seamless.yaml")
	require.ErrorContains(t, err, "not absolute")
	require.NoFileExists(t, path)
}

// writeDesktopDoctorFixture lays down a runnable desired state for the doctor
// tests: an executable seam binary, a config file, and the desktop config
// holding the given mcpServers entry (or no file when entry is nil).
func writeDesktopDoctorFixture(t *testing.T, entry any) (path, seamBin, configPath string) {
	t.Helper()
	dir := t.TempDir()
	seamBin = filepath.Join(dir, seamBinName())
	writeTestExecutable(t, seamBin)
	configPath = filepath.Join(dir, "seamless.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("mcp:\n  api_key: \"k\"\n"), 0o600))
	path = filepath.Join(dir, "claude_desktop_config.json")
	if entry != nil {
		blob, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"seamless": entry}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, blob, 0o600))
	}
	return path, seamBin, configPath
}

// With neither the app nor a desktop config file present there is nothing to
// diagnose: no lines at all, symmetric with claudeRuntimeChecks, so a machine
// without the Claude app is never nagged about its chat surface.
func TestClaudeDesktopChecksFor_QuietWithoutAppOrConfig(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	require.Empty(t, claudeDesktopChecksFor(path, false, seamBin, configPath))
}

// The app being present without a registration is informational, never a
// warning: the chat surface is explicit opt-in (--client claude-desktop), so
// an unregistered app is a choice, not drift. The line names both the
// automatic command and the manual Settings > Developer fallback.
func TestClaudeDesktopChecksFor_AppWithoutRegistrationIsInfo(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	checks := claudeDesktopChecksFor(path, true, seamBin, configPath)
	require.Len(t, checks, 1)
	require.Equal(t, statusInfo, checks[0].status)
	require.Equal(t, "claude desktop mcp", checks[0].name)
	require.Contains(t, checks[0].detail, "not registered")
	require.Contains(t, checks[0].detail, "seamlessd install-hooks --client claude-desktop")
	require.Contains(t, checks[0].detail, "Settings > Developer > Edit Config")
}

// A desktop config file alone triggers diagnosis even when the app was not
// detected: app detection is macOS-only, so on Windows (and for custom
// layouts) the file is the only evidence there is a chat surface to check.
func TestClaudeDesktopChecksFor_ConfigFileAloneTriggersDiagnosis(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600))
	checks := claudeDesktopChecksFor(path, false, seamBin, configPath)
	require.Len(t, checks, 1)
	require.Equal(t, statusInfo, checks[0].status)
	require.Contains(t, checks[0].detail, "not registered")
}

// An exact runnable registration is OK, but the detail must state that the
// running app's loaded state is unverifiable: the app reads the config only at
// startup and exposes no way to ask what it loaded, so "registered but not
// restarted" cannot be told apart from "live" -- doctor says so instead of
// guessing.
func TestClaudeDesktopMCPCheck_ExactReportsLoadedStateUnverifiable(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	want, err := desiredClaudeDesktopMCPServer(seamBin, configPath)
	require.NoError(t, err)
	blob, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"seamless": want}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, blob, 0o600))

	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusOK, chk.status)
	require.Contains(t, chk.detail, "exact stdio bridge (seam mcp-proxy)")
	require.Contains(t, chk.detail, path)
	require.Contains(t, chk.detail, "unverifiable")
	require.Contains(t, chk.detail, "reads the config at startup")
}

func TestClaudeDesktopMCPCheck_OwnedDriftWarnsStale(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t,
		claudeDesktopMCPServer{Command: "/old/bin/seam", Args: []string{"mcp-proxy"}})
	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "owned registration is stale")
	require.Contains(t, chk.detail, "bridge command differs")
	require.Contains(t, chk.detail, "seamlessd install-hooks --client claude-desktop")
}

// A foreign server squatting the reserved name is diagnosed but the fix stays
// manual: doctor points at the app's own config editor, mirroring the
// reconciler's refusal to repair over an entry it cannot prove it owns.
func TestClaudeDesktopMCPCheck_ForeignEntryWarnsIncompatible(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t,
		claudeDesktopMCPServer{Command: "npx", Args: []string{"-y", "other-memory-server"}})
	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "incompatible registration")
	require.Contains(t, chk.detail, "Settings > Developer > Edit Config")
}

func TestClaudeDesktopMCPCheck_UnrecognizedShapeWarns(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, "http://127.0.0.1:8081/api/mcp")
	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "unrecognized shape")
	require.Contains(t, chk.detail, "Settings > Developer > Edit Config")
}

func TestClaudeDesktopMCPCheck_UnreadableConfigWarns(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o600))
	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "cannot inspect")
	require.Contains(t, chk.detail, "mcpServers is not an object")
}

// An exact entry whose targets are gone is non-operational even though the
// registration matches desired state: the comparator and the runnability probe
// stay separate results, same as codex mcp.
func TestClaudeDesktopMCPCheck_ExactButMissingBridgeIsNotRunnable(t *testing.T) {
	path, seamBin, configPath := writeDesktopDoctorFixture(t, nil)
	require.NoError(t, os.Remove(seamBin))
	want, err := desiredClaudeDesktopMCPServer(seamBin, configPath)
	require.NoError(t, err)
	blob, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"seamless": want}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, blob, 0o600))

	chk := claudeDesktopMCPCheck(path, seamBin, configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "not runnable")
	require.Contains(t, chk.detail, "bridge executable is missing")
}

// Desired state is built from what install-hooks would write NOW; when that
// cannot be computed the check says so rather than falling back to paths read
// from the existing entry, which would make uniform drift look current.
func TestClaudeDesktopMCPCheck_RelativeSeamBinCannotBuildDesired(t *testing.T) {
	path, _, configPath := writeDesktopDoctorFixture(t,
		claudeDesktopMCPServer{Command: "/opt/seam", Args: []string{"mcp-proxy"}})
	chk := claudeDesktopMCPCheck(path, "seam", configPath)
	require.Equal(t, statusWarn, chk.status)
	require.Contains(t, chk.detail, "cannot build desired registration")
	require.Contains(t, chk.detail, "not absolute")
}
