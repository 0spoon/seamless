//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/core"
	"github.com/arctop/seamless/internal/update"
)

// The Linux spawner's end-to-end check runs only in a throwaway VM, driven by
// a script: a user unit named seamless.service runs this test binary as the
// daemon (TestLinuxSpawnE2E_Daemon), which spawns the updater with the real
// systemdRunSpawner. The updater's executable is a wrapper script that execs
// this binary again as TestLinuxSpawnE2E_Updater: a transient unit starts from
// the user manager's environment, so the wrapper, not the daemon, names the
// work dir. Both write JSON reports there for the driver to check.
const (
	spawnE2EEnv     = "SEAMLESS_SPAWN_E2E"      // the work dir
	spawnE2ERoleEnv = "SEAMLESS_SPAWN_E2E_ROLE" // daemon or updater
)

// e2eDir is the work dir when this process plays role, else the test skips.
func e2eDir(t *testing.T, role string) string {
	t.Helper()
	dir := os.Getenv(spawnE2EEnv)
	if dir == "" || os.Getenv(spawnE2ERoleEnv) != role {
		t.Skip("the Linux spawner's end-to-end check runs only from its VM driver")
	}
	return dir
}

// e2eReport is what either role says about itself.
type e2eReport struct {
	Role         string            `json:"role"`
	PID          int               `json:"pid"`
	Cgroup       string            `json:"cgroup"`
	InUnit       bool              `json:"in_unit"`
	OutsideCheck string            `json:"outside_check"` // "ok" or the refusal
	Supervised   bool              `json:"supervised"`
	SupReason    string            `json:"supervised_reason"`
	Attempt      string            `json:"attempt"`
	SpawnErr     string            `json:"spawn_err,omitempty"`
	SpawnMillis  int64             `json:"spawn_ms,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env"`
}

// e2eIdentity fills what both roles report about themselves.
func e2eIdentity(t *testing.T, role string) e2eReport {
	t.Helper()
	cg, err := os.ReadFile("/proc/self/cgroup")
	require.NoError(t, err)
	r := e2eReport{Role: role, PID: os.Getpid(), Cgroup: strings.TrimSpace(string(cg)), InUnit: inSystemdUnit(cg, systemdUnit), Env: map[string]string{}}
	r.OutsideCheck = "ok"
	if err := outsideServiceCheck("linux", os.ReadFile); err != nil {
		r.OutsideCheck = err.Error()
	}
	r.Supervised, r.SupReason = probeSupervised(realInstallProbeEnv())
	for _, name := range []string{"HTTPS_PROXY", "SEAMLESS_CONFIG", "SEAMLESS_E2E_DAEMON_ONLY", "INVOCATION_ID", "XDG_RUNTIME_DIR", "PATH"} {
		if v, ok := os.LookupEnv(name); ok {
			r.Env[name] = v
		}
	}
	return r
}

// writeE2EReport writes r whole to dir/name.
func writeE2EReport(t *testing.T, dir, name string, r e2eReport) {
	t.Helper()
	raw, err := json.MarshalIndent(r, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path+".tmp", raw, 0o600))
	require.NoError(t, os.Rename(path+".tmp", path))
}

// TestLinuxSpawnE2E_Daemon plays the daemon inside seamless.service: it
// reports where it runs, spawns one updater through systemdRunSpawner, and
// stays up until systemd stops it.
func TestLinuxSpawnE2E_Daemon(t *testing.T) {
	dir := e2eDir(t, "daemon")
	r := e2eIdentity(t, "daemon")
	id, err := core.NewID()
	require.NoError(t, err)
	r.Attempt = id
	req := update.SpawnRequest{AttemptID: id, From: mustVersion(t, "0.7.2"), To: mustVersion(t, "0.7.3"), Why: update.WhyAuto}
	sp := osUpdateSpawner(spawnTarget{
		exe:        filepath.Join(dir, "updater.sh"),
		configPath: filepath.Join(dir, "seamless.yaml"),
		dataDir:    dir,
	}, nil)
	start := time.Now()
	if err := sp.Spawn(context.Background(), req); err != nil {
		r.SpawnErr = err.Error()
	}
	r.SpawnMillis = time.Since(start).Milliseconds()
	writeE2EReport(t, dir, "daemon-"+id+".json", r)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
}

// TestLinuxSpawnE2E_Updater plays the updater in its transient unit: it
// reports where it runs and with which environment and arguments, then waits
// for the driver to restart seamless.service and reports that it outlived
// the restart.
func TestLinuxSpawnE2E_Updater(t *testing.T) {
	dir := e2eDir(t, "updater")
	r := e2eIdentity(t, "updater")
	r.Args = flag.Args()
	for i, a := range r.Args {
		if a == "--attempt" && i+1 < len(r.Args) {
			r.Attempt = r.Args[i+1]
		}
	}
	require.NotEmpty(t, r.Attempt)
	writeE2EReport(t, dir, "updater-"+r.Attempt+".json", r)

	restarted := filepath.Join(dir, "restarted")
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(restarted); err == nil {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "survived-"+r.Attempt), []byte("ok\n"), 0o600))
				return
			}
		case <-deadline:
			t.Fatal("the driver never restarted the service")
		}
	}
}
