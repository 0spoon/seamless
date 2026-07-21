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

func TestIsClaudeDesktopSelector(t *testing.T) {
	for _, yes := range []string{"claude-desktop", "desktop", " Claude-Desktop "} {
		require.True(t, isClaudeDesktopSelector(yes), yes)
	}
	for _, no := range []string{"", "claude", "codex", "all", "detect", "claude-code"} {
		require.False(t, isClaudeDesktopSelector(no), no)
	}
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

// parseInstallClients still rejects the desktop selector: it can only yield
// hooks.Client values, and the chat surface deliberately is not one. Both
// commands resolve claude-desktop before calling it.
func TestParseInstallClients_RejectsClaudeDesktop(t *testing.T) {
	_, err := parseInstallClients("claude-desktop", true, true)
	require.ErrorContains(t, err, "valid values are claude, codex, claude-desktop, all, detect")
}

func TestReconcileClaudeDesktopMCP_RejectsRelativePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	_, err := reconcileClaudeDesktopMCP(path, "seam", "/etc/seamless.yaml")
	require.ErrorContains(t, err, "not absolute")
	require.NoFileExists(t, path)
}
