package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clearSeamlessEnv unsets every SEAMLESS_* variable the developer's shell
// carries, so a test comparing against Defaults() is not comparing against
// their environment instead.
func clearSeamlessEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SEAMLESS_") {
			unsetEnv(t, k)
		}
	}
}

// TestExampleConfigLoads loads the shipped seamless.yaml.example through the
// real loader. `make install` seeds a fresh config from it and the docs print
// it verbatim, so it must load -- KnownFields turns a key renamed in code but
// not here into a daemon that will not start -- and the promise in its header,
// "every key, at its default", must hold.
func TestExampleConfigLoads(t *testing.T) {
	clearSeamlessEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

	path := filepath.Join("..", "..", "seamless.yaml.example")
	cfg, err := LoadFrom(path)
	require.NoError(t, err)

	// The update block: check_interval, max_defer and min_age are spelled out
	// at their defaults, and check and auto are only comments, so each is
	// unset exactly as its default is. Every key and its env override is
	// written out, set or not.
	require.Equal(t, Defaults().Update, cfg.Update)
	require.Nil(t, cfg.Update.Check, "check is commented out in the example: unset, the build decides")
	require.Nil(t, cfg.Update.Auto, "auto is commented out in the example: unset, which means on")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, line := range []string{
		"  # check: false\n", "  check_interval: 6h\n", "  # auto: false\n", "  max_defer: 24h\n", "  min_age: 24h\n",
		"  # env: SEAMLESS_UPDATE_CHECK\n", "  # env: SEAMLESS_UPDATE_CHECK_INTERVAL\n", "  # env: SEAMLESS_UPDATE_AUTO\n",
		"  # env: SEAMLESS_UPDATE_MAX_DEFER\n", "  # env: SEAMLESS_UPDATE_MIN_AGE\n",
	} {
		require.Contains(t, text, line)
	}

	// Every other key is at its default too, modulo what loading legitimately
	// does to a default: data_dir's leading ~ expands, and the example's
	// `allowed_hosts: []` decodes to an empty list where the default is nil --
	// both mean "no extra hosts".
	resolvedHome, err := os.UserHomeDir()
	require.NoError(t, err)
	want := Defaults()
	want.DataDir = filepath.Join(resolvedHome, ".seamless")
	want.sourcePath = path
	require.Empty(t, cfg.AllowedHosts)
	cfg.AllowedHosts = nil
	require.Equal(t, want, cfg)
}
