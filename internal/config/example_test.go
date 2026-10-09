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

	// The update block: check_interval is spelled out at its default, and check
	// is only a comment, so it is unset exactly as the default is.
	require.Equal(t, Defaults().Update, cfg.Update)
	require.Nil(t, cfg.Update.Check, "check is commented out in the example: unset, the build decides")

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
