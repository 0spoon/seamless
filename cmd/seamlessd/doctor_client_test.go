package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
)

// writeClientConfig writes a role: client seamless.yaml naming dataDir, points
// $SEAMLESS_CONFIG at it, and moves $HOME into the same throwaway tree so the
// hook, skill and Codex checks inspect the fixture rather than the developer's
// real client configuration.
func writeClientConfig(t *testing.T, dir, serverURL, dataDir string) {
	t.Helper()
	path := filepath.Join(dir, "seamless.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join([]string{
		"role: client",
		"server_url: " + serverURL,
		"data_dir: " + dataDir,
		"mcp:",
		"  api_key: doctor-client-test-key",
		"",
	}, "\n")), 0o600))
	t.Setenv("SEAMLESS_CONFIG", path)
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))
}

// The bug this pins: doctor opened the database unconditionally, and store.Open
// CREATES and migrates. On a client that is not a useless check but a
// destructive one -- it mints the seam.db whose absence is what "role: client"
// means, after which every later run finds a database and reports it healthy,
// and an owner looking for their corpus finds an empty one on the wrong machine.
//
// data_dir is named explicitly (rather than left to the default) so the
// assertion is about the path doctor would have used, not about a directory that
// happens not to exist.
func TestDoctor_ClientRoleOpensNoDatabase(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "seamless-data")
	// Port 1 is reserved and nothing listens there: the reachability check fails
	// without a live server, which is also the state a client is most often
	// diagnosed in.
	writeClientConfig(t, dir, "http://127.0.0.1:1", dataDir)

	var runErr error
	out := captureStdout(t, func() error {
		runErr = doctor(nil)
		return nil
	})

	require.NoDirExists(t, dataDir, "a client doctor must not create the data dir")
	require.NoFileExists(t, filepath.Join(dataDir, "seam.db"))
	// The whole tree, not just the configured path: nothing anywhere may be a
	// database this run brought into existence.
	require.NoError(t, filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		require.NotContains(t, filepath.Base(path), "seam.db", "doctor created a database at %s", path)
		return nil
	}))

	// The unreachable server is the failure, and it is reported as one: on a
	// client there is no benign "the daemon is just stopped" reading.
	require.Error(t, runErr)
	require.Contains(t, out, "[info] role: client: this install runs no daemon and dials http://127.0.0.1:1")
	require.Contains(t, out, "[fail] server_url: http://127.0.0.1:1 is not answering")
}

// The skip list, asserted by absence. Each of these reads the local seam.db or
// describes daemon-side work, so a client that printed any of them would be
// reporting on a machine that is somewhere else.
func TestDoctor_ClientRoleSkipsTheServerOnlyChecks(t *testing.T) {
	dir := t.TempDir()
	writeClientConfig(t, dir, "http://127.0.0.1:1", filepath.Join(dir, "seamless-data"))

	out := captureStdout(t, func() error {
		_ = doctor(nil)
		return nil
	})

	for _, absent := range []string{
		"database:", "schema version:", "repo map:", "remote sessions:",
		"feature skills:", "gardener:", "data_dir:", "embedder:", "llm:",
		"bind:", "tls:",
	} {
		require.NotContains(t, out, absent, "a client install has no %q to report", absent)
	}

	// And the checks it DOES run, so the test cannot be satisfied by a doctor
	// that prints nothing: config, the key, reachability, the tool count, and
	// the client-side hook wiring.
	for _, present := range []string{"config:", "mcp.api_key:", "server_url:", "mcp_tools:", "hooks:"} {
		require.Contains(t, out, present)
	}
}

// clientChecks is the list, independent of config.Load: it must never reach for
// a database handle, which is what makes the skip structural rather than a guard
// each future check has to remember.
func TestClientChecks_TakesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	writeClientConfig(t, dir, "http://127.0.0.1:1", filepath.Join(dir, "seamless-data"))

	cfg, err := config.Load()
	require.NoError(t, err)
	require.True(t, cfg.IsClient())

	checks := clientChecks(cfg)
	require.NotEmpty(t, checks)
	names := make([]string, 0, len(checks))
	for _, c := range checks {
		names = append(names, c.name)
	}
	require.Contains(t, names, "role")
	require.Contains(t, names, "server_url")
	require.Contains(t, names, "mcp.api_key")
	require.NotContains(t, names, "database")
	require.NotContains(t, names, "codex hook activity", "the event log lives on the server")
}
