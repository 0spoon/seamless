// The aggregation math, tested hard. This is the layer that produces the
// number the whole benchmark exists for, so a silent arithmetic error here
// would not look like a bug -- it would look like a result.

package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// synthesis helpers: a cell as a compact outcome string
// ---------------------------------------------------------------------------

// cellRuns builds one scenario x condition x version cell from an outcome
// string, one rune per run:
//
//	P  graded, passed
//	F  graded, failed
//	X  failed to run (crash, timeout, capture error)
//	U  ungradeable (ran, left nothing readable)
func cellRuns(t *testing.T, scenario, condition string, p Profile, version, outcomes string) []RunResult {
	t.Helper()
	out := make([]RunResult, 0, len(outcomes))
	for i, r := range outcomes {
		rr := RunResult{Record: RunRecord{
			Scenario:  scenario,
			Condition: Condition{Name: condition, Profile: p, Client: ClientClaude},
			Run:       i + 1,
			Version:   version,
		}}
		switch r {
		case 'P':
			rr.Grade = Grade{Schema: GradeSchema, Status: StatusGraded, Pass: true}
		case 'F':
			rr.Grade = Grade{Schema: GradeSchema, Status: StatusGraded}
		case 'X':
			rr.Record.Error = "agent timed out"
			rr.Grade = Grade{Schema: GradeSchema, Status: StatusFailedToRun, Error: "agent timed out"}
		case 'U':
			rr.Grade = Grade{Schema: GradeSchema, Status: StatusUngradeable, Error: "missing artifacts"}
		default:
			require.Failf(t, "bad outcome rune", "%q", r)
		}
		out = append(out, rr)
	}
	return out
}

// results assembles a set from cells.
func results(t *testing.T, cells ...[]RunResult) Results {
	t.Helper()
	var all []RunResult
	for _, c := range cells {
		all = append(all, c...)
	}
	return NewResults("", all)
}

const scA = "auth-refresh"
const scB = "cache-landmine"

// ---------------------------------------------------------------------------
// rates and spreads
// ---------------------------------------------------------------------------

func TestRate(t *testing.T) {
	tests := []struct {
		name    string
		rate    Rate
		ok      bool
		value   float64
		stdErr  float64
		display string
	}{
		{"empty", Rate{}, false, 0, 0, "n/a (0/0)"},
		{"one of two", Rate{Passed: 1, Graded: 2}, true, 0.5, 0.35355339059327373, "0.50 (1/2)"},
		{"perfect", Rate{Passed: 3, Graded: 3}, true, 1, 0, "1.00 (3/3)"},
		{"none", Rate{Passed: 0, Graded: 4}, true, 0, 0, "0.00 (0/4)"},
		{"two of three", Rate{Passed: 2, Graded: 3}, true, 2.0 / 3.0, 0.2721655269759087, "0.67 (2/3)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.ok, tt.rate.OK())
			require.InDelta(t, tt.value, tt.rate.Value(), 1e-12)
			require.InDelta(t, tt.stdErr, tt.rate.StdErr(), 1e-12)
			require.Equal(t, tt.display, tt.rate.String())
		})
	}
}

func TestMetricStat(t *testing.T) {
	tests := []struct {
		name                     string
		xs                       []float64
		n                        int
		mean, stdDev, minV, maxV float64
	}{
		{"empty", nil, 0, 0, 0, 0, 0},
		{"single run has no spread", []float64{7}, 1, 7, 0, 7, 7},
		{"two runs use the sample sd", []float64{4, 6}, 2, 5, 1.4142135623730951, 4, 6},
		{"identical runs", []float64{3, 3, 3}, 3, 3, 0, 3, 3},
		{"spread", []float64{2, 4, 4, 4, 5, 5, 7, 9}, 8, 5, 2.138089935299395, 2, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := metricStat(tt.xs)
			require.Equal(t, tt.n, s.N)
			require.InDelta(t, tt.mean, s.Mean, 1e-12)
			require.InDelta(t, tt.stdDev, s.StdDev, 1e-12)
			require.InDelta(t, tt.minV, s.Min, 1e-12)
			require.InDelta(t, tt.maxV, s.Max, 1e-12)
		})
	}
}

// ---------------------------------------------------------------------------
// cells: the three outcomes stay apart
// ---------------------------------------------------------------------------

