package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

// fakeScenario is a cheap stand-in for a real bench scenario: the seed exercises
// the demokit path for real without the ~9 memories and backdated plan the
// auth-refresh fixture builds (internal/bench already covers that seed).
func fakeScenario() bench.Scenario {
	return bench.Scenario{
		Name:   "fake-scenario",
		Prompt: "continue where we left off",
		Seed: func(s *demokit.Seeder, repoPath string) error {
			if err := s.EnsureProject("myapp", "myapp"); err != nil {
				return err
			}
			if repoPath == "" {
				return nil
			}
			return s.MapRepo(repoPath, "myapp")
		},
	}
}

// newTestRunner wires a runner over fake arms, a stub daemon, and a fake agent.
func newTestRunner(t *testing.T, base string, conds []bench.Condition, agentScript string, n int) (*runner, map[string]int) {
	t.Helper()
	served := map[string]int{}
	return &runner{
		base:       base,
		out:        filepath.Join(base, "runs"),
		version:    "v9.9.9-test",
		scenarios:  []bench.Scenario{fakeScenario()},
		conditions: conds,
		n:          n,
		timeout:    2 * time.Minute,
		agent:      agentOpts{command: agentScript, permissionMode: "bypassPermissions"},
		serve: func(_ context.Context, a *arm, logPath string) (stopFunc, error) {
			served[a.condition.Name]++
			require.True(t, a.seamless, "only a Seamless-ful arm may be served")
			return func() {}, nil
		},
		w: io.Discard,
	}, served
}

func TestRunner_FullLoopCapturesEveryCell(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	vanilla := bench.Condition{Name: "vanilla", Profile: bench.ProfileVanilla, Client: bench.ClientClaude}
	mechanism := bench.Condition{Name: "mechanism", Profile: bench.ProfileMechanism, Client: bench.ClientClaude}
	fv := newArmFixture(t, base, vanilla, 0)
	fm := newArmFixture(t, base, mechanism, 8099)

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	t.Setenv("FAKE_AGENT_ENVLOG", filepath.Join(logDir, "env"))
	script := writeScript(t, t.TempDir(), "agent.sh", fakeAgentBody)

	r, served := newTestRunner(t, base, []bench.Condition{vanilla, mechanism}, script, 2)
	require.NoError(t, r.run(context.Background()))

	require.Equal(t, map[string]int{"mechanism": 2}, served, "the vanilla arm needs no daemon")

	for _, cond := range []bench.Condition{vanilla, mechanism} {
		for i := 1; i <= 2; i++ {
			dir := filepath.Join(r.out, "fake-scenario", cond.Name, fmt.Sprintf("run-%02d", i))
			rec, arts, err := bench.LoadRun(dir)
			require.NoError(t, err, dir)

			require.Empty(t, rec.Error)
			require.Equal(t, 0, rec.ExitCode)
			require.Equal(t, "fake-scenario", rec.Scenario)
			require.Equal(t, cond, rec.Condition)
			require.Equal(t, i, rec.Run)
			require.Equal(t, "v9.9.9-test", rec.Version)
			require.Equal(t, fixtureModel, rec.Model)
			require.Equal(t, "continue where we left off", rec.Prompt)
			require.False(t, rec.StartedAt.IsZero())
			require.False(t, rec.EndedAt.Before(rec.StartedAt))
			require.Equal(t, bench.Metrics{
				Turns: 5, InputTokens: 115, OutputTokens: 50, CostUSD: 0.4242, DurationMS: 4200,
			}, rec.Metrics)

			// The frozen layout, end to end.
			require.NotEmpty(t, arts.RepoDir)
			require.NotEmpty(t, arts.Transcript)
			require.FileExists(t, filepath.Join(dir, bench.AgentLogFile))
			require.FileExists(t, filepath.Join(dir, bench.EventsFile))
			require.Contains(t, arts.RepoDiff, "ratelimit.go")
			require.Equal(t, 1, strings.Count(arts.RepoDiff, "+// touched by the agent"),
				"each run starts from the snapshot, so its diff carries one edit, not i of them")
			require.NoDirExists(t, filepath.Join(arts.RepoDir, ".git"))
			require.FileExists(t, filepath.Join(arts.RepoDir, "ratelimit.go"))

			var evs []core.Event
			b, err := os.ReadFile(filepath.Join(dir, bench.EventsFile))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(b, &evs))

			if cond.Profile == bench.ProfileVanilla {
				require.Empty(t, arts.DataDir, "a vanilla arm preserves no data dir")
			} else {
				require.NotEmpty(t, arts.DataDir)
				require.FileExists(t, filepath.Join(arts.DataDir, "seam.db"))
			}
		}
	}

	// The agent ran once per cell, in the right repo, under the arm's own home.
	args, err := os.ReadFile(filepath.Join(logDir, "args"))
	require.NoError(t, err)
	require.Equal(t, 4, strings.Count(string(args), "-p continue where we left off --output-format json --permission-mode bypassPermissions"))

	envLog, err := os.ReadFile(filepath.Join(logDir, "env"))
	require.NoError(t, err)
	// The agent's own $PWD is the symlink-resolved arm repo, so the cwd is
	// matched by suffix; the rest must be the arm's values exactly.
	require.Contains(t, string(envLog), "/arms/vanilla/myapp home="+fv.home+" config="+fv.configDir+" seamless=unset")
	require.Contains(t, string(envLog), "/arms/mechanism/myapp home="+fm.home+" config="+fm.configDir+" seamless="+fm.config)

	// Each arm's demo repo is left clean for the next invocation.
	for _, f := range []armFixture{fv, fm} {
		diff, err := gitDiff(context.Background(), f.repo, f.head)
		require.NoError(t, err)
		require.Contains(t, diff, "ratelimit.go", "the last run's work is still in the arm until the next run resets it")
	}
}

