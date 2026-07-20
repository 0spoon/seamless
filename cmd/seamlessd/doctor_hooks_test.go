package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/hooks"
	"github.com/stretchr/testify/require"
)

func TestCodexChecksMissingClientIsStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("PATH", t.TempDir())

	cfg := config.Defaults()
	cfg.MCP.APIKey = "k"
	path := filepath.Join(home, "hooks.json")
	_, err := hooks.Install(hooks.InstallOptions{
		Client: hooks.ClientCodex, SettingsPath: path, BaseURL: hookBaseURL(cfg.Addr),
		APIKey: cfg.MCP.APIKey, SeamBin: "seam",
	})
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	stop := settings["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)
	delete(stop, "seamless_managed")
	handler := stop["hooks"].([]any)[0].(map[string]any)
	handler["command"] = strings.ReplaceAll(handler["command"].(string), " --client codex", "")
	handler["command_windows"] = strings.ReplaceAll(handler["command_windows"].(string), " --client codex", "")
	raw, err = json.Marshal(settings)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	checks := codexChecks(cfg)
	require.NotEmpty(t, checks)
	require.Equal(t, statusWarn, checks[0].status)
	require.Contains(t, checks[0].detail, "2/3 current")
	require.Contains(t, checks[0].detail, "stale: Stop")
}