func TestResults_CellSplitsTheThreeOutcomes(t *testing.T) {
	r := results(t, cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PPFXU"))

	c, ok := r.Cell(scA, "mechanism", "v1")
	require.True(t, ok)
	require.Equal(t, 5, c.Runs)
	require.Equal(t, Rate{Passed: 2, Graded: 3}, c.Rate, "only graded runs are in the denominator")
	require.Equal(t, 1, c.FailedToRun)
	require.Equal(t, 1, c.Ungradeable)

	_, ok = r.Cell(scA, "mechanism", "v2")
	require.False(t, ok, "an empty cell is absent, not a zero cell")
}

// A crashed run is not a failed run. If it were folded into the failure count,
// a dead API key would read as a Seamless regression -- which is exactly the
// misreading this benchmark exists to prevent.
func TestResults_InfrastructureFailuresDoNotDepressThePassRate(t *testing.T) {
	clean := results(t, cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PP"))
	flaky := results(t, cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PPXXU"))

	cc, _ := clean.Cell(scA, "mechanism", "v1")
	cf, _ := flaky.Cell(scA, "mechanism", "v1")
	require.Equal(t, cc.Rate, cf.Rate, "three lost runs must not move the pass-rate")
	require.Equal(t, 1.0, cf.Rate.Value())
	require.Equal(t, 2, cf.FailedToRun)
	require.Equal(t, 1, cf.Ungradeable)
	require.Equal(t, 5, cf.Runs, "but they are still reported")
}

// A status this build does not recognize -- an unset one, or one written by a
// newer schema -- must not slide into the pass-rate as a verdict.
func TestResults_UnknownStatusIsNotAVerdict(t *testing.T) {
	runs := cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PP")
	runs[1].Grade.Status = "" // e.g. a grade.json written before the field existed
	odd := cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "P")
	odd[0].Grade.Status = "quarantined-by-a-future-build"
	runs = append(runs, odd...)

	r := results(t, runs)
	c, ok := r.Cell(scA, "mechanism", "v1")
	require.True(t, ok)
	require.Equal(t, Rate{1, 1}, c.Rate, "only an explicit graded status counts")
	require.Equal(t, 2, c.Ungradeable)
	require.Equal(t, 3, c.Runs)

	total, graded, failed, ungradeable := r.Totals()
	require.Equal(t, [4]int{3, 1, 0, 2}, [4]int{total, graded, failed, ungradeable})
}

func TestResults_Totals(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "PF"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PXU"),
	)
	total, graded, failed, ungradeable := r.Totals()
	require.Equal(t, 5, total)
	require.Equal(t, 3, graded)
	require.Equal(t, 1, failed)
	require.Equal(t, 1, ungradeable)
	require.Equal(t, 1, r.MinGradedPerCell(), "the mechanism cell graded one run")
}

func TestResults_MetricsSpreadOverGradedRunsOnly(t *testing.T) {
	runs := cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PPX")
	runs[0].Record.Metrics = Metrics{Turns: 8, InputTokens: 1000}
	runs[0].Grade.Metrics = Metrics{ToolCalls: 4}
	runs[1].Record.Metrics = Metrics{Turns: 12, InputTokens: 2000}
	runs[1].Grade.Metrics = Metrics{ToolCalls: 6}
	// The crashed run's truncated numbers must not enter the mean.
	runs[2].Record.Metrics = Metrics{Turns: 1, InputTokens: 40}

	c, ok := results(t, runs).Cell(scA, "mechanism", "v1")
	require.True(t, ok)
	require.Equal(t, MetricStat{N: 2, Mean: 10, StdDev: 2.8284271247461903, Min: 8, Max: 12}, c.Metrics["turns"])
	require.Equal(t, MetricStat{N: 2, Mean: 5, StdDev: 1.4142135623730951, Min: 4, Max: 6}, c.Metrics["toolCalls"])
	require.Equal(t, 1500.0, c.Metrics["inputTokens"].Mean)
}

// ---------------------------------------------------------------------------
// uplift
// ---------------------------------------------------------------------------

func TestResults_Uplift(t *testing.T) {
	tests := []struct {
		name            string
		control, arm    string // outcome strings for the vanilla and mechanism cells
		wantUplift      float64
		wantOK          bool
		wantRate        Rate
		wantControlRate Rate
	}{
		{"mechanism fixes every run the control fails", "FF", "PP", 1, true, Rate{2, 2}, Rate{0, 2}},
		{"no difference", "PP", "PP", 0, true, Rate{2, 2}, Rate{2, 2}},
		{"one run of two is half an uplift", "FF", "PF", 0.5, true, Rate{1, 2}, Rate{0, 2}},
		{"a negative uplift is a real answer", "PP", "FF", -1, true, Rate{0, 2}, Rate{2, 2}},
		{"control lost every run to crashes", "XX", "PP", 0, false, Rate{2, 2}, Rate{}},
		{"arm lost every run to crashes", "PP", "XX", 0, false, Rate{}, Rate{2, 2}},
		{"a crash on one side does not shift the other", "PFX", "PP", 0.5, true, Rate{2, 2}, Rate{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := results(t,
				cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", tt.control),
				cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", tt.arm),
			)
			u := r.UpliftFor(scA, "mechanism", "v1")
			require.Equal(t, "vanilla", u.Control)
			require.True(t, u.HasControl)
			require.Equal(t, tt.wantOK, u.OK())
			require.Equal(t, tt.wantRate, u.Rate)
			require.Equal(t, tt.wantControlRate, u.ControlRate)
			require.InDelta(t, tt.wantUplift, u.Value, 1e-12)
		})
	}
}

// Without a control arm there is no uplift, and an absolute pass-rate must not
// be handed back as though there were one.
func TestResults_UpliftWithoutAVanillaArm(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PP"),
		cellRuns(t, scA, "full", ProfileFull, "v1", "PF"),
	)
	require.Empty(t, r.Control)
	require.Contains(t, r.ControlNote, "no arm with the vanilla profile")

	u := r.UpliftFor(scA, "mechanism", "v1")
	require.False(t, u.HasControl)
	require.False(t, u.OK())
	require.Zero(t, u.Value)
	require.Equal(t, Rate{2, 2}, u.Rate, "the absolute pass-rate is still reported")
}

