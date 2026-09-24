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

// clientEnvIsolation clears every environment override that would otherwise
// leak the developer's own install into a bootstrap test.
func clientEnvIsolation(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	for _, key := range []string{"SEAMLESS_CONFIG", "SEAMLESS_MCP_API_KEY", "SEAMLESS_SERVER_URL", "SEAMLESS_ROLE"} {
		unsetenvForTest(t, key)
	}
}

func TestEnsureClientConfig(t *testing.T) {
	t.Run("first run writes role, server_url and the key -- and no data_dir", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)

		got, path, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.NoError(t, err)
		want := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.Equal(t, want, path)
		require.True(t, got.IsClient())
		require.Equal(t, "https://server.example:8081", got.ServerURL())
		require.Equal(t, "server-key", got.MCP.APIKey)
		require.Equal(t, want, got.SourcePath())

		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		body := string(data)
		require.Contains(t, body, "role: client\n")
		require.Contains(t, body, "server_url: \"https://server.example:8081\"\n")
		require.Contains(t, body, "  api_key: \"server-key\"\n")
		require.NotContains(t, body, "\ndata_dir:", "a client holds no corpus, so it is given no data dir key")

		// The file is what every later process loads, not just what this call
		// returned.
		reloaded, err := Load()
		require.NoError(t, err)
		require.True(t, reloaded.IsClient())
		require.Equal(t, "https://server.example:8081", reloaded.ServerURL())
	})

	t.Run("a trailing slash is trimmed, not written into the base URL", func(t *testing.T) {
		clientEnvIsolation(t, t.TempDir())
		got, _, err := EnsureClientConfig("http://box.lan:8081/", "k")
		require.NoError(t, err)
		require.Equal(t, "http://box.lan:8081", got.ServerURL())
	})

	t.Run("an all-digit key survives the round trip", func(t *testing.T) {
		clientEnvIsolation(t, t.TempDir())
		got, _, err := EnsureClientConfig("http://box.lan:8081", "0123456789")
		require.NoError(t, err)
		require.Equal(t, "0123456789", got.MCP.APIKey)
	})

	t.Run("an existing config file is never edited", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)
		path := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		body := "mcp:\n  api_key: \"mine\"\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

		_, wrote, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.Error(t, err)
		require.Empty(t, wrote)
		// The message is the whole remedy: the file, and the exact lines.
		require.Contains(t, err.Error(), path)
		require.Contains(t, err.Error(), "role: client")
		require.Contains(t, err.Error(), "server_url: \"https://server.example:8081\"")
		require.Contains(t, err.Error(), "api_key: <the key from --api-key>")
		require.NotContains(t, err.Error(), "server-key", "the key never goes into an error message")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, body, string(data))
	})

	t.Run("re-running against the same server writes nothing and succeeds", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)
		got, path, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.NoError(t, err)
		require.NotEmpty(t, path)
		before, err := os.ReadFile(path)
		require.NoError(t, err)

		// Upgrading is "install-hooks again": the second run must be a no-op,
		// not a refusal of the config the first run wrote.
		again, wrote, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.NoError(t, err)
		require.Empty(t, wrote, "nothing is written the second time")
		require.Equal(t, got.ServerURL(), again.ServerURL())
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))

		// A different server, or a rotated key, is the refusal again: the file
		// is the owner's to change.
		_, _, err = EnsureClientConfig("https://other.example:8081", "server-key")
		require.ErrorContains(t, err, "never edited on your behalf")
		_, _, err = EnsureClientConfig("https://server.example:8081", "rotated-key")
		require.ErrorContains(t, err, "never edited on your behalf")
	})

	t.Run("a SEAMLESS_CONFIG file elsewhere in the search order is never edited", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)
		elsewhere := filepath.Join(t.TempDir(), "seamless.yaml")
		body := "mcp:\n  api_key: \"mine\"\n"
		require.NoError(t, os.WriteFile(elsewhere, []byte(body), 0o600))
		t.Setenv("SEAMLESS_CONFIG", elsewhere)

		_, wrote, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.Error(t, err)
		require.Empty(t, wrote)
		require.Contains(t, err.Error(), elsewhere)
		require.NoFileExists(t, filepath.Join(home, ".config", "seamless", "seamless.yaml"),
			"the home path is not written when another file already answers the search")
	})

	t.Run("O_EXCL: a path that already exists is never clobbered", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)
		path := filepath.Join(home, ".config", "seamless", "seamless.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		// A dangling symlink is the one shape the search order steps over (Stat
		// follows it and fails) while O_CREATE|O_EXCL still refuses the path --
		// which is exactly the race branch: something is there, so nothing of
		// ours is written over it.
		missing := filepath.Join(home, "elsewhere.yaml")
		if err := os.Symlink(missing, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, wrote, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.Error(t, err)
		require.Empty(t, wrote)
		require.NoFileExists(t, missing, "the symlink target is never created")
		target, lerr := os.Readlink(path)
		require.NoError(t, lerr)
		require.Equal(t, missing, target, "the link itself is left alone")
	})

	t.Run("refuses a URL that is not a bare absolute base URL", func(t *testing.T) {
		clientEnvIsolation(t, t.TempDir())
		for _, url := range []string{"", "server.example:8081", "https://server.example:8081/api", "ftp://server.example"} {
			_, wrote, err := EnsureClientConfig(url, "server-key")
			require.Error(t, err, "url %q", url)
			require.Empty(t, wrote)
		}
	})

	t.Run("a SEAMLESS_SERVER_URL naming another server is contradictory input", func(t *testing.T) {
		home := t.TempDir()
		clientEnvIsolation(t, home)
		t.Setenv("SEAMLESS_SERVER_URL", "https://other.example:8081")

		_, wrote, err := EnsureClientConfig("https://server.example:8081", "server-key")
		require.ErrorContains(t, err, "name different servers")
		require.Empty(t, wrote)
		require.NoFileExists(t, filepath.Join(home, ".config", "seamless", "seamless.yaml"))

		// The same value in both places is agreement, not a conflict.
		t.Setenv("SEAMLESS_SERVER_URL", "https://server.example:8081/")
		_, wrote, err = EnsureClientConfig("https://server.example:8081", "server-key")
		require.NoError(t, err)
		require.NotEmpty(t, wrote)
	})
}