// twoStepScenario mirrors the handoff shape: evidence materialized for the
// first session only, a fresh working tree for the second.
func twoStepScenario() bench.Scenario {
	sc := fakeScenario()
	sc.Name = "fake-two-step"
	sc.Prompt = ""
	sc.Steps = []bench.Step{
		{Name: "investigate", Prompt: "find the root cause",
			Evidence: map[string]string{"logs/app.log": "the incident evidence\n"}},
		{Name: "fix", Prompt: "land the fix", FreshRepo: true},
	}
	return sc
}

// twoStepAgentBody plays both sessions and ASSERTS the boundary from the
// inside: session 1 must see the evidence, session 2 must see neither the
// evidence nor session 1's working-tree leftovers. A violated boundary exits
// non-zero, which surfaces as the run's Error.
const twoStepAgentBody = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$FAKE_AGENT_ARGS"
mkdir -p "$CLAUDE_CONFIG_DIR/projects/-fixture-myapp"
case "$2" in
"find the root cause")
	[ -f logs/app.log ] || exit 7
	echo 'scratch notes' >NOTES.md
	echo '// investigated' >>main.go
	printf '{"type":"assistant"}\n' >"$CLAUDE_CONFIG_DIR/projects/-fixture-myapp/fake-s1.jsonl"
	cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"duration_ms":1000,"num_turns":4,"session_id":"fake-s1","total_cost_usd":0.1,"usage":{"input_tokens":100,"output_tokens":10}}
JSON
	;;
"land the fix")
	[ ! -e logs/app.log ] || exit 8
	[ ! -e NOTES.md ] || exit 9
	printf 'package main\n' >fix.go
	printf '{"type":"assistant"}\n' >"$CLAUDE_CONFIG_DIR/projects/-fixture-myapp/fake-s2.jsonl"
	cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"duration_ms":2000,"num_turns":9,"session_id":"fake-s2","total_cost_usd":0.3,"usage":{"input_tokens":200,"output_tokens":30}}
JSON
	;;
*)
	exit 5
	;;
