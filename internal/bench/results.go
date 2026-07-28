// The measurement layer: many captured runs in, one number out.
//
// This is where the benchmark's whole point lands, so the arithmetic is kept
// pure and separate from both halves that feed it -- the runner writes run
// dirs, the grader turns one run dir into a verdict, and everything here is a
// function of those verdicts. Nothing in this file runs an agent, opens an arm,
// or talks to a live instance.
//
// THREE OUTCOMES, NEVER TWO. A cell's runs fall into three buckets and they are
// never folded together:
//
//	graded         the agent ran and left evidence; the verdict is real
//	failed to run  the RUN failed (crash, timeout, harness error, capture error)
//	ungradeable    it ran, but left no readable evidence to grade
//
// Only graded runs are in the pass-rate's denominator. Folding either of the
// other two into the failure count is how an infrastructure flake -- a dead API
// key, a full disk -- gets read as a Seamless regression, which is the exact
// misreading this benchmark exists to prevent.
//
// SMALL N IS THE NORMAL CASE. A cell is a handful of runs of a nondeterministic
// agent, so every rate in here carries the counts behind it (Rate) and every
// metric carries its spread (MetricStat). "50% uplift" over two runs is one run
// and must never be printed as though it were a measurement.

package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Schema markers for the two JSON artifacts this layer owns. They exist so a
// future field addition is detectable by a reader that predates it: bump on a
// change that a previous reader would misinterpret, not on a purely additive
// field.
const (
	// ResultsSchema versions the exported results set (results.json).
	ResultsSchema = 1
	// GradeSchema versions the per-run persisted verdict (grade.json).
	GradeSchema = 1
)

// ResultsFile is the exported results set, written at the root of a run tree.
const ResultsFile = "results.json"

// VersionsFile records which version label is the baseline and which the
// candidate, written by the runner at the root of a two-version run tree. The
// report reads it so `report` needs no flags to draw the delta table, and so a
// reader months later can still tell which way round the comparison went.
const VersionsFile = "versions.json"

// RunStatus classifies one run for aggregation.
type RunStatus string

const (
	// StatusGraded is a run whose verdict is real evidence about the agent.
	StatusGraded RunStatus = "graded"
	// StatusFailedToRun is a run that never produced a verdict to begin with:
	// the agent crashed, timed out, or the capture failed. Counted apart from
	// failures because it says nothing about the agent.
	StatusFailedToRun RunStatus = "failed_to_run"
	// StatusUngradeable is a run that finished but cannot be graded -- missing
	// artifacts, an unreadable database, a manifest naming a scenario this
	// build does not know. Also counted apart: no evidence is not bad evidence.
	StatusUngradeable RunStatus = "ungradeable"
)

// Grade is the verdict for one run directory, persisted beside the manifest as
// grade.json so a run dir is self-describing and the report never re-grades
// work it already has. Re-grading is deliberate: `report --regrade` after a
// grader fix rewrites every one of these without spending a token, which is
// why grading lives in `report` and not in `run`.
type Grade struct {
	Schema   int       `json:"schema"`
	GradedAt time.Time `json:"gradedAt"`
	Status   RunStatus `json:"status"`
	// Pass is meaningful only when Status is StatusGraded.
	Pass bool `json:"pass"`
	// Error says why a run is failed-to-run or ungradeable.
	Error   string   `json:"error,omitempty"`
	Details []string `json:"details,omitempty"`
	// Metrics is the grader-derived half only; the runner's half stays on the
	// RunRecord (see MergeMetrics).
	Metrics Metrics `json:"metrics"`
	// TrialID is the live research-lab trial this run was recorded as, once it
	// has been. Present means "already recorded", so re-running the report does
	// not duplicate the trial.
	TrialID string `json:"trialId,omitempty"`
}

// RunResult is one run in a results set: what the runner recorded and what the
// grader made of it.
type RunResult struct {
	Record RunRecord `json:"record"`
	// Dir is the run directory, slash-separated and relative to the results
	// root, so an exported results set survives being moved or copied.
	Dir   string `json:"dir,omitempty"`
	Grade Grade  `json:"grade"`
}