func TestControlCondition(t *testing.T) {
	tests := []struct {
		name     string
		runs     []RunResult
		want     string
		wantNote string
	}{
		{"named after its profile", cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "P"), "vanilla", ""},
		{"named anything else", cellRuns(t, scA, "control", ProfileVanilla, "v1", "P"), "control", ""},
		{"absent", cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "P"), "", "no arm with the vanilla profile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, note := controlCondition(tt.runs)
			require.Equal(t, tt.want, got)
			if tt.wantNote == "" {
				require.Empty(t, note)
			} else {
				require.Contains(t, note, tt.wantNote)
			}
		})
	}

	t.Run("two vanilla arms are ambiguous, not a coin flip", func(t *testing.T) {
		var runs []RunResult
		runs = append(runs, cellRuns(t, scA, "bare", ProfileVanilla, "v1", "P")...)
		runs = append(runs, cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "P")...)
		got, note := controlCondition(runs)
		require.Empty(t, got)
		require.Contains(t, note, "ambiguous control")
		require.Contains(t, note, "bare, vanilla")
	})
}

// The aggregate pools graded runs across scenarios: a scenario that lost runs
// carries proportionally less weight, which is the honest reading when cells
// are uneven.
func TestResults_AggregateUpliftPoolsGradedRuns(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "FF"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PP"),
		cellRuns(t, scB, "vanilla", ProfileVanilla, "v1", "PP"),
		cellRuns(t, scB, "mechanism", ProfileMechanism, "v1", "PF"),
	)
	perScenario := r.ScenarioUplifts("v1")
	require.Len(t, perScenario, 4)
	require.InDelta(t, 1.0, r.UpliftFor(scA, "mechanism", "v1").Value, 1e-12)
	require.InDelta(t, -0.5, r.UpliftFor(scB, "mechanism", "v1").Value, 1e-12)

	agg := r.AggregateUplifts("v1")
	require.Len(t, agg, 2)
	require.Equal(t, "vanilla", agg[0].Condition, "the control reads first")
	mech := agg[1]
	require.Equal(t, Rate{3, 4}, mech.Rate)
	require.Equal(t, Rate{2, 4}, mech.ControlRate)
	require.InDelta(t, 0.25, mech.Value, 1e-12, "pooled, not the mean of 1.0 and -0.5")
}

