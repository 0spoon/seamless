package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// codexOpts is the standard Codex install: the profile client, an abs seam
// binary, and an abs config path so the emitted shell strings carry --config.
func codexOpts(path string) InstallOptions {
	return InstallOptions{
		Client: ClientCodex, SettingsPath: path, BaseURL: "http://127.0.0.1:8081",
		APIKey: "k", SeamBin: "/opt/seam", ConfigPath: "/etc/seamless.yaml",
	}
}

// requireCodexCommandHook asserts the event carries a Seamless-owned Codex
// command hook: a shell-string `command` running `seam hook <cliArg> ... --client
// codex`, a `command_windows` variant, the managed marker, and no exec-form args.
func requireCodexCommandHook(t *testing.T, hooksObj map[string]any, event, cliArg string) {
	t.Helper()
	arr := entryArray(hooksObj, event)
	require.NotEmpty(t, arr, "%s should be installed", event)
	for _, e := range arr {
		if !isManaged(e) {
			continue
		}
		m := e.(map[string]any)
		require.Equal(t, true, m[managedMarker], "%s entry should carry the managed marker", event)
		h0 := m["hooks"].([]any)[0].(map[string]any)
		require.Equal(t, "command", h0["type"], "%s should be a command hook", event)
		require.Nil(t, h0["args"], "Codex command hooks are shell strings, not exec-form args")
		cmd, ok := h0["command"].(string)
		require.True(t, ok, "%s command should be a shell string", event)
		require.Contains(t, cmd, " hook "+cliArg, "%s should run `hook %s`", event, cliArg)
		require.Contains(t, cmd, "--client codex", "%s must carry --client codex", event)
		require.Contains(t, cmd, "--config", "%s must carry --config so it resolves config from any cwd", event)
		require.NotEmpty(t, h0["command_windows"], "%s needs a command_windows variant for the Windows install", event)
		return
	}
	t.Fatalf("no Codex command hook running `hook %s` found for %s", cliArg, event)
}

// A fresh Codex install writes exactly the three-hook profile as shell-string
// command hooks, no plan-capture or SessionEnd hooks (D7 / D5), and re-installing
// is a clean no-op.
func TestInstallCodexProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".codex", "hooks.json")

	res, err := Install(codexOpts(path))
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	// Only the top-level "hooks" key is written -- Codex's file struct is
	// deny_unknown_fields, so any stray top-level key would disable every hook.
	require.Equal(t, []string{"hooks"}, topKeys(got))

	hooksObj := got["hooks"].(map[string]any)
	requireCodexCommandHook(t, hooksObj, "SessionStart", "session-start")
	requireCodexCommandHook(t, hooksObj, "UserPromptSubmit", "user-prompt-submit")
	requireCodexCommandHook(t, hooksObj, "Stop", "stop")

	// SessionStart keeps its source matcher; UserPromptSubmit and Stop have none.
	require.Equal(t, "startup|resume|clear|compact",
		hooksObj["SessionStart"].([]any)[0].(map[string]any)["matcher"])
	require.Nil(t, hooksObj["UserPromptSubmit"].([]any)[0].(map[string]any)["matcher"])

	// No Claude Code / plan-capture hooks leak into the Codex file (D7), and no
	// SessionEnd (Codex 0.144.5 does not fire it -- D5).
	for _, absent := range []string{"SessionEnd", "PostToolUse", "SubagentStop", "PermissionRequest"} {
		require.NotContains(t, hooksObj, absent, "%s must not be installed for Codex", absent)
	}

	// The bearer key never lands in hooks.json -- command hooks load it from --config.
	require.NotContains(t, string(raw), "Bearer")
	require.NotContains(t, string(raw), "\"k\"")

	// Windows variant carries Windows quoting of the same binary + config paths.
	winCmd := hooksObj["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command_windows"].(string)
	require.Contains(t, winCmd, `"/opt/seam"`)
	require.Contains(t, winCmd, `"/etc/seamless.yaml"`)
	require.Contains(t, winCmd, "--client codex")

	res2, err := Install(codexOpts(path))
	require.NoError(t, err)
	require.False(t, res2.Changed, "re-install must be a clean no-op")
	for _, a := range res2.Actions {
		require.Contains(t, a, "unchanged")
	}
}

// A pre-existing hooks.json with the user's own foreign hook and a top-level
// description is preserved: the foreign entry survives and description is untouched.
func TestInstallCodexPreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "description": "my hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "echo mine"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo guard"}]}
    ]
  }
}`), 0o600))

	res, err := Install(codexOpts(path))
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.NotEmpty(t, res.BackupPath, "the pre-existing file should be backed up on first change")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "my hooks", got["description"], "top-level description preserved")

	hooksObj := got["hooks"].(map[string]any)
	// The foreign PreToolUse hook survives untouched.
	require.Contains(t, hooksObj, "PreToolUse")
	require.Contains(t, string(raw), "echo guard")
	// The user's own foreign SessionStart hook survives alongside the new one.
	ss := hooksObj["SessionStart"].([]any)
	require.Len(t, ss, 2)
	require.Contains(t, string(raw), "echo mine")
	requireCodexCommandHook(t, hooksObj, "SessionStart", "session-start")
}

// A prior Seamless Codex install whose marker was stripped, plus an unmarked
// duplicate, is adopted and deduped in place -- not appended beside.
func TestInstallCodexAdoptsAndDedupesPriorInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume|clear|compact", "hooks": [{"type": "command", "command": "/old/seam hook session-start --config /old/seamless.yaml --client codex", "timeout": 10}]},
      {"seamless_managed": true, "matcher": "startup|resume|clear|compact", "hooks": [{"type": "command", "command": "/old/seam hook session-start --config /old/seamless.yaml --client codex", "timeout": 10}]}
    ]
  }
}`), 0o600))

	res, err := Install(codexOpts(path))
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	hooksObj := got["hooks"].(map[string]any)

	// The two owned SessionStart entries collapse to a single marked entry
	// rewritten to the new binary/config path.
	ss := hooksObj["SessionStart"].([]any)
	require.Len(t, ss, 1)
	h0 := ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	require.Contains(t, h0["command"].(string), "/opt/seam")
	require.Contains(t, h0["command"].(string), "/etc/seamless.yaml")
	require.Contains(t, strings.Join(res.Actions, ","), "SessionStart: deduped")

	res2, err := Install(codexOpts(path))
	require.NoError(t, err)
	require.False(t, res2.Changed)
}

// InstalledStatus is client-scoped: the Codex profile's events are reported from
// a Codex install, and a Claude Code query against the same file finds nothing
// (the events overlap in name but the profiles and files never do).
func TestInstalledStatusCodex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")

	status, err := InstalledStatus(codexOpts(path))
	require.NoError(t, err)
	require.Empty(t, status.Owned)

	_, err = Install(codexOpts(path))
	require.NoError(t, err)

	status, err = InstalledStatus(codexOpts(path))
	require.NoError(t, err)
	require.Equal(t, installedEvents(t, ClientCodex), status.Current)
	require.Empty(t, status.Stale)
	require.Len(t, status.Current, 3)
}

// topKeys returns the sorted top-level keys of a decoded JSON object.
func topKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// tiny insertion sort keeps the helper dependency-free
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