esac
`

func TestRunner_TwoStepRunKeepsTheBoundaries(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	mechanism := bench.Condition{Name: "mechanism", Profile: bench.ProfileMechanism, Client: bench.ClientClaude}
	newArmFixture(t, base, mechanism, 8099)

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	script := writeScript(t, t.TempDir(), "two-step.sh", twoStepAgentBody)

	r, _ := newTestRunner(t, base, []bench.Condition{mechanism}, script, 1)
	r.scenarios = []bench.Scenario{twoStepScenario()}
	require.NoError(t, r.run(context.Background()))

	dir := filepath.Join(r.out, "fake-two-step", "mechanism", "run-01")
	rec, arts, err := bench.LoadRun(dir)
	require.NoError(t, err)
	require.Empty(t, rec.Error, "the agent's own boundary assertions must hold")

	// The manifest: prompts on the steps, runner metrics summed on top.
	require.Empty(t, rec.Prompt)
	require.Len(t, rec.Steps, 2)
	require.Equal(t, "investigate", rec.Steps[0].Name)
	require.Equal(t, "fake-s1", rec.Steps[0].SessionID)
	require.Equal(t, 4, rec.Steps[0].Metrics.Turns)
	require.Equal(t, "fix", rec.Steps[1].Name)
	require.Equal(t, "fake-s2", rec.Steps[1].SessionID)
	require.Equal(t, 13, rec.Metrics.Turns)
	require.InDelta(t, 0.4, rec.Metrics.CostUSD, 1e-9)

	// Session 1's artifacts: its own diff (evidence-free, leftovers included)
	// and its own transcript.
	require.Len(t, arts.Steps, 1)
	require.Contains(t, arts.Steps[0].RepoDiff, "main.go")
	require.Contains(t, arts.Steps[0].RepoDiff, "NOTES.md")
	require.NotContains(t, arts.Steps[0].RepoDiff, "app.log")
	require.NotContains(t, arts.Steps[0].RepoDiff, "fix.go")
	b, err := os.ReadFile(arts.Steps[0].Transcript)
	require.NoError(t, err)
	require.Contains(t, filepath.Base(arts.Steps[0].Transcript), "transcript")
	require.NotEmpty(t, b)

	// The final artifacts are session 2's alone: the fresh-repo boundary
	// dropped session 1's tree, and the evidence never reaches a capture.
	require.Contains(t, arts.RepoDiff, "fix.go")
	require.NotContains(t, arts.RepoDiff, "main.go")
	require.NotContains(t, arts.RepoDiff, "app.log")
	require.NotContains(t, arts.RepoDiff, "NOTES.md")
	require.FileExists(t, filepath.Join(arts.RepoDir, "fix.go"))
	require.NoFileExists(t, filepath.Join(arts.RepoDir, "NOTES.md"))
	require.NoDirExists(t, filepath.Join(arts.RepoDir, "logs"))
	require.FileExists(t, filepath.Join(dir, bench.StepDirName(1), bench.AgentLogFile))

	// Both sessions ran, in order.
	args, err := os.ReadFile(filepath.Join(logDir, "args"))
	require.NoError(t, err)
	require.Regexp(t, `(?s)find the root cause.*land the fix`, string(args))
}

func TestRunner_AFailedStepAbortsTheRemainingSteps(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	mechanism := bench.Condition{Name: "mechanism", Profile: bench.ProfileMechanism, Client: bench.ClientClaude}
	newArmFixture(t, base, mechanism, 8099)

	logDir := t.TempDir()
	t.Setenv("FAKE_AGENT_ARGS", filepath.Join(logDir, "args"))
	script := writeScript(t, t.TempDir(), "boom.sh", "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>\"$FAKE_AGENT_ARGS\"\nexit 3\n")

	r, _ := newTestRunner(t, base, []bench.Condition{mechanism}, script, 1)
	r.scenarios = []bench.Scenario{twoStepScenario()}
	err := r.run(context.Background())
	require.ErrorContains(t, err, "every run failed")

	dir := filepath.Join(r.out, "fake-two-step", "mechanism", "run-01")
	rec, _, err := bench.LoadRun(dir)
	require.NoError(t, err)
	require.Contains(t, rec.Error, "step 1/2 (investigate)")
	require.Len(t, rec.Steps, 1, "a step the run never reached is not recorded")

	args, err := os.ReadFile(filepath.Join(logDir, "args"))
	require.NoError(t, err)
	require.NotContains(t, string(args), "land the fix", "the second session must never start")
}

func TestValidateScenario(t *testing.T) {
	ok := twoStepScenario()
	require.NoError(t, validateScenario(ok))

	both := ok
	both.Prompt = "also a prompt"
	require.ErrorContains(t, validateScenario(both), "both Prompt and Steps")

	recall := ok
	recall.RequiresRecall = true
	require.ErrorContains(t, validateScenario(recall), "multi-step")

	escape := fakeScenario()
	escape.Prompt = ""
	escape.Steps = []bench.Step{{Prompt: "p", Evidence: map[string]string{"../out": "x"}}}
	require.ErrorContains(t, validateScenario(escape), "escapes the repo")

	empty := fakeScenario()
	empty.Prompt = ""
	require.ErrorContains(t, validateScenario(empty), "no prompt")
}

func TestRunner_FailedRunsAreRecordedAndTheSuiteContinues(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	vanilla := bench.Condition{Name: "vanilla", Profile: bench.ProfileVanilla, Client: bench.ClientClaude}
	newArmFixture(t, base, vanilla, 0)

	script := writeScript(t, t.TempDir(), "boom.sh", "#!/usr/bin/env bash\necho 'no api key' >&2\nexit 3\n")
	r, _ := newTestRunner(t, base, []bench.Condition{vanilla}, script, 2)

	// Every run failed, so the suite says so -- after running them all.
	err := r.run(context.Background())
	require.ErrorContains(t, err, "every run failed")

	for i := 1; i <= 2; i++ {
		dir := filepath.Join(r.out, "fake-scenario", "vanilla", fmt.Sprintf("run-%02d", i))
		rec, arts, err := bench.LoadRun(dir)
		require.NoError(t, err)
		require.Equal(t, 3, rec.ExitCode)
		require.Contains(t, rec.Error, "agent exited 3")
		// A failed run is still captured: the grader decides what a partial
		// capture means.
		require.NotEmpty(t, arts.RepoDir)
		require.FileExists(t, filepath.Join(dir, bench.EventsFile))
		log, err := os.ReadFile(filepath.Join(dir, bench.AgentLogFile))
		require.NoError(t, err)
		require.Contains(t, string(log), "no api key")
	}
}

func TestRunner_RequiresRecallTakesTheHookPathNotTheAgent(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	mechanism := bench.Condition{Name: "mechanism", Profile: bench.ProfileMechanism, Client: bench.ClientClaude}
	f := newArmFixture(t, base, mechanism, 8099)

	srv := recallServer(t, "<seam-recall>Seam has possibly relevant memories:</seam-recall>", http.StatusOK)
	defer srv.Close()

	a, err := loadArm(f.envFile, mechanism)
	require.NoError(t, err)
	a.snapshot = f.head
	a.url = srv.URL

	// An agent command that would fail loudly if the runner reached for it.
	r, _ := newTestRunner(t, base, []bench.Condition{mechanism},
		filepath.Join(t.TempDir(), "must-not-run"), 1)

	sc := fakeScenario()
	sc.RequiresRecall = true
	out := r.execute(context.Background(), sc, a, t.TempDir())
	require.NoError(t, out.err)
	require.Equal(t, 0, out.exitCode)
}

func TestSelectScenarios(t *testing.T) {
	all, err := selectScenarios("")
	require.NoError(t, err)
	require.NotEmpty(t, all)
	require.Equal(t, scenarioNames(), namesOf(all))

	one, err := selectScenarios(" cookie-hardening ")
	require.NoError(t, err)
	require.Equal(t, []string{"cookie-hardening"}, namesOf(one))

	_, err = selectScenarios("cookie-hardening,cookie-hardening")
	require.ErrorContains(t, err, "duplicate")

	_, err = selectScenarios("nope")
	require.ErrorContains(t, err, "unknown scenario")

	_, err = selectScenarios(",")
	require.ErrorContains(t, err, "selected nothing")
}

func TestDefaultConditionList_DerivesFromTheCanonicalSet(t *testing.T) {
	want := make([]string, 0, len(bench.DefaultConditions()))
	for _, c := range bench.DefaultConditions() {
		want = append(want, c.Name)
	}
	require.Equal(t, strings.Join(want, ","), defaultConditionList())

	// The default list is exactly what the harness would accept.
	conds, err := bench.ParseConditions(defaultConditionList())
	require.NoError(t, err)
	require.Len(t, conds, len(want))
}

func namesOf(scs []bench.Scenario) []string {
	out := make([]string, len(scs))
	for i, sc := range scs {
		out[i] = sc.Name
	}
	return out
}
