package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// removedActions returns the "<event>: removed" entries from an uninstall result.
func removedActions(actions []string) []string {
	var out []string
	for _, a := range actions {
		if strings.HasSuffix(a, ": removed") {
			out = append(out, a)
		}
	}
	return out
}

func readInstallStatus(t *testing.T, client Client, path, baseURL string) InstallStatus {
	t.Helper()
	status, err := InstalledStatus(InstallOptions{
		Client: client, SettingsPath: path, BaseURL: baseURL, APIKey: "k",
	})
	require.NoError(t, err)
	return status
}

// Install then Uninstall is a round-trip: our hooks vanish, unknown top-level
// keys survive, and the "hooks" key is dropped once only Seamless hooks lived
// under it (rather than leaving an empty object behind).
func TestUninstallRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600))

	base := "http://127.0.0.1:8081"
	_, err := Install(InstallOptions{SettingsPath: path, BaseURL: base, APIKey: "k"})
	require.NoError(t, err)

	res, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Len(t, removedActions(res.Actions), len(installedEvents(t, ClientClaudeCode)))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "opus", got["model"], "unknown top-level key preserved")
	_, hasHooks := got["hooks"]
	require.False(t, hasHooks, "hooks key dropped once only Seamless hooks were present")

	status := readInstallStatus(t, ClientClaudeCode, path, base)
	require.Empty(t, status.Owned, "nothing of ours remains")
}

// Claude Code strips the seamless_managed marker on unrelated edits; the exact
// current and known-legacy classifier states must still allow safe removal.
func TestUninstallRemovesMarkerStrippedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	base := "http://127.0.0.1:8081"
	_, err := Install(InstallOptions{SettingsPath: path, BaseURL: base, APIKey: "k"})
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	for _, arr := range settings["hooks"].(map[string]any) {
		for _, e := range arr.([]any) {
			delete(e.(map[string]any), managedMarker)
		}
	}
	raw, err = json.Marshal(settings)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	res, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.True(t, res.Changed)

	status := readInstallStatus(t, ClientClaudeCode, path, base)
	require.Empty(t, status.Owned, "marker-stripped hooks are still removed")
}

// Uninstall removes only Seamless entries: a v1 seam_managed :8080 hook and an
// unrelated user hook on a different event both survive, and an event that held
// only our now-removed hook is dropped.
func TestUninstallPreservesForeignAndUserHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "hooks": {
    "SessionStart": [
      {"seam_managed": true, "matcher": "startup", "hooks": [{"type":"http","url":"http://127.0.0.1:8080/api/hooks/session-start"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type":"command","command":"/usr/local/bin/my-guard"}]}
    ]
  }
}`), 0o600))

	base := "http://127.0.0.1:8081"
	_, err := Install(InstallOptions{SettingsPath: path, BaseURL: base, APIKey: "k"})
	require.NoError(t, err)

	res, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.True(t, res.Changed)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	hooksObj := got["hooks"].(map[string]any)

	// The v1 :8080 entry survives; only our added SessionStart hook is removed.
	ss := hooksObj["SessionStart"].([]any)
	require.Len(t, ss, 1)
	require.Equal(t, true, ss[0].(map[string]any)["seam_managed"])

	// The user's own hook on an event Seamless never manages is untouched.
	pre := hooksObj["PreToolUse"].([]any)
	require.Len(t, pre, 1)

	// An event that held only our hook is dropped, not left as an empty array.
	_, hasUPS := hooksObj["UserPromptSubmit"]
	require.False(t, hasUPS, "UserPromptSubmit array dropped after our only entry was removed")

	status := readInstallStatus(t, ClientClaudeCode, path, base)
	require.Empty(t, status.Owned)
}

// A missing file and a second run are both clean no-ops.
func TestUninstallIdempotentAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	base := "http://127.0.0.1:8081"

	res, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Empty(t, res.BackupPath)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "uninstall must not create the file")

	_, err = Install(InstallOptions{SettingsPath: path, BaseURL: base, APIKey: "k"})
	require.NoError(t, err)
	_, err = Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)

	res2, err := Uninstall(UninstallOptions{SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.False(t, res2.Changed)
	for _, a := range res2.Actions {
		require.Contains(t, a, "absent")
	}
}

// The complete Codex command-hook profile round-trips as one owned set.
func TestUninstallCodexClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	base := "http://127.0.0.1:8081"
	_, err := Install(InstallOptions{Client: ClientCodex, SettingsPath: path, BaseURL: base, APIKey: "k"})
	require.NoError(t, err)
	status := readInstallStatus(t, ClientCodex, path, base)
	want := len(installedEvents(t, ClientCodex))
	require.Len(t, status.Current, want)

	res, err := Uninstall(UninstallOptions{Client: ClientCodex, SettingsPath: path, BaseURL: base})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Len(t, removedActions(res.Actions), want)

	status = readInstallStatus(t, ClientCodex, path, base)
	require.Empty(t, status.Owned)
}

func TestUninstallValidatesOptions(t *testing.T) {
	_, err := Uninstall(UninstallOptions{SettingsPath: "", BaseURL: "http://127.0.0.1:8081"})
	require.Error(t, err)
	_, err = Uninstall(UninstallOptions{SettingsPath: "/tmp/x.json", BaseURL: ""})
	require.Error(t, err)
}
