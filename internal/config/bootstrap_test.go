package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// unsetenvForTest clears key for the duration of the test. t.Setenv with the
// current value first registers the restore; the unset then takes effect.
func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	if v, ok := os.LookupEnv(key); ok {
		t.Setenv(key, v)
	}
	require.NoError(t, os.Unsetenv(key))
}

func TestEnsureAPIKey(t *testing.T) {
	t.Run("first run writes the default config with a generated key", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		unsetenvForTest(t, "SEAMLESS_CONFIG")
		unsetenvForTest(t, "SEAMLESS_MCP_API_KEY")

		cfg, err := Load()
		require.NoError(t, err)
		require.Empty(t, cfg.SourcePath())

		got, path, err := EnsureAPIKey(cfg)
		require.NoError(t, err)
		want := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.Equal(t, want, path)
		require.Len(t, got.MCP.APIKey, 64)
		require.Equal(t, want, got.SourcePath())

		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		reloaded, err := Load()
		require.NoError(t, err)
		require.Equal(t, got.MCP.APIKey, reloaded.MCP.APIKey)
	})

	t.Run("a key already set is untouched", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		cfg := Defaults()
		cfg.MCP.APIKey = "already-set"

		got, path, err := EnsureAPIKey(cfg)
		require.NoError(t, err)
		require.Empty(t, path)
		require.Equal(t, "already-set", got.MCP.APIKey)
	})

	t.Run("an existing config file with an empty key is never edited", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		unsetenvForTest(t, "SEAMLESS_MCP_API_KEY")
		path := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		body := "mcp:\n  api_key: \"\"\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

		cfg, err := LoadFrom(path)
		require.NoError(t, err)
		got, wrote, err := EnsureAPIKey(cfg)
		require.NoError(t, err)
		require.Empty(t, wrote)
		require.Empty(t, got.MCP.APIKey)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, body, string(data))
	})

	t.Run("a set-but-empty env key blocks generation", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		unsetenvForTest(t, "SEAMLESS_CONFIG")
		t.Setenv("SEAMLESS_MCP_API_KEY", "")

		cfg, err := Load()
		require.NoError(t, err)
		got, wrote, err := EnsureAPIKey(cfg)
		require.NoError(t, err)
		require.Empty(t, wrote)
		require.Empty(t, got.MCP.APIKey)
		require.NoFileExists(t, filepath.Join(home, ".config", "seamless", "seamless.yaml"))
	})

	t.Run("a config file that appeared since Load wins", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		unsetenvForTest(t, "SEAMLESS_CONFIG")
		unsetenvForTest(t, "SEAMLESS_MCP_API_KEY")

		cfg, err := Load()
		require.NoError(t, err)

		path := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("mcp:\n  api_key: \"theirs\"\n"), 0o600))

		got, wrote, err := EnsureAPIKey(cfg)
		require.NoError(t, err)
		require.Empty(t, wrote)
		require.Equal(t, "theirs", got.MCP.APIKey)
	})
}
