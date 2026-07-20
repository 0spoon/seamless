package main

import (
	"encoding/json"
	"fmt"
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
	events, eventsErr := hooks.InstalledEvents(hooks.ClientCodex)
	require.NoError(t, eventsErr)
	require.Contains(t, checks[0].detail, fmt.Sprintf("%d/%d current", len(events)-1, len(events)))
	require.Contains(t, checks[0].detail, "stale: Stop")
}

func TestDoctorInstallOptionsPrefersRecordedPaths(t *testing.T) {
	dir := t.TempDir()
	seam := filepath.Join(dir, "seam")
	require.NoError(t, os.WriteFile(seam, []byte("#!/bin/sh\n"), 0o755))
	yaml := filepath.Join(dir, "seamless.yaml")
	path := filepath.Join(dir, "hooks.json")

	cfg := config.Defaults()
	cfg.MCP.APIKey = "k"
	_, err := hooks.Install(hooks.InstallOptions{
		Client: hooks.ClientCodex, SettingsPath: path, BaseURL: hookBaseURL(cfg.Addr),
		APIKey: cfg.MCP.APIKey, SeamBin: seam, ConfigPath: yaml,
	})
	require.NoError(t, err)

	// The doctor binary's sibling seam is a different path from the recorded
	// one; judging against the recorded paths keeps the install current.
	opts := doctorInstallOptions(hooks.ClientCodex, path, cfg)
	require.Equal(t, seam, opts.SeamBin)
	require.Equal(t, yaml, opts.ConfigPath)

	status, err := hooks.InstalledStatus(opts)
	require.NoError(t, err)
	events, err := hooks.InstalledEvents(hooks.ClientCodex)
	require.NoError(t, err)
	require.ElementsMatch(t, events, status.Current)
	require.Empty(t, status.Stale)
}

func TestDoctorInstallOptionsFallsBackWhenRecordedBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "seam")
	path := filepath.Join(dir, "hooks.json")

	cfg := config.Defaults()
	cfg.MCP.APIKey = "k"
	_, err := hooks.Install(hooks.InstallOptions{
		Client: hooks.ClientCodex, SettingsPath: path, BaseURL: hookBaseURL(cfg.Addr),
		APIKey: cfg.MCP.APIKey, SeamBin: gone,
	})
	require.NoError(t, err)

	opts := doctorInstallOptions(hooks.ClientCodex, path, cfg)
	require.Equal(t, resolveSeamBin(""), opts.SeamBin)
}