// Metrics returns the run's full measurement set: the runner's half from the
// manifest merged with the grader's half from the verdict.
func (r RunResult) Metrics() Metrics { return MergeMetrics(r.Record.Metrics, r.Grade.Metrics) }

// Results is the whole graded results set -- every cell of the
// scenario x condition x version x run matrix that was captured.
type Results struct {
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generatedAt"`
	// Root is the run tree the set was collected from, for provenance only;
	// nothing reads back through it.
	Root string `json:"root,omitempty"`
	// Control is the condition name the uplift metric subtracts -- the arm with
	// the vanilla profile. Empty means there is none, and ControlNote says why.
	Control     string `json:"control,omitempty"`
	ControlNote string `json:"controlNote,omitempty"`
	// Baseline and Candidate name the two version labels being compared, when
	// the run tree was produced by a version comparison.
	Baseline  string      `json:"baseline,omitempty"`
	Candidate string      `json:"candidate,omitempty"`
	Runs      []RunResult `json:"runs"`
}

// VersionPair is the baseline/candidate record a version-comparison run leaves
// at the root of its run tree.
type VersionPair struct {
	Schema    int    `json:"schema"`
	Baseline  string `json:"baseline"`
	Candidate string `json:"candidate"`
}

// ---------------------------------------------------------------------------
// rates and spreads
// ---------------------------------------------------------------------------

// Rate is a pass-rate that cannot be quoted without its counts. Every rate in
// this package is one of these on purpose: at the run counts a token-metered
// benchmark can afford, "50%" is a sentence about two runs and the reader has
// to be able to see that.
type Rate struct {
	Passed int `json:"passed"`
	Graded int `json:"graded"`
}

// OK reports whether the rate rests on any graded run at all.
func (r Rate) OK() bool { return r.Graded > 0 }

// Value is Passed/Graded, or 0 when nothing was graded. Check OK first: a zero
// from an empty cell and a genuine 0% are not the same claim.
func (r Rate) Value() float64 {
	if r.Graded == 0 {
		return 0
	}
	return float64(r.Passed) / float64(r.Graded)
}

// StdErr is the standard error of the proportion, sqrt(p(1-p)/n) -- the cheap
// honest answer to "how much of this is noise?". At n=2, p=0.5 it is 0.35,
// which is the point.
func (r Rate) StdErr() float64 {
	if r.Graded == 0 {
		return 0
	}
	p := r.Value()
	return math.Sqrt(p * (1 - p) / float64(r.Graded))
}

// String renders the rate with its counts: "0.50 (1/2)".
func (r Rate) String() string {
	if !r.OK() {
		return "n/a (0/0)"
	}
	return fmt.Sprintf("%.2f (%d/%d)", r.Value(), r.Passed, r.Graded)
}

// add accumulates another rate into this one.
func (r Rate) add(o Rate) Rate {
	return Rate{Passed: r.Passed + o.Passed, Graded: r.Graded + o.Graded}
}

// MetricStat is one metric's spread over a cell's graded runs.
type MetricStat struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stdDev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// metricStat summarizes a sample. StdDev is the SAMPLE standard deviation
// (n-1): the runs are draws from a nondeterministic process, not a whole
// population, and at n=2 the Bessel-corrected figure is the one that does not
// understate how little is known. A single run has no spread, so N=1 gives 0.
func metricStat(xs []float64) MetricStat {
	s := MetricStat{N: len(xs)}
	if s.N == 0 {
		return s
	}
	s.Min, s.Max = xs[0], xs[0]
	sum := 0.0
	for _, x := range xs {
		sum += x
		s.Min = min(s.Min, x)
		s.Max = max(s.Max, x)
	}
	s.Mean = sum / float64(s.N)
	if s.N < 2 {
		return s
	}
	ss := 0.0
	for _, x := range xs {
		d := x - s.Mean
		ss += d * d
	}
	s.StdDev = math.Sqrt(ss / float64(s.N-1))
	return s
}

