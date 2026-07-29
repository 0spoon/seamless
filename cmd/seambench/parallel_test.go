package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
)

// barrierTimeout bounds the overlap assertion. Serial execution can never
// satisfy the barrier, so without a deadline this test would HANG on a
// regression instead of failing -- and a hanging test in CI reads as
// infrastructure trouble rather than as the bug it is.
const barrierTimeout = 30 * time.Second

// --parallel-conditions is only worth its concurrency if the arms genuinely
// overlap, so prove the overlap rather than infer it from wall clock: each
// Seamless-ful arm announces itself as its daemon starts and then waits for its
// sibling. Both can only get through if both are inside their run at once, and a
// serial runner deadlocks against the barrier until the deadline.
func TestRunner_ParallelConditionsRunArmsAtTheSameTime(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	vanilla := bench.Condition{Name: "vanilla", Profile: bench.ProfileVanilla, Client: bench.ClientClaude}
	mechanism := bench.Condition{Name: "mechanism", Profile: bench.ProfileMechanism, Client: bench.ClientClaude}
	full := bench.Condition{Name: "full", Profile: bench.ProfileFull, Client: bench.ClientClaude}
	newArmFixture(t, base, vanilla, 0)
	newArmFixture(t, base, mechanism, 8099)
	newArmFixture(t, base, full, 8100)
	conds := []bench.Condition{vanilla, mechanism, full}

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))
	script := writeScript(t, t.TempDir(), "agent.sh", fakeAgentBody)

	// The two Seamless-ful arms rendezvous here; vanilla serves no daemon and
	// never reaches it. The last one in opens the gate for both.
	const participants = 2
	var (
		mu      sync.Mutex
		served  = map[string]int{}
		waiting int
		gate    = make(chan struct{})
	)

	var out bytes.Buffer
	r, _ := newTestRunner(t, base, conds, script, 1)
	r.parallelConditions = true
	r.w = &out
	r.serve = func(_ context.Context, a *arm, _ string) (stopFunc, error) {
		mu.Lock()
		served[a.condition.Name]++
		waiting++
		if waiting == participants {
			close(gate)
		}
		mu.Unlock()

		select {
		case <-gate:
			return func() {}, nil
		case <-time.After(barrierTimeout):
			return nil, fmt.Errorf("arm %s waited %s alone: the condition arms did not overlap",
				a.condition.Name, barrierTimeout)
		}
	}

	require.NoError(t, r.run(context.Background()))

	mu.Lock()
	require.Equal(t, map[string]int{"mechanism": 1, "full": 1}, served, "the vanilla arm needs no daemon")
	mu.Unlock()

	for _, cond := range conds {
		dir := filepath.Join(r.out, "fake-scenario", cond.Name, "run-01")
		rec, _, err := bench.LoadRun(dir)
		require.NoError(t, err, dir)
		require.Empty(t, rec.Error)
		require.Equal(t, cond, rec.Condition)
		// Wall clock from a contended run is not comparable with a serial one,
		// so the manifest has to say which it was.
		require.True(t, rec.Concurrent, "a concurrent run must be stamped Concurrent")
	}

	// Three goroutines share one writer: a run's header and its result must
	// still arrive as one unbroken block, or the log stops being readable
	// exactly when a failure makes it matter.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, cond := range conds {
		header := fmt.Sprintf("==> fake-scenario / %s run 1/1", cond.Name)
		at := -1
		for i, line := range lines {
			if strings.TrimSpace(line) == header {
				at = i
				break
			}
		}
		require.NotEqual(t, -1, at, "no header for %s in:\n%s", cond.Name, out.String())
		require.Less(t, at+1, len(lines), "header for %s has no result line", cond.Name)
		require.Contains(t, lines[at+1], "ok in",
			"another arm's output split the %s block:\n%s", cond.Name, out.String())
	}
}

// Serial stays the default, and a serial run's manifest must be unchanged --
// including the absence of the Concurrent stamp, which omitempty keeps out of
// the JSON entirely.
func TestRunner_SerialRunsAreNotStampedConcurrent(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	vanilla := bench.Condition{Name: "vanilla", Profile: bench.ProfileVanilla, Client: bench.ClientClaude}
	newArmFixture(t, base, vanilla, 0)

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))
	script := writeScript(t, t.TempDir(), "agent.sh", fakeAgentBody)

	r, _ := newTestRunner(t, base, []bench.Condition{vanilla}, script, 1)
	require.NoError(t, r.run(context.Background()))

	dir := filepath.Join(r.out, "fake-scenario", "vanilla", "run-01")
	rec, _, err := bench.LoadRun(dir)
	require.NoError(t, err)
	require.False(t, rec.Concurrent)

	raw, err := os.ReadFile(filepath.Join(dir, bench.RunManifestFile))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "concurrent", "a serial manifest gained a concurrent key")
}
