package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/bench"
)

// benchRun synthesizes one graded run for the report fixtures. The aggregation
// itself is proven in internal/bench; what these tests cover is that the table
// shows what the aggregation computed, including the parts a reader would
// misinterpret if they were missing.
func benchRun(scenario, cond string, p bench.Profile, version string, i int, status bench.RunStatus, pass bool) bench.RunResult {
	return bench.RunResult{
		Record: bench.RunRecord{
			Scenario:  scenario,
			Condition: bench.Condition{Name: cond, Profile: p, Client: bench.ClientClaude},
			Run:       i,
			Version:   version,
			Metrics:   bench.Metrics{Turns: 8 + i, InputTokens: 1000 * i, CostUSD: 0.42},
		},
		Dir:   version + "/" + scenario + "/" + cond + "/run-0" + string(rune('0'+i)),
		Grade: bench.Grade{Schema: bench.GradeSchema, Status: status, Pass: pass, Metrics: bench.Metrics{ToolCalls: 3 * i}},
	}
}

// twoVersionResults: mechanism helps at v1 and stops helping at v2, while the
// control holds steady -- a clean regression.
func twoVersionResults() bench.Results {
	var runs []bench.RunResult
	add := func(version, cond string, p bench.Profile, passes ...bool) {
		for i, pass := range passes {
			runs = append(runs, benchRun("cookie-hardening", cond, p, version, i+1, bench.StatusGraded, pass))
		}
	}
	add("v1", "vanilla", bench.ProfileVanilla, false, false)
	add("v1", "mechanism", bench.ProfileMechanism, true, true)
	add("v2", "vanilla", bench.ProfileVanilla, false, false)
	add("v2", "mechanism", bench.ProfileMechanism, false, false)
	res := bench.NewResults("/tmp/runs", runs)
	res.Baseline, res.Candidate = "v1", "v2"
	return res
}

func render(t *testing.T, res bench.Results, baseline, candidate string) string {
	t.Helper()
	var b bytes.Buffer
	renderReport(&b, res, baseline, candidate)
	return b.String()
}

func TestRenderReport_PerScenarioUpliftAndCounts(t *testing.T) {
	runs := []bench.RunResult{
		benchRun("cookie-hardening", "vanilla", bench.ProfileVanilla, "v1", 1, bench.StatusGraded, false),
		benchRun("cookie-hardening", "vanilla", bench.ProfileVanilla, "v1", 2, bench.StatusGraded, false),
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 1, bench.StatusGraded, true),
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 2, bench.StatusGraded, false),
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 3, bench.StatusFailedToRun, false),
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 4, bench.StatusUngradeable, false),
	}
	out := render(t, bench.NewResults("/tmp/runs", runs), "", "")

	require.Contains(t, out, "6 total: 4 graded, 1 failed to run, 1 ungradeable")
	require.Contains(t, out, "control:   vanilla")
	require.Contains(t, out, "NOT a failed verdict")
	require.Contains(t, out, "smallest cell: n=2")

	// vanilla 0/2, mechanism 1/2 -> +0.50, with the two lost runs visible.
	require.Regexp(t, `cookie-hardening\s+vanilla\s+0\.00 \(0/2\)\s+-\s+0\s+0`, out)
	require.Regexp(t, `cookie-hardening\s+mechanism\s+0\.50 \(1/2\)\s+\+0\.50\s+1\s+1`, out)

	// Metrics carry their spread, never a bare mean.
	require.Contains(t, out, "metrics (mean +- sd over graded runs)")
	require.Regexp(t, `cookie-hardening\s+mechanism\s+2\s+9\.50 \+- 0\.7071`, out)
}

func TestRenderReport_AggregateRowOnlyWithMoreThanOneScenario(t *testing.T) {
	one := []bench.RunResult{
		benchRun("cookie-hardening", "vanilla", bench.ProfileVanilla, "v1", 1, bench.StatusGraded, false),
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 1, bench.StatusGraded, true),
	}
	require.NotContains(t, render(t, bench.NewResults("", one), "", ""), aggregateRow)

	two := append(one,
		benchRun("cache-landmine", "vanilla", bench.ProfileVanilla, "v1", 1, bench.StatusGraded, true),
		benchRun("cache-landmine", "mechanism", bench.ProfileMechanism, "v1", 1, bench.StatusGraded, true),
	)
	out := render(t, bench.NewResults("", two), "", "")
	require.Regexp(t, `ALL\s+mechanism\s+1\.00 \(2/2\)\s+\+0\.50`, out,
		"the aggregate pools graded runs across scenarios")
}