// ---------------------------------------------------------------------------
// version delta
// ---------------------------------------------------------------------------

func TestResults_VersionDeltas(t *testing.T) {
	tests := []struct {
		name                 string
		baseControl, baseArm string
		candControl, candArm string
		wantOK               bool
		wantDelta            float64
		wantNote             string
	}{
		{
			name:        "uplift held",
			baseControl: "FF", baseArm: "PP",
			candControl: "FF", candArm: "PP",
			wantOK: true, wantDelta: 0,
		},
		{
			name:        "regression: the mechanism stopped helping",
			baseControl: "FF", baseArm: "PP",
			candControl: "FF", candArm: "FF",
			wantOK: true, wantDelta: -1,
		},
		{
			// Both arms dropped equally: the model had a bad day, Seamless is
			// unchanged. The uplift delta is 0 and ControlDrift shows the drop.
			name:        "a bad model day is not a regression",
			baseControl: "PPPP", baseArm: "PPPP",
			candControl: "FFFF", candArm: "FFFF",
			wantOK: true, wantDelta: 0,
		},
		{
			name:        "improvement",
			baseControl: "FF", baseArm: "FF",
			candControl: "FF", candArm: "PP",
			wantOK: true, wantDelta: 1,
		},
		{
			name:        "the candidate never produced a graded run",
			baseControl: "FF", baseArm: "PP",
			candControl: "FF", candArm: "XX",
			wantOK: false, wantNote: "no graded runs at v2",
		},
		{
			name:        "the baseline never produced a graded run",
			baseControl: "XX", baseArm: "XX",
			candControl: "FF", candArm: "PP",
			wantOK: false, wantNote: "no graded runs at v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := results(t,
				cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", tt.baseControl),
				cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", tt.baseArm),
				cellRuns(t, scA, "vanilla", ProfileVanilla, "v2", tt.candControl),
				cellRuns(t, scA, "mechanism", ProfileMechanism, "v2", tt.candArm),
			)
			deltas := r.VersionDeltas("v1", "v2")
			// One scenario x one non-control condition, plus the aggregate row.
			require.Len(t, deltas, 2)
			for _, d := range deltas {
				require.Equal(t, "mechanism", d.Condition)
				require.Equal(t, tt.wantOK, d.OK, "scenario %q", d.Scenario)
				if tt.wantOK {
					require.InDelta(t, tt.wantDelta, d.Value, 1e-12, "scenario %q", d.Scenario)
				} else {
					require.Contains(t, d.Note, tt.wantNote)
					require.Zero(t, d.Value)
				}
			}
		})
	}
}

func TestResults_VersionDeltasSkipTheControlAndCoverTheAggregate(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "FF"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PP"),
		cellRuns(t, scA, "full", ProfileFull, "v1", "PP"),
		cellRuns(t, scB, "vanilla", ProfileVanilla, "v1", "FF"),
		cellRuns(t, scB, "mechanism", ProfileMechanism, "v1", "PP"),
		cellRuns(t, scB, "full", ProfileFull, "v1", "PP"),
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v2", "FF"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v2", "FF"),
		cellRuns(t, scA, "full", ProfileFull, "v2", "PP"),
		cellRuns(t, scB, "vanilla", ProfileVanilla, "v2", "FF"),
		cellRuns(t, scB, "mechanism", ProfileMechanism, "v2", "PP"),
		cellRuns(t, scB, "full", ProfileFull, "v2", "PP"),
	)
	deltas := r.VersionDeltas("v1", "v2")
	// (2 scenarios + aggregate) x 2 non-control conditions.
	require.Len(t, deltas, 6)
	got := map[string]float64{}
	for _, d := range deltas {
		require.NotEqual(t, "vanilla", d.Condition, "the control's uplift over itself is not a comparison")
		require.True(t, d.OK)
		got[d.Scenario+"/"+d.Condition] = d.Value
	}
	require.InDelta(t, -1.0, got[scA+"/mechanism"], 1e-12, "mechanism regressed on auth-refresh")
	require.InDelta(t, 0.0, got[scB+"/mechanism"], 1e-12)
	require.InDelta(t, -0.5, got["/mechanism"], 1e-12, "the aggregate halves it across two scenarios")
	require.InDelta(t, 0.0, got["/full"], 1e-12)
}

