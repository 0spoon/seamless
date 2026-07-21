package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverClaudeAppRuntimesEnumeratesVersionBundles(t *testing.T) {
	dir := t.TempDir()

	// Two retained versions with real binaries, one version directory without a
	// binary (skipped, never guessed at), and a stray file at the top level.
	for _, version := range []string{"2.1.215", "2.1.220"} {
		bin := filepath.Join(dir, version, "claude.app", "Contents", "MacOS", "claude")
		require.NoError(t, os.MkdirAll(filepath.Dir(bin), 0o755))
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "2.1.216-partial"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".verified"), []byte("x"), 0o644))

	candidates := discoverClaudeAppRuntimes(dir)
	require.Len(t, candidates, 2)
	for _, candidate := range candidates {
		require.Equal(t, "claude app runtime", candidate.name)
	}
	// os.ReadDir sorts entries, so the order is deterministic.
	require.Equal(t, filepath.Join(dir, "2.1.215", "claude.app", "Contents", "MacOS", "claude"), candidates[0].path)
	require.Equal(t, filepath.Join(dir, "2.1.220", "claude.app", "Contents", "MacOS", "claude"), candidates[1].path)
}

func TestDiscoverClaudeAppRuntimesMissingDirIsQuiet(t *testing.T) {
	require.Empty(t, discoverClaudeAppRuntimes(filepath.Join(t.TempDir(), "absent")))
}

func TestClaudeAppSharesSelectedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset means the default home", "", true},
		{"blank means the default home", "   ", true},
		{"explicit default home", filepath.Join(home, ".claude"), true},
		{"tilde default home", "~/.claude", true},
		{"custom home breaks the shared-state assumption", filepath.Join(home, "elsewhere"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tc.value)
			require.Equal(t, tc.want, claudeAppSharesSelectedHome())
		})
	}
}