// sameClientConfig decides "already exactly this client" from "a different
// install in the way": the first writes nothing and succeeds, the second is the
// refusal.
func TestSameClientConfig(t *testing.T) {
	client := Config{Role: RoleClient, AdvertisedURL: "https://server.example:8081"}
	client.MCP.APIKey = "k"
	require.True(t, sameClientConfig(client, "https://server.example:8081", "k"))
	require.False(t, sameClientConfig(client, "https://other.example:8081", "k"),
		"a client of a different server is not agreement")
	require.False(t, sameClientConfig(client, "https://server.example:8081", "rotated"),
		"a different key is not agreement -- the file is the one thing we may not fix")
	require.False(t, sameClientConfig(Config{Addr: "127.0.0.1:8081"}, "http://127.0.0.1:8081", ""),
		"a server-role config is never adopted as a client config")
}

func TestClientAPIKey(t *testing.T) {
	t.Run("the flag beats the environment", func(t *testing.T) {
		t.Setenv("SEAMLESS_MCP_API_KEY", "from-env")
		key, source, err := clientAPIKey("from-env")
		require.NoError(t, err)
		require.Equal(t, "from-env", key)
		require.Equal(t, "--api-key", source)
	})

	t.Run("the environment fills an absent flag", func(t *testing.T) {
		t.Setenv("SEAMLESS_MCP_API_KEY", "from-env")
		key, source, err := clientAPIKey("")
		require.NoError(t, err)
		require.Equal(t, "from-env", key)
		require.Equal(t, "SEAMLESS_MCP_API_KEY", source)
	})

	t.Run("a set-but-empty environment key is an error, not an absence", func(t *testing.T) {
		t.Setenv("SEAMLESS_MCP_API_KEY", "")
		_, _, err := clientAPIKey("")
		require.ErrorContains(t, err, "set but empty")
		// Even with a flag: an empty env override would blank the file's key at
		// every later load.
		_, _, err = clientAPIKey("from-flag")
		require.ErrorContains(t, err, "set but empty")
	})

	t.Run("no key at all is an error -- a client cannot invent one", func(t *testing.T) {
		unsetenvForTest(t, "SEAMLESS_MCP_API_KEY")
		_, _, err := clientAPIKey("")
		require.ErrorContains(t, err, "no bearer key")
	})

	t.Run("two different keys are contradictory input, not a silent pick", func(t *testing.T) {
		t.Setenv("SEAMLESS_MCP_API_KEY", "from-env")
		_, _, err := clientAPIKey("from-flag")
		require.ErrorContains(t, err, "different keys")
	})
}