// The control arm is the invariant: its own movement across versions is base
// model and environment, never this repo.
func TestResults_ControlDrift(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "PPPF"),
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v2", "PFFF"),
	)
	base, cand, ok := r.ControlDrift("v1", "v2")
	require.True(t, ok)
	require.Equal(t, Rate{3, 4}, base)
	require.Equal(t, Rate{1, 4}, cand)

	_, _, ok = results(t, cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "P")).ControlDrift("v1", "v2")
	require.False(t, ok)
}

// ---------------------------------------------------------------------------
// ordering
// ---------------------------------------------------------------------------

func TestResults_Ordering(t *testing.T) {
	r := results(t,
		cellRuns(t, scB, "full", ProfileFull, "v2", "P"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "P"),
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v2", "P"),
	)
	require.Equal(t, []string{scA, scB}, r.Scenarios())
	require.Equal(t, []string{"vanilla", "mechanism", "full"}, r.Conditions(),
		"conditions read control-first, then increasing amounts of Seamless")
	require.Equal(t, []string{"v1", "v2"}, r.Versions())

	r.Baseline, r.Candidate = "v2", "v1"
	require.Equal(t, []string{"v2", "v1"}, r.Versions(), "a known pair orders baseline first")
}

// ---------------------------------------------------------------------------
// metrics vocabulary
// ---------------------------------------------------------------------------

func TestMergeMetrics(t *testing.T) {
	runner := Metrics{Turns: 9, InputTokens: 100, OutputTokens: 50, CostUSD: 1.5, DurationMS: 4200,
		ToolCalls: 999} // a runner never fills this; if it did, the grader still wins
	grader := Metrics{ToolCalls: 7, Injections: 1, MemoryReads: 2, Recalls: 3, RecallMisses: 1,
		MemoryWrites: 1, SessionFindings: 1, TaskTransitions: 1, Mishaps: 1, ToolErrors: 2,
		ToolCallsByName: map[string]int{"recall": 3}}

	m := MergeMetrics(runner, grader)
	require.Equal(t, 9, m.Turns)
	require.Equal(t, 100, m.InputTokens)
	require.Equal(t, 50, m.OutputTokens)
	require.Equal(t, 1.5, m.CostUSD)
	require.Equal(t, int64(4200), m.DurationMS)
	require.Equal(t, 7, m.ToolCalls, "the grader owns the event-derived half")
	require.Equal(t, map[string]int{"recall": 3}, m.ToolCallsByName)

	// A legitimately zero measurement stays zero rather than being back-filled.
	m = MergeMetrics(Metrics{Turns: 3}, Metrics{})
	require.Zero(t, m.ToolCalls)
	require.Equal(t, 3, m.Turns)
}

func TestMetricsFields_CoverEveryScalarField(t *testing.T) {
	// Every scalar field, set to a distinct value, must come back by its JSON
	// name -- the guard against Fields drifting behind the struct.
	m := Metrics{
		Turns: 1, InputTokens: 2, OutputTokens: 3, CostUSD: 4.5, DurationMS: 6,
		ToolCalls: 7, Injections: 8, MemoryReads: 9, Recalls: 10, RecallMisses: 11,
		MemoryWrites: 12, SessionFindings: 13, TaskTransitions: 14, Mishaps: 15, ToolErrors: 16,
		ToolCallsByName: map[string]int{"recall": 3},
	}
	f := m.Fields()
	require.Equal(t, map[string]float64{
		"turns": 1, "inputTokens": 2, "outputTokens": 3, "costUsd": 4.5, "durationMs": 6,
		"toolCalls": 7, "injections": 8, "memoryReads": 9, "recalls": 10, "recallMisses": 11,
		"memoryWrites": 12, "sessionFindings": 13, "taskTransitions": 14, "mishaps": 15, "toolErrors": 16,
	}, f)

	names := MetricNames()
	require.Len(t, names, len(f))
	for _, n := range ReportedMetrics {
		require.Contains(t, names, n, "the report shows a metric that does not exist")
	}
}

// ---------------------------------------------------------------------------
// export
// ---------------------------------------------------------------------------

