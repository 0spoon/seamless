//go:build seambench_e2e

// The one test that uses the REAL fixture harness and REAL daemons, behind a
// build tag so `make test` never depends on either:
//
//	go test -tags seambench_e2e ./cmd/seambench -run TestE2E -v
//
// It still uses the fake agent -- a real `claude -p` take costs real tokens and
// is the owner's monitored, manual step -- so what it proves is the plumbing the
// hermetic tests stub out: that harness.sh --mode bench writes env files this
// runner reads, and that an arm's daemon comes up, serves, and shuts down
// around a run.

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
)

// e2ePort is well clear of the live 8081 and of the harness default (8099), so
// a manual fixture can be standing while this runs.
const e2ePort = 8140

func TestE2E_RealHarnessAndDaemon(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoRoot, err := resolveRepoRoot(ctx, "")
	require.NoError(t, err)

	base := t.TempDir()
	conds, err := bench.ParseConditions("vanilla,mechanism")
	require.NoError(t, err)
	require.NoError(t, buildArms(ctx, harnessOpts{
		repoRoot:   repoRoot,
		base:       base,
		port:       e2ePort,
		conditions: conds,
		w:          os.Stdout,
	}))

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))

	r := &runner{
		base:       base,
		out:        filepath.Join(base, "runs"),
		version:    repoVersion(ctx, repoRoot),
		scenarios:  []bench.Scenario{mustScenario(t, "auth-refresh")},
		conditions: conds,
		n:          1,
		timeout:    5 * time.Minute,
		agent:      agentOpts{command: writeScript(t, t.TempDir(), "agent.sh", fakeAgentBody)},
		serve:      execServe(filepath.Join(repoRoot, "bin", "seamlessd")),
		w:          io.Discard,
	}
	require.NoError(t, r.run(ctx))

	for _, cond := range conds {
		dir := filepath.Join(r.out, "auth-refresh", cond.Name, "run-01")
		rec, arts, err := bench.LoadRun(dir)
		require.NoError(t, err)
		require.Empty(t, rec.Error)
		require.Contains(t, arts.RepoDiff, "ratelimit.go")
		require.NotEmpty(t, arts.RepoDir)
		require.NotEmpty(t, arts.Transcript)
		if cond.Profile == bench.ProfileVanilla {
			require.Empty(t, arts.DataDir)
		} else {
			require.FileExists(t, filepath.Join(arts.DataDir, "seam.db"))
			require.DirExists(t, filepath.Join(arts.DataDir, "memory", "myapp"))
		}
	}
}

func mustScenario(t *testing.T, name string) bench.Scenario {
	t.Helper()
	sc, ok := bench.ScenarioByName(name)
	require.True(t, ok, "unknown scenario %s", name)
	return sc
}