// ---------------------------------------------------------------------------
// cells, uplift, version delta
// ---------------------------------------------------------------------------

// CellKey identifies one scenario x condition x version cell.
type CellKey struct {
	Scenario  string `json:"scenario"`
	Condition string `json:"condition"`
	Version   string `json:"version"`
}

// CellStats summarizes one cell: the pass-rate, the counts that qualify it, and
// the spread of every metric over the cell's graded runs.
type CellStats struct {
	CellKey
	// Runs is every run in the cell, graded or not.
	Runs        int  `json:"runs"`
	Rate        Rate `json:"rate"`
	FailedToRun int  `json:"failedToRun"`
	Ungradeable int  `json:"ungradeable"`
	// Metrics is keyed by the JSON field names of Metrics, over graded runs
	// only: a crashed run's truncated token count measures nothing.
	Metrics map[string]MetricStat `json:"metrics,omitempty"`
}

// Uplift is the primary metric: how much better a condition did than the
// control, at one version. Scenario "" is the aggregate over every scenario.
type Uplift struct {
	Scenario  string `json:"scenario,omitempty"`
	Condition string `json:"condition"`
	Version   string `json:"version"`
	// Control names the arm subtracted. HasControl false means the results set
	// has no vanilla arm, so Value is not an uplift at all and Rate is the only
	// honest thing to report.
	Control     string `json:"control,omitempty"`
	HasControl  bool   `json:"hasControl"`
	Rate        Rate   `json:"rate"`
	ControlRate Rate   `json:"controlRate"`
	// Value is Rate - ControlRate, valid only when HasControl and both rates
	// are OK.
	Value float64 `json:"value"`
}

// OK reports whether the uplift figure means anything: a control exists and
// both arms have at least one graded run.
func (u Uplift) OK() bool { return u.HasControl && u.Rate.OK() && u.ControlRate.OK() }