func TestResults_JSONRoundTrip(t *testing.T) {
	r := results(t,
		cellRuns(t, scA, "vanilla", ProfileVanilla, "v1", "PF"),
		cellRuns(t, scA, "mechanism", ProfileMechanism, "v1", "PPX"),
	)
	r.Baseline, r.Candidate = "v1", "v2"
	r.Runs[0].Grade.Details = []string{"repo/gate: limiter present -- PASS"}

	path := filepath.Join(t.TempDir(), ResultsFile)
	require.NoError(t, WriteResults(path, r))

	back, err := ReadResults(path)
	require.NoError(t, err)
	require.Equal(t, ResultsSchema, back.Schema)
	require.Equal(t, r.Control, back.Control)
	require.Equal(t, r.Baseline, back.Baseline)
	require.Len(t, back.Runs, 5)
	require.Equal(t, r.Runs[0].Grade.Details, back.Runs[0].Grade.Details)

	// The re-read set aggregates identically: the report never needs the run
	// dirs again.
	require.Equal(t, r.UpliftFor(scA, "mechanism", "v1"), back.UpliftFor(scA, "mechanism", "v1"))

	t.Run("a schema from the future is refused, not half-read", func(t *testing.T) {
		var raw map[string]any
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(b, &raw))
		raw["schema"] = ResultsSchema + 1
		b, err = json.Marshal(raw)
		require.NoError(t, err)
		future := filepath.Join(t.TempDir(), ResultsFile)
		require.NoError(t, os.WriteFile(future, b, 0o644))

		_, err = ReadResults(future)
		require.ErrorContains(t, err, "this build reads")
	})
}

func TestVersionPair_RoundTrip(t *testing.T) {
	root := t.TempDir()
	_, ok, err := ReadVersionPair(root)
	require.NoError(t, err)
	require.False(t, ok, "absent is not an error: most run trees hold one version")

	require.NoError(t, WriteVersionPair(root, VersionPair{Baseline: "v0.4.5", Candidate: "v0.4.6-dirty"}))
	p, ok, err := ReadVersionPair(root)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "v0.4.5", p.Baseline)
	require.Equal(t, "v0.4.6-dirty", p.Candidate)
}

// ---------------------------------------------------------------------------
// collecting a real run tree
// ---------------------------------------------------------------------------

// writeRunDirAt lays out one run directory at an exact path, the way the runner
// would. No data dir: these are control-arm runs, graded on the repo
// assertions alone, which keeps the walk tests cheap.
func writeRunDirAt(t *testing.T, dir string, rec RunRecord, files map[string]string) {
	t.Helper()
	repo := filepath.Join(dir, RepoDirName)
	require.NoError(t, os.MkdirAll(repo, 0o755))
	for name, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, DiffFile),
		[]byte(cookieDiff), 0o644))
	require.NoError(t, WriteRunRecord(dir, rec))
}

// runDirRec is the manifest for one synthesized run of the real scenario.
func runDirRec(cond Condition, version string, i int) RunRecord {
	return RunRecord{
		Scenario: cookieHardeningName, Condition: cond, Run: i, Version: version,
		Metrics: Metrics{Turns: 9, InputTokens: 1000},
	}
}