// Without a control arm there is no uplift, and the report says so instead of
// printing an absolute pass-rate in the uplift column.
func TestRenderReport_NoControlArm(t *testing.T) {
	runs := []bench.RunResult{
		benchRun("cookie-hardening", "mechanism", bench.ProfileMechanism, "v1", 1, bench.StatusGraded, true),
		benchRun("cookie-hardening", "full", bench.ProfileFull, "v1", 1, bench.StatusGraded, false),
	}
	out := render(t, bench.NewResults("", runs), "", "")
	require.Contains(t, out, "control:   NONE -- no arm with the vanilla profile")
	require.Regexp(t, `cookie-hardening\s+mechanism\s+1\.00 \(1/1\)\s+n/a`, out)
	require.NotContains(t, out, "+1.00")
}

func TestRenderReport_VersionDeltaAndControlCalibration(t *testing.T) {
	out := render(t, twoVersionResults(), "v1", "v2")

	require.Contains(t, out, "=== version v1 (baseline) ===")
	require.Contains(t, out, "=== version v2 (candidate) ===")
	require.Contains(t, out, "=== version delta: v1 -> v2 ===")
	require.Regexp(t, `cookie-hardening\s+mechanism\s+\+1\.00\s+\+0\.00\s+-1\.00\s+REGRESSION`, out)
	require.Contains(t, out, "control calibration")
	require.Contains(t, out, "v1: 0.00 (0/2)  ->  v2: 0.00 (0/2)   (change +0.00)")
	require.NotContains(t, out, "The control moved")
}

// Both arms dropping together is the model having a bad day, not a Seamless
// regression: the delta is 0 and the control calibration is what says why.
func TestRenderReport_ControlDriftIsCalledOut(t *testing.T) {
	var runs []bench.RunResult
	add := func(version, cond string, p bench.Profile, passes ...bool) {
		for i, pass := range passes {
			runs = append(runs, benchRun("cookie-hardening", cond, p, version, i+1, bench.StatusGraded, pass))
		}
	}
	add("v1", "vanilla", bench.ProfileVanilla, true, true)
	add("v1", "mechanism", bench.ProfileMechanism, true, true)
	add("v2", "vanilla", bench.ProfileVanilla, false, false)
	add("v2", "mechanism", bench.ProfileMechanism, false, false)

	out := render(t, bench.NewResults("", runs), "v1", "v2")
	require.Regexp(t, `cookie-hardening\s+mechanism\s+\+0\.00\s+\+0\.00\s+\+0\.00`, out)
	require.Equal(t, 1, strings.Count(out, "REGRESSION"),
		"only the legend mentions it: nothing regressed")
	require.Contains(t, out, "v1: 1.00 (2/2)  ->  v2: 0.00 (0/2)   (change -1.00)")
	require.Contains(t, out, "The control moved")
}

// A control-only matrix has no uplift to compare. An empty table under a
// heading reads as a bug, so the report says what is missing.
func TestRenderReport_VersionDeltaWithOnlyTheControlArm(t *testing.T) {
	runs := []bench.RunResult{
		benchRun("cookie-hardening", "vanilla", bench.ProfileVanilla, "v1", 1, bench.StatusGraded, true),
		benchRun("cookie-hardening", "vanilla", bench.ProfileVanilla, "v2", 1, bench.StatusGraded, true),
	}
	out := render(t, bench.NewResults("", runs), "v1", "v2")
	require.Contains(t, out, "nothing to compare: vanilla is the only arm")
	require.Contains(t, out, "--conditions vanilla,mechanism")
}

func TestRenderReport_TwoVersionsWithoutAKnownPair(t *testing.T) {
	res := twoVersionResults()
	res.Baseline, res.Candidate = "", ""
	out := render(t, res, "", "")
	require.Contains(t, out, "2 versions are present but the tree does not record which is the baseline")
	require.NotContains(t, out, "version delta:")
}

func TestResolveVersionPair(t *testing.T) {
	res := twoVersionResults()
	tests := []struct {
		name               string
		recBase, recCand   string
		flagBase, flagCand string
		wantBase, wantCand string
	}{
		{"from the run tree", "v1", "v2", "", "", "v1", "v2"},
		{"flags win", "v1", "v2", "v2", "v1", "v2", "v1"},
		{"flags alone", "", "", "v1", "v2", "v1", "v2"},
		{"half a pair is no pair", "v1", "", "", "", "", ""},
		{"the same label twice is not a comparison", "v1", "v1", "", "", "", ""},
		{"nothing at all", "", "", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res.Baseline, res.Candidate = tt.recBase, tt.recCand
			base, cand := resolveVersionPair(res, tt.flagBase, tt.flagCand)
			require.Equal(t, tt.wantBase, base)
			require.Equal(t, tt.wantCand, cand)
		})
	}
}

func TestFormatStat(t *testing.T) {
	tests := []struct {
		name string
		s    bench.MetricStat
		want string
	}{
		{"no runs", bench.MetricStat{}, "-"},
		{"one run", bench.MetricStat{N: 1, Mean: 9}, "9.00"},
		{"identical runs", bench.MetricStat{N: 3, Mean: 5}, "5.00"},
		{"spread", bench.MetricStat{N: 2, Mean: 9.5, StdDev: 0.7071}, "9.50 +- 0.7071"},
		{"tokens", bench.MetricStat{N: 2, Mean: 15000, StdDev: 1200}, "15000 +- 1200"},
		{"cost", bench.MetricStat{N: 2, Mean: 0.4242, StdDev: 0.0031}, "0.4242 +- 0.0031"},
		{"zero", bench.MetricStat{N: 2, Mean: 0}, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, formatStat(tt.s))
		})
	}
}