// VersionDelta compares one condition's uplift across two versions. A negative
// Value is a regression: Seamless helped less than it used to.
type VersionDelta struct {
	Scenario  string  `json:"scenario,omitempty"`
	Condition string  `json:"condition"`
	Baseline  Uplift  `json:"baseline"`
	Candidate Uplift  `json:"candidate"`
	Value     float64 `json:"value"`
	// OK is false when either side is missing; Note says which.
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------
// building a results set
// ---------------------------------------------------------------------------

// NewResults assembles a results set and derives its control arm.
func NewResults(root string, runs []RunResult) Results {
	control, note := controlCondition(runs)
	return Results{
		Schema:      ResultsSchema,
		GeneratedAt: time.Now().UTC(),
		Root:        root,
		Control:     control,
		ControlNote: note,
		Runs:        runs,
	}
}

// controlCondition picks the control arm by PROFILE, not by name: an arm is the
// control because it has no Seamless in it, whatever its owner called it. It
// returns the condition name, or "" plus the reason there is none -- an absent
// control is reported, never papered over by quoting an absolute pass-rate as
// though it were an uplift.
func controlCondition(runs []RunResult) (string, string) {
	var names []string
	for _, r := range runs {
		c := r.Record.Condition
		if c.Profile == ProfileVanilla && !slices.Contains(names, c.Name) {
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	switch len(names) {
	case 0:
		return "", "no arm with the " + string(ProfileVanilla) +
			" profile: uplift is undefined, so only absolute pass-rates are reported"
	case 1:
		return names[0], ""
	default:
		return "", fmt.Sprintf("ambiguous control: %d arms use the %s profile (%s); "+
			"uplift needs exactly one", len(names), ProfileVanilla, strings.Join(names, ", "))
	}
}

// Versions lists the version labels present, baseline first when the set knows
// which is which, else sorted.
func (r Results) Versions() []string {
	vs := distinct(r.Runs, func(x RunResult) string { return x.Record.Version })
	slices.Sort(vs)
	if r.Baseline == "" || r.Candidate == "" {
		return vs
	}
	ordered := make([]string, 0, len(vs))
	for _, want := range []string{r.Baseline, r.Candidate} {
		if slices.Contains(vs, want) {
			ordered = append(ordered, want)
		}
	}
	for _, v := range vs {
		if !slices.Contains(ordered, v) {
			ordered = append(ordered, v)
		}
	}
	return ordered
}

// Scenarios lists the scenario names present, sorted.
func (r Results) Scenarios() []string {
	ss := distinct(r.Runs, func(x RunResult) string { return x.Record.Scenario })
	slices.Sort(ss)
	return ss
}

// Conditions lists the condition names present, ordered the way the arms are
// meant to be read: the control first, then increasing amounts of Seamless.
func (r Results) Conditions() []string {
	profile := map[string]Profile{}
	for _, x := range r.Runs {
		if _, ok := profile[x.Record.Condition.Name]; !ok {
			profile[x.Record.Condition.Name] = x.Record.Condition.Profile
		}
	}
	cs := distinct(r.Runs, func(x RunResult) string { return x.Record.Condition.Name })
	sort.Slice(cs, func(i, j int) bool {
		ri, rj := profileRank(profile[cs[i]]), profileRank(profile[cs[j]])
		if ri != rj {
			return ri < rj
		}
		return cs[i] < cs[j]
	})
	return cs
}

// profileRank orders profiles by how much Seamless they contain.
func profileRank(p Profile) int {
	if i := slices.Index(Profiles, p); i >= 0 {
		return i
	}
	return len(Profiles)
}

func distinct[T any](in []T, key func(T) string) []string {
	var out []string
	for _, x := range in {
		if k := key(x); k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Cells summarizes every populated cell, in report order.
func (r Results) Cells() []CellStats {
	var out []CellStats
	for _, sc := range r.Scenarios() {
		for _, cond := range r.Conditions() {
			for _, v := range r.Versions() {
				if c, ok := r.Cell(sc, cond, v); ok {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// Cell summarizes one cell, reporting whether it holds any run at all.
func (r Results) Cell(scenario, condition, version string) (CellStats, bool) {
	c := CellStats{CellKey: CellKey{Scenario: scenario, Condition: condition, Version: version}}
	samples := map[string][]float64{}
	for _, x := range r.Runs {
		if !matches(x, scenario, condition, version) {
			continue
		}
		c.Runs++
		switch x.Grade.Status {
		case StatusGraded:
			c.Rate = c.Rate.add(Rate{Passed: btoi(x.Grade.Pass), Graded: 1})
			for name, v := range x.Metrics().Fields() {
				samples[name] = append(samples[name], v)
			}
		case StatusFailedToRun:
			c.FailedToRun++
		default:
			// StatusUngradeable, and anything this build does not recognize --
			// an unset status, or one written by a newer schema. Only an
			// explicit StatusGraded may enter a pass-rate; everything else is
			// counted where it is visible instead of quietly becoming a verdict.
			c.Ungradeable++
		}
	}
	if c.Runs == 0 {
		return CellStats{}, false
	}
	if len(samples) > 0 {
		c.Metrics = make(map[string]MetricStat, len(samples))
		for name, xs := range samples {
			c.Metrics[name] = metricStat(xs)
		}
	}
	return c, true
}

// matches reports whether a run belongs to a cell selector. An empty scenario,
// condition, or version widens that dimension, which is how the aggregate rows
// are computed by the same code as the per-cell ones.
func matches(x RunResult, scenario, condition, version string) bool {
	return (scenario == "" || x.Record.Scenario == scenario) &&
		(condition == "" || x.Record.Condition.Name == condition) &&
		(version == "" || x.Record.Version == version)
}

// RateFor is the pass-rate over a selector, pooling every graded run it covers.
// An empty scenario pools across scenarios, which is what the aggregate row
// means: scenarios are weighted by their graded run count, so a cell that lost
// runs to crashes carries proportionally less of the aggregate.
func (r Results) RateFor(scenario, condition, version string) Rate {
	var rate Rate
	for _, x := range r.Runs {
		if !matches(x, scenario, condition, version) || x.Grade.Status != StatusGraded {
			continue
		}
		rate = rate.add(Rate{Passed: btoi(x.Grade.Pass), Graded: 1})
	}
	return rate
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpliftFor computes one condition's uplift over the control. Scenario "" is
// the aggregate.
func (r Results) UpliftFor(scenario, condition, version string) Uplift {
	u := Uplift{
		Scenario:   scenario,
		Condition:  condition,
		Version:    version,
		Control:    r.Control,
		HasControl: r.Control != "",
		Rate:       r.RateFor(scenario, condition, version),
	}
	if !u.HasControl {
		return u
	}
	u.ControlRate = r.RateFor(scenario, r.Control, version)
	if u.Rate.OK() && u.ControlRate.OK() {
		u.Value = u.Rate.Value() - u.ControlRate.Value()
	}
	return u
}

// ScenarioUplifts returns one row per populated scenario x condition cell at a
// version.
func (r Results) ScenarioUplifts(version string) []Uplift {
	var out []Uplift
	for _, sc := range r.Scenarios() {
		for _, cond := range r.Conditions() {
			if _, ok := r.Cell(sc, cond, version); ok {
				out = append(out, r.UpliftFor(sc, cond, version))
			}
		}
	}
	return out
}

// AggregateUplifts returns one row per condition, pooled over every scenario.
func (r Results) AggregateUplifts(version string) []Uplift {
	var out []Uplift
	for _, cond := range r.Conditions() {
		if r.RunCount("", cond, version) == 0 {
			continue
		}
		out = append(out, r.UpliftFor("", cond, version))
	}
	return out
}

// RunCount counts every run matching a selector, graded or not.
func (r Results) RunCount(scenario, condition, version string) int {
	n := 0
	for _, x := range r.Runs {
		if matches(x, scenario, condition, version) {
			n++
		}
	}
	return n
}

// VersionDeltas compares every condition's uplift between two versions, per
// scenario and aggregate (Scenario ""). A negative Value is a regression:
// Seamless helped less on the candidate than it did on the baseline.
//
// Read these next to ControlDrift. The control arm has no Seamless in it, so
// any movement there is the base model or the run environment, not this repo --
// which is what tells "Seamless got worse" apart from "the model had a bad day".
func (r Results) VersionDeltas(baseline, candidate string) []VersionDelta {
	var out []VersionDelta
	for _, sc := range append(r.Scenarios(), "") {
		for _, cond := range r.Conditions() {
			if cond == r.Control {
				continue // the control's own uplift is 0 by construction
			}
			d := VersionDelta{
				Scenario:  sc,
				Condition: cond,
				Baseline:  r.UpliftFor(sc, cond, baseline),
				Candidate: r.UpliftFor(sc, cond, candidate),
			}
			switch {
			case !d.Baseline.HasControl:
				d.Note = r.ControlNote
			case !d.Baseline.OK():
				d.Note = "no graded runs at " + baseline
			case !d.Candidate.OK():
				d.Note = "no graded runs at " + candidate
			default:
				d.OK = true
				d.Value = d.Candidate.Value - d.Baseline.Value
			}
			out = append(out, d)
		}
	}
	return out
}

// ControlDrift is the control arm's own pass-rate at each version -- the
// invariant that calibrates base-model and environment noise across a version
// comparison. It reports false when the set has no control arm.
func (r Results) ControlDrift(baseline, candidate string) (Rate, Rate, bool) {
	if r.Control == "" {
		return Rate{}, Rate{}, false
	}
	return r.RateFor("", r.Control, baseline), r.RateFor("", r.Control, candidate), true
}

// Totals counts the whole set by status, for the header line that tells a
// reader how much of the matrix actually produced evidence.
func (r Results) Totals() (total, graded, failedToRun, ungradeable int) {
	for _, x := range r.Runs {
		total++
		switch x.Grade.Status {
		case StatusGraded:
			graded++
		case StatusFailedToRun:
			failedToRun++
		default:
			ungradeable++ // see Cell: an unrecognized status is not a verdict
		}
	}
	return total, graded, failedToRun, ungradeable
}

// MinGradedPerCell is the smallest graded-run count over the populated cells --
// the n that qualifies every number in the report. Zero when nothing is graded.
func (r Results) MinGradedPerCell() int {
	n := -1
	for _, c := range r.Cells() {
		if n < 0 || c.Rate.Graded < n {
			n = c.Rate.Graded
		}
	}
	return max(n, 0)
}

// ---------------------------------------------------------------------------
// collecting and grading a run tree
// ---------------------------------------------------------------------------

// CollectOptions tunes a walk over a run tree.
type CollectOptions struct {
	// Judge enables the LLM judge layer on runs that are graded in this pass.
	Judge Judge
	// Regrade re-grades every run, ignoring (and overwriting) any persisted
	// grade. This is the after-a-grader-fix path: it costs no tokens because
	// grading only ever reads the preserved artifacts.
	Regrade bool
}

// Collect walks a run tree, grades what needs grading, and assembles the
// results set. Runs already carrying a grade.json are reused as-is unless
// Regrade is set, so re-reporting is free and a fixed grader is one flag away.
//
// Grading a run writes its grade.json back into the run dir. A run tree that
// cannot be written to still reports -- the cache is a convenience, not the
// record.
func Collect(ctx context.Context, root string, opt CollectOptions) (Results, error) {
	dirs, err := RunDirs(root)
	if err != nil {
		return Results{}, err
	}
	runs := make([]RunResult, 0, len(dirs))
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return Results{}, err
		}
		rec, g, cached, err := gradeOrReuse(ctx, dir, opt)
		if err != nil {
			return Results{}, err
		}
		if !cached {
			if err := WriteGrade(dir, g); err != nil {
				slog.Warn("could not cache a run's grade", "dir", dir, "err", err)
			}
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			rel = dir
		}
		runs = append(runs, RunResult{Record: rec, Dir: filepath.ToSlash(rel), Grade: g})
	}
	res := NewResults(root, runs)
	vp, ok, err := ReadVersionPair(root)
	if err != nil {
		return Results{}, err
	}
	if ok {
		res.Baseline, res.Candidate = vp.Baseline, vp.Candidate
	}
	return res, nil
}

// gradeOrReuse returns a run's grade, from cache when there is one and grading
// is not being forced. The bool reports that the grade came from cache (so the
// caller need not write it back).
func gradeOrReuse(ctx context.Context, dir string, opt CollectOptions) (RunRecord, Grade, bool, error) {
	rec, err := ReadRunRecord(dir)
	if err != nil {
		return RunRecord{}, Grade{}, false, err
	}
	if !opt.Regrade {
		g, ok, err := ReadGrade(dir)
		if err != nil {
			return rec, Grade{}, false, err
		}
		if ok && g.Schema == GradeSchema {
			return rec, g, true, nil
		}
	}
	return rec, GradeRun(ctx, dir, rec, opt.Judge), false, nil
}

// GradeRun turns one captured run into a classified verdict. It never returns
// an error: every way grading can go wrong is itself one of the three outcomes,
// and a report that aborted on the first unreadable run dir would throw away
// the runs that did produce evidence.
func GradeRun(ctx context.Context, dir string, rec RunRecord, judge Judge) Grade {
	g := Grade{Schema: GradeSchema, GradedAt: time.Now().UTC()}
	if rec.Error != "" {
		// The run itself failed. Its artifacts are partial by definition, so
		// there is nothing to grade -- and grading them anyway would put an
		// infrastructure failure in the pass-rate's denominator.
		g.Status = StatusFailedToRun
		g.Error = rec.Error
		return g
	}
	_, res, err := GradeRunDir(ctx, dir, judge)
	if err != nil {
		g.Status = StatusUngradeable
		g.Error = err.Error()
		if errors.Is(err, ErrMissingArtifacts) {
			g.Error = "missing artifacts: " + g.Error
		}
		return g
	}
	g.Status = StatusGraded
	g.Pass = res.Pass
	g.Details = res.Details
	g.Metrics = res.Metrics
	return g
}

// RunDirs finds every run directory under root, identified by its manifest
// rather than by its depth, so the same walk handles the single-version layout
// (<out>/<scenario>/<condition>/run-NN) and the version-comparison one
// (<out>/<version>/<scenario>/<condition>/run-NN).
func RunDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("bench: walk %s: %w", p, err)
		}
		if !d.IsDir() {
			return nil
		}
		// A run dir's preserved copies hold no manifests and can be large.
		if name := d.Name(); p != root && (name == RepoDirName || name == DataDirName) {
			return fs.SkipDir
		}
		// Never follow a link out of the tree.
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return fs.SkipDir
		}
		if _, err := os.Stat(filepath.Join(p, RunManifestFile)); err == nil {
			out = append(out, p)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}

// ---------------------------------------------------------------------------
// on-disk artifacts
// ---------------------------------------------------------------------------

// WriteGrade persists one run's verdict beside its manifest.
func WriteGrade(dir string, g Grade) error {
	return writeJSON(filepath.Join(dir, GradeFile), g, "grade")
}

// ReadGrade reads a persisted verdict, reporting whether there was one.
func ReadGrade(dir string) (Grade, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, GradeFile))
	if os.IsNotExist(err) {
		return Grade{}, false, nil
	}
	if err != nil {
		return Grade{}, false, fmt.Errorf("bench: read grade in %s: %w", dir, err)
	}
	var g Grade
	if err := json.Unmarshal(b, &g); err != nil {
		return Grade{}, false, fmt.Errorf("bench: parse grade in %s: %w", dir, err)
	}
	return g, true, nil
}

// WriteResults exports a results set as JSON.
func WriteResults(path string, r Results) error {
	return writeJSON(path, r, "results")
}

// ReadResults reads an exported results set back. A schema from the future is
// an error rather than a partial parse: a reader that silently drops fields it
// does not know would report a confidently wrong number.
func ReadResults(path string) (Results, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Results{}, fmt.Errorf("bench: read results %s: %w", path, err)
	}
	var r Results
	if err := json.Unmarshal(b, &r); err != nil {
		return Results{}, fmt.Errorf("bench: parse results %s: %w", path, err)
	}
	if r.Schema > ResultsSchema {
		return Results{}, fmt.Errorf("bench: results %s are schema %d, this build reads %d",
			path, r.Schema, ResultsSchema)
	}
	return r, nil
}

// WriteVersionPair records which version label is the baseline and which the
// candidate, at the root of a run tree.
func WriteVersionPair(root string, p VersionPair) error {
	p.Schema = ResultsSchema
	return writeJSON(filepath.Join(root, VersionsFile), p, "version pair")
}

// ReadVersionPair reads a run tree's baseline/candidate record, reporting
// whether there was one.
func ReadVersionPair(root string) (VersionPair, bool, error) {
	b, err := os.ReadFile(filepath.Join(root, VersionsFile))
	if os.IsNotExist(err) {
		return VersionPair{}, false, nil
	}
	if err != nil {
		return VersionPair{}, false, fmt.Errorf("bench: read version pair in %s: %w", root, err)
	}
	var p VersionPair
	if err := json.Unmarshal(b, &p); err != nil {
		return VersionPair{}, false, fmt.Errorf("bench: parse version pair in %s: %w", root, err)
	}
	return p, true, nil
}

func writeJSON(path string, v any, what string) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshal %s: %w", what, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("bench: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("bench: write %s: %w", path, err)
	}
	return nil
}