func TestCollect_GradesTheTreeAndCachesTheVerdicts(t *testing.T) {
	root := t.TempDir()
	vanilla := DefaultConditions()[0]
	cell := func(version string, i int, files map[string]string) string {
		dir := filepath.Join(root, version, cookieHardeningName, vanilla.Name, "run-0"+string(rune('0'+i)))
		writeRunDirAt(t, dir, runDirRec(vanilla, version, i), files)
		return dir
	}
	passDir := cell("v1", 1, cookieHardenedRepo())
	failDir := cell("v1", 2, baselineRepo())

	// A run that never produced a verdict, and one that produced no evidence.
	crashed := filepath.Join(root, "v1", cookieHardeningName, vanilla.Name, "run-03")
	rec := runDirRec(vanilla, "v1", 3)
	rec.Error = "agent timed out"
	writeRunDirAt(t, crashed, rec, cookieHardenedRepo())

	empty := filepath.Join(root, "v1", cookieHardeningName, vanilla.Name, "run-04")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	require.NoError(t, WriteRunRecord(empty, runDirRec(vanilla, "v1", 4)))

	ctx := context.Background()
	res, err := Collect(ctx, root, CollectOptions{})
	require.NoError(t, err)
	require.Len(t, res.Runs, 4)
	require.Equal(t, "vanilla", res.Control)

	byDir := map[string]RunResult{}
	for _, r := range res.Runs {
		byDir[r.Dir] = r
	}
	rel := func(dir string) string {
		p, err := filepath.Rel(root, dir)
		require.NoError(t, err)
		return filepath.ToSlash(p)
	}
	require.Equal(t, StatusGraded, byDir[rel(passDir)].Grade.Status)
	require.True(t, byDir[rel(passDir)].Grade.Pass)
	require.Equal(t, StatusGraded, byDir[rel(failDir)].Grade.Status)
	require.False(t, byDir[rel(failDir)].Grade.Pass)
	require.Equal(t, StatusFailedToRun, byDir[rel(crashed)].Grade.Status)
	require.Contains(t, byDir[rel(crashed)].Grade.Error, "timed out")
	require.Equal(t, StatusUngradeable, byDir[rel(empty)].Grade.Status)
	require.Contains(t, byDir[rel(empty)].Grade.Error, "missing artifacts")

	// One pass out of two graded; the crash and the empty run stay out of it.
	c, ok := res.Cell(cookieHardeningName, vanilla.Name, "v1")
	require.True(t, ok)
	require.Equal(t, Rate{1, 2}, c.Rate)
	require.Equal(t, 1, c.FailedToRun)
	require.Equal(t, 1, c.Ungradeable)
	require.Equal(t, 4, c.Runs)

	// Every verdict was cached back into its run dir.
	g, ok, err := ReadGrade(passDir)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, g.Pass)
	require.Equal(t, GradeSchema, g.Schema)

	t.Run("a cached verdict is reused", func(t *testing.T) {
		// Rewrite the cache with an obviously wrong verdict: if the second
		// collect re-graded, it would come back to PASS.
		g, _, err := ReadGrade(passDir)
		require.NoError(t, err)
		g.Pass = false
		require.NoError(t, WriteGrade(passDir, g))

		again, err := Collect(ctx, root, CollectOptions{})
		require.NoError(t, err)
		require.False(t, again.RateFor("", vanilla.Name, "v1").Passed > 0, "the cache was not consulted")
	})

	t.Run("regrade rewrites it", func(t *testing.T) {
		again, err := Collect(ctx, root, CollectOptions{Regrade: true})
		require.NoError(t, err)
		require.Equal(t, Rate{1, 2}, again.RateFor("", vanilla.Name, "v1"))
		g, _, err := ReadGrade(passDir)
		require.NoError(t, err)
		require.True(t, g.Pass)
	})

	t.Run("the version pair travels with the tree", func(t *testing.T) {
		require.NoError(t, WriteVersionPair(root, VersionPair{Baseline: "v1", Candidate: "v2"}))
		again, err := Collect(ctx, root, CollectOptions{})
		require.NoError(t, err)
		require.Equal(t, "v1", again.Baseline)
		require.Equal(t, "v2", again.Candidate)
	})
}

func TestRunDirs(t *testing.T) {
	root := t.TempDir()
	vanilla := DefaultConditions()[0]
	a := filepath.Join(root, "v1", cookieHardeningName, "vanilla", "run-01")
	b := filepath.Join(root, "v2", cookieHardeningName, "vanilla", "run-01")
	writeRunDirAt(t, a, runDirRec(vanilla, "v1", 1), cookieHardenedRepo())
	writeRunDirAt(t, b, runDirRec(vanilla, "v2", 1), cookieHardenedRepo())

	// A stray manifest inside a preserved copy must not be mistaken for a run.
	require.NoError(t, os.MkdirAll(filepath.Join(a, RepoDirName, "nested"), 0o755))
	require.NoError(t, WriteRunRecord(filepath.Join(a, RepoDirName, "nested"), runDirRec(vanilla, "v1", 9)))

	dirs, err := RunDirs(root)
	require.NoError(t, err)
	require.Equal(t, []string{a, b}, dirs)

	t.Run("an empty tree is empty, not an error", func(t *testing.T) {
		dirs, err := RunDirs(t.TempDir())
		require.NoError(t, err)
		require.Empty(t, dirs)
	})
}