// The whole `report` command over a real run tree, with the live write off.
// The tree holds only runs that classify without repo fixtures (internal/bench
// covers grading itself), which is enough to prove the flags, the export, and
// the ordering of the two.
func TestRunReport_EndToEndWithoutTrials(t *testing.T) {
	isolateLiveConfig(t)
	root := t.TempDir()

	cond := bench.DefaultConditions()[0]
	crashed := filepath.Join(root, "cookie-hardening", cond.Name, "run-01")
	require.NoError(t, os.MkdirAll(crashed, 0o755))
	require.NoError(t, bench.WriteRunRecord(crashed, bench.RunRecord{
		Scenario: "cookie-hardening", Condition: cond, Run: 1, Version: "v9.9.9-test",
		Error: "agent timed out",
	}))
	empty := filepath.Join(root, "cookie-hardening", cond.Name, "run-02")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	require.NoError(t, bench.WriteRunRecord(empty, bench.RunRecord{
		Scenario: "cookie-hardening", Condition: cond, Run: 2, Version: "v9.9.9-test",
	}))

	var out bytes.Buffer
	require.NoError(t, runReport([]string{"--out", root, "--no-trials"}, &out))

	require.Contains(t, out.String(), "2 total: 0 graded, 1 failed to run, 1 ungradeable")
	require.Contains(t, out.String(), "trials: skipped (--no-trials)")

	// The export is the durable half and is re-readable without the run dirs.
	export := filepath.Join(root, bench.ResultsFile)
	require.FileExists(t, export)
	res, err := bench.ReadResults(export)
	require.NoError(t, err)
	require.Len(t, res.Runs, 2)
	total, graded, failed, ungradeable := res.Totals()
	require.Equal(t, [4]int{2, 0, 1, 1}, [4]int{total, graded, failed, ungradeable})

	// Every verdict was cached back into its run dir, so a re-report is free.
	for _, dir := range []string{crashed, empty} {
		g, ok, err := bench.ReadGrade(dir)
		require.NoError(t, err)
		require.True(t, ok)
		require.NotEqual(t, bench.StatusGraded, g.Status)
	}

	t.Run("a custom --json path", func(t *testing.T) {
		alt := filepath.Join(t.TempDir(), "elsewhere.json")
		var out bytes.Buffer
		require.NoError(t, runReport([]string{"--out", root, "--json", alt, "--no-trials"}, &out))
		require.FileExists(t, alt)
	})

	t.Run("an empty tree says what to do", func(t *testing.T) {
		var out bytes.Buffer
		err := runReport([]string{"--out", t.TempDir(), "--no-trials"}, &out)
		require.ErrorContains(t, err, "no run directories")
	})

	t.Run("a missing tree is not silently empty", func(t *testing.T) {
		var out bytes.Buffer
		err := runReport([]string{"--out", filepath.Join(t.TempDir(), "nope"), "--no-trials"}, &out)
		require.ErrorContains(t, err, "no run tree at")
	})
}

// The judge is opt-in and its construction failures are loud: the operator
// asked for a layer that costs tokens, so a provider that cannot be built is
// an error, never a silent judge-less pass.
func TestBuildJudge(t *testing.T) {
	j, err := buildJudge(false, "")
	require.NoError(t, err)
	require.Nil(t, j, "without --judge there is no judge")

	bogus := filepath.Join(t.TempDir(), "judge.yaml")
	require.NoError(t, os.WriteFile(bogus, []byte("llm:\n  provider: bogus\n"), 0o600))
	_, err = buildJudge(true, bogus)
	require.ErrorContains(t, err, "--judge")
	require.ErrorContains(t, err, "bogus")

	keyless := filepath.Join(t.TempDir(), "judge.yaml")
	require.NoError(t, os.WriteFile(keyless, []byte("llm:\n  provider: openai\n  openai:\n    api_key: \"\"\n"), 0o600))
	t.Setenv("SEAMLESS_OPENAI_API_KEY", "")
	_, err = buildJudge(true, keyless)
	require.ErrorContains(t, err, "api_key")
}

// isolateLiveConfig points config.Load() at a throwaway file, so no test in
// this package can reach the owner's real instance even if a flag is forgotten.
func isolateLiveConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "seamless.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(strings.Join([]string{
		`addr: "127.0.0.1:1"`,
		`data_dir: "` + filepath.Join(dir, "data") + `"`,
		"mcp:",
		`  api_key: "test-key-not-a-real-one"`,
	}, "\n")+"\n"), 0o600))
	t.Setenv("SEAMLESS_CONFIG", cfg)
}
