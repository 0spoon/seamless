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
	"strings"
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

// e2eGradePort keeps the composition test's arms clear of the plumbing test's.
const e2eGradePort = 8150

// The two halves of plan:seambench are built against a frozen on-disk contract
// and never call each other, so nothing in either package proves they compose.
// This does: it runs the REAL runner over the REAL harness and grades what it
// captured, with two fake agents that differ only in the fix they ship.
//
// The vanilla arm carries the verdict, and that is the point rather than a
// limitation: it preserves no data dir, so its verdict rests on the repo
// assertions alone -- exactly the control arm the uplift metric subtracts. On
// the Seamless-ful arm a fake agent fires no hooks and calls no MCP tool, so
// its briefing gate legitimately fails; what that arm proves is that the event
// layer opened and read the data dir the runner copied (WAL sidecars included).
func TestE2E_RunnerArtifactsGradeThroughTheGrader(t *testing.T) {
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
		port:       e2eGradePort,
		conditions: conds,
		w:          os.Stdout,
	}))

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))
	scripts := t.TempDir()

	cases := []struct {
		name     string
		body     string
		wantPass bool
	}{
		{"shared-storage limiter", fakeAgentSharedLimiter, true},
		{"per-process map limiter", fakeAgentInMemoryLimiter, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(base, "runs", strings.ReplaceAll(tc.name, " ", "-"))
			r := &runner{
				base:       base,
				out:        out,
				version:    repoVersion(ctx, repoRoot),
				scenarios:  []bench.Scenario{mustScenario(t, "auth-refresh")},
				conditions: conds,
				n:          1,
				timeout:    5 * time.Minute,
				agent:      agentOpts{command: writeScript(t, scripts, strings.ReplaceAll(tc.name, " ", "-")+".sh", tc.body)},
				serve:      execServe(filepath.Join(repoRoot, "bin", "seamlessd")),
				w:          io.Discard,
			}
			require.NoError(t, r.run(ctx))

			_, res, err := bench.GradeRunDir(ctx, filepath.Join(out, "auth-refresh", "vanilla", "run-01"), nil)
			require.NoError(t, err)
			details := strings.Join(res.Details, "\n")
			require.Equal(t, tc.wantPass, res.Pass, "vanilla verdict, details:\n%s", details)
			require.Contains(t, details, "no data dir preserved")

			_, mres, err := bench.GradeRunDir(ctx, filepath.Join(out, "auth-refresh", "mechanism", "run-01"), nil)
			require.NoError(t, err)
			mdetails := strings.Join(mres.Details, "\n")
			require.NotContains(t, mdetails, "no data dir preserved",
				"the event layer never opened the runner's data-dir copy")
			require.Contains(t, mdetails, "SessionStart briefing injected")
		})
	}
}

// fakeAgentSharedLimiter ships the right answer: a limiter on the refresh path
// whose counter lives in Redis, so it survives an instance restart and is
// shared across instances (memory rate-limit-not-in-memory).
const fakeAgentSharedLimiter = fakeAgentPreamble + `
cat >ratelimit.go <<'GO'
package main

import (
	"net/http"

	"github.com/redis/go-redis/v9"
)

var refreshLimiter *redis.Client

// allowRefresh rate-limits POST /auth/refresh per IP. The counter lives in
// Redis so every instance behind the load balancer sees the same tally.
func allowRefresh(w http.ResponseWriter, r *http.Request, ip string) bool {
	n, err := refreshLimiter.Incr(r.Context(), "/auth/refresh:"+ip).Result()
	if err != nil || n > 10 {
		w.WriteHeader(http.StatusTooManyRequests)
		return false
	}
	return true
}
GO
` + fakeAgentResult

// fakeAgentInMemoryLimiter ships the wrong answer the scenario exists to
// catch: a limiter whose counter is a per-process map, which resets on every
// deploy and is per-instance behind a load balancer.
const fakeAgentInMemoryLimiter = fakeAgentPreamble + `
cat >ratelimit.go <<'GO'
package main

import (
	"net/http"
	"sync"
)

var (
	refreshMu     sync.Mutex
	refreshCounts = map[string]int{}
)

// allowRefresh rate-limits POST /auth/refresh per IP.
func allowRefresh(w http.ResponseWriter, r *http.Request, ip string) bool {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	refreshCounts[ip]++
	if refreshCounts[ip] > 10 {
		w.WriteHeader(http.StatusTooManyRequests)
		return false
	}
	return true
}
GO
` + fakeAgentResult

// fakeAgentPreamble and fakeAgentResult bracket the part that differs: the
// scaffolding (transcript, argv/env log) and the --output-format json result
// are the same for both fixes.
const fakeAgentPreamble = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$FAKE_AGENT_ARGS"
printf 'cwd=%s\n' "$PWD" >>"$FAKE_AGENT_ENVLOG"
mkdir -p "$CLAUDE_CONFIG_DIR/projects/-fixture-myapp"
printf '{"type":"user"}\n{"type":"assistant"}\n' >"$CLAUDE_CONFIG_DIR/projects/-fixture-myapp/fake-session.jsonl"
`

const fakeAgentResult = `
cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"duration_ms":4200,"num_turns":5,"session_id":"fake-session","total_cost_usd":0.4242,"usage":{"input_tokens":100,"output_tokens":50}}
JSON
`

func mustScenario(t *testing.T, name string) bench.Scenario {
	t.Helper()
	sc, ok := bench.ScenarioByName(name)
	require.True(t, ok, "unknown scenario %s", name)
	return sc
}
