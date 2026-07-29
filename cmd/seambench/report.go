// `seambench report`: the measurement half.
//
// WHY GRADING LIVES HERE AND NOT IN `run`. A run costs real tokens; grading
// costs a file read. Keeping them apart means a grader fix is re-applied to
// every run ever captured (`report --regrade`) without re-running anything, and
// a run tree copied off the machine can be graded anywhere. `run` therefore
// writes artifacts and nothing else, and every verdict in this report came from
// a run directory -- never from a live arm and never from the runner's memory.
//
// The report walks the run tree, grades what has no cached verdict, writes
// results.json, prints the tables, and only then records the live trials. That
// order is deliberate: a run that already cost tokens must not be lost because
// a trial write failed, so the durable file lands first and an unreachable live
// instance is a loud warning rather than an error.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/0spoon/seamless/internal/bench"
	"github.com/0spoon/seamless/internal/config"
)

// aggregateRow is the scenario-column label for a row pooled over scenarios.
const aggregateRow = "ALL"

func runReport(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: seambench report [flags]\n\n"+
			"Grades every captured run under --out, writes results.json, and prints\n"+
			"per-scenario pass-rates and with-vs-without uplift -- plus a\n"+
			"baseline-vs-candidate delta table when the tree holds two versions.\n\n"+
			"Grading reads only the preserved run directories, so it is free to repeat:\n"+
			"--regrade re-applies a fixed grader to runs that already cost their tokens.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var (
		out       = fs.String("out", "", "artifact root holding the captured runs (default <base>/runs)")
		jsonPath  = fs.String("json", "", "where to write the results set (default <out>/"+bench.ResultsFile+")")
		baseline  = fs.String("baseline", "", "baseline version label for the delta table (default: from "+bench.VersionsFile+")")
		candidate = fs.String("candidate", "", "candidate version label for the delta table (default: from "+bench.VersionsFile+")")
		regrade   = fs.Bool("regrade", false, "re-grade every run instead of reusing cached verdicts")
		noTrials  = fs.Bool("no-trials", false, "do not record the results as research-lab trials in the live instance")
		judgeOn   = fs.Bool("judge", false, "enable the advisory LLM judge on runs graded in THIS pass (never gates; cached grade.json verdicts skip it, so pair with --regrade to judge existing runs)")
		judgeCfg  = fs.String("judge-config", "", "Seamless config file whose llm: section builds the judge (default: the standard search order incl. $SEAMLESS_CONFIG)")
	)
	trials := addTrialFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := *out
	if root == "" {
		root = filepath.Join(defaultBase(), "runs")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve --out %s: %w", *out, err)
	}
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("no run tree at %s (run `seambench run` first): %w", root, err)
	}

	judge, err := buildJudge(*judgeOn, *judgeCfg)
	if err != nil {
		return err
	}
	res, err := bench.Collect(ctx, root, bench.CollectOptions{Regrade: *regrade, Judge: judge})
	if err != nil {
		return err
	}
	if len(res.Runs) == 0 {
		return fmt.Errorf("no run directories under %s (run `seambench run` first)", root)
	}

	// The durable export first: everything after this point is presentation or
	// a live write, and neither may be able to lose the results.
	export := *jsonPath
	if export == "" {
		export = filepath.Join(root, bench.ResultsFile)
	}
	if err := bench.WriteResults(export, res); err != nil {
		return err
	}

	base, cand := resolveVersionPair(res, *baseline, *candidate)
	renderReport(w, res, base, cand)
	fmt.Fprintf(w, "\nresults: %s\n", export)

	if *noTrials {
		fmt.Fprintln(w, "trials: skipped (--no-trials)")
		return nil
	}
	return recordTrialsForReport(ctx, trials, root, res, w)
}

// buildJudge constructs the advisory LLM judge when --judge asks for one. The
// operator asked explicitly, so a provider that cannot be built is a LOUD
// error here -- construction is where misconfiguration must surface; only a
// per-run judge failure degrades (internal/bench/judge.go). The report is a
// daemon-less CLI, so the LLM config comes from the standard Seamless config
// search order, or from an explicit --judge-config file (which is also how a
// bench-specific judge model is chosen: a yaml, not a flag).
func buildJudge(on bool, cfgPath string) (bench.Judge, error) {
	if !on {
		return nil, nil
	}
	var (
		cfg config.Config
		err error
	)
	if cfgPath != "" {
		cfg, err = config.LoadFrom(cfgPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return nil, fmt.Errorf("--judge: %w", err)
	}
	judge, err := bench.NewLLMJudge(cfg.LLM)
	if err != nil {
		return nil, fmt.Errorf("--judge: %w", err)
	}
	return judge, nil
}

// resolveVersionPair settles which two version labels the delta table compares:
// the flags when given, else what the runner recorded when it produced the
// tree. Two versions with no recorded pair and no flags is genuinely ambiguous
// -- there is no way to tell which is the baseline -- so it resolves to no
// comparison and the report says so, rather than guessing a direction and
// printing a regression that might be an improvement.
func resolveVersionPair(res bench.Results, baseline, candidate string) (string, string) {
	if baseline == "" {
		baseline = res.Baseline
	}
	if candidate == "" {
		candidate = res.Candidate
	}
	if baseline == "" || candidate == "" || baseline == candidate {
		return "", ""
	}
	return baseline, candidate
}

// renderReport prints the whole report: a header, one pass-rate/uplift and
// metrics section per version, and the version-delta table when there are two.
// Plain text, no colour, no unicode -- it is read in a terminal and pasted into
// notes.
func renderReport(w io.Writer, res bench.Results, baseline, candidate string) {
	renderHeader(w, res)
	for _, v := range res.Versions() {
		fmt.Fprintf(w, "\n=== version %s%s ===\n", v, versionRole(v, baseline, candidate))
		renderUplift(w, res, v)
		renderMetrics(w, res, v)
	}
	if baseline != "" && candidate != "" {
		renderVersionDelta(w, res, baseline, candidate)
		return
	}
	if len(res.Versions()) > 1 {
		fmt.Fprintf(w, "\n%d versions are present but the tree does not record which is the baseline.\n"+
			"Pass --baseline LABEL --candidate LABEL for the delta table.\n", len(res.Versions()))
	}
}

func versionRole(v, baseline, candidate string) string {
	switch v {
	case baseline:
		return " (baseline)"
	case candidate:
		return " (candidate)"
	default:
		return ""
	}
}

func renderHeader(w io.Writer, res bench.Results) {
	total, graded, failed, ungradeable := res.Totals()
	fmt.Fprintf(w, "seambench report -- %s\n", res.Root)
	fmt.Fprintf(w, "  runs:      %d total: %d graded, %d failed to run, %d ungradeable\n",
		total, graded, failed, ungradeable)
	if failed+ungradeable > 0 {
		fmt.Fprintf(w, "             (a failed or ungradeable run is NOT a failed verdict; "+
			"neither is in any pass-rate below)\n")
	}
	fmt.Fprintf(w, "  matrix:    %d scenario(s) x %d condition(s) x %d version(s)\n",
		len(res.Scenarios()), len(res.Conditions()), len(res.Versions()))
	if res.Control != "" {
		fmt.Fprintf(w, "  control:   %s (uplift = pass-rate - control pass-rate)\n", res.Control)
	} else {
		fmt.Fprintf(w, "  control:   NONE -- %s\n", res.ControlNote)
	}
	if n := res.MinGradedPerCell(); n > 0 {
		fmt.Fprintf(w, "  smallest cell: n=%d graded run(s); one run moves that pass-rate by %.2f. "+
			"Read anything smaller as noise.\n", n, 1/float64(n))
	}
}

// renderUplift prints the per-scenario rows then the pooled aggregate rows for
// one version. Every rate carries its counts, and the two non-verdict outcomes
// get their own columns so a hole in the matrix is visible rather than implied.
func renderUplift(w io.Writer, res bench.Results, version string) {
	tw := newTable(w)
	fmt.Fprintln(tw, "\nscenario\tcondition\tpass-rate\tuplift\tfailed\tungradeable")
	row := func(u bench.Uplift, label string, c bench.CellStats) {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\n",
			label, u.Condition, u.Rate.String(), upliftCell(res, u), c.FailedToRun, c.Ungradeable)
	}
	for _, u := range res.ScenarioUplifts(version) {
		c, _ := res.Cell(u.Scenario, u.Condition, u.Version)
		row(u, u.Scenario, c)
	}
	if len(res.Scenarios()) > 1 {
		for _, u := range res.AggregateUplifts(version) {
			row(u, aggregateRow, aggregateCell(res, u.Condition, u.Version))
		}
	}
	flush(tw)
}

// aggregateCell pools a condition's cells across scenarios, for the counts the
// aggregate row shows.
func aggregateCell(res bench.Results, condition, version string) bench.CellStats {
	var out bench.CellStats
	for _, sc := range res.Scenarios() {
		if c, ok := res.Cell(sc, condition, version); ok {
			out.Runs += c.Runs
			out.FailedToRun += c.FailedToRun
			out.Ungradeable += c.Ungradeable
		}
	}
	return out
}

// upliftCell renders the uplift figure, or why there is not one. The control
// arm's own row shows a dash rather than "+0.00": it is the reference, not a
// measurement of itself.
func upliftCell(res bench.Results, u bench.Uplift) string {
	switch {
	case !u.HasControl:
		return "n/a"
	case u.Condition == res.Control:
		return "-"
	case !u.OK():
		return "n/a"
	default:
		return fmt.Sprintf("%+.2f", u.Value)
	}
}

// renderMetrics prints the cost-and-shape columns for one version, mean and
// sample sd over each cell's graded runs.
func renderMetrics(w io.Writer, res bench.Results, version string) {
	cells := []bench.CellStats{}
	for _, c := range res.Cells() {
		if c.Version == version && c.Rate.Graded > 0 {
			cells = append(cells, c)
		}
	}
	if len(cells) == 0 {
		return
	}
	tw := newTable(w)
	fmt.Fprintf(tw, "\nmetrics (mean +- sd over graded runs)\nscenario\tcondition\tn\t%s\n",
		strings.Join(bench.ReportedMetrics, "\t"))
	for _, c := range cells {
		cols := make([]string, 0, len(bench.ReportedMetrics))
		for _, name := range bench.ReportedMetrics {
			cols = append(cols, formatStat(c.Metrics[name]))
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", c.Scenario, c.Condition, c.Rate.Graded, strings.Join(cols, "\t"))
	}
	flush(tw)
}

// renderVersionDelta prints the regression table plus the control calibration
// that qualifies it.
func renderVersionDelta(w io.Writer, res bench.Results, baseline, candidate string) {
	fmt.Fprintf(w, "\n=== version delta: %s -> %s ===\n", baseline, candidate)
	fmt.Fprintln(w, "delta = uplift(candidate) - uplift(baseline); a negative delta is a REGRESSION")

	deltas := res.VersionDeltas(baseline, candidate)
	if len(deltas) == 0 {
		// Every arm is the control, so there is no uplift to compare. Saying so
		// beats an empty table under a heading, which reads as a bug.
		fmt.Fprintf(w, "\nnothing to compare: %s is the only arm, and an uplift needs a\n"+
			"non-control condition. Re-run with --conditions vanilla,mechanism[,full].\n", res.Control)
		return
	}
	tw := newTable(w)
	fmt.Fprintf(tw, "\nscenario\tcondition\tuplift@%s\tuplift@%s\tdelta\t\n", baseline, candidate)
	for _, d := range deltas {
		label := d.Scenario
		if label == "" {
			label = aggregateRow
		}
		if !d.OK {
			fmt.Fprintf(tw, "%s\t%s\tn/a\tn/a\tn/a\t%s\n", label, d.Condition, d.Note)
			continue
		}
		mark := ""
		if d.Value < 0 {
			mark = "REGRESSION"
		}
		fmt.Fprintf(tw, "%s\t%s\t%+.2f\t%+.2f\t%+.2f\t%s\n",
			label, d.Condition, d.Baseline.Value, d.Candidate.Value, d.Value, mark)
	}
	flush(tw)

	base, cand, ok := res.ControlDrift(baseline, candidate)
	if !ok {
		return
	}
	fmt.Fprintf(w, "\ncontrol calibration -- the %s arm has no Seamless in it, so movement here is the\n"+
		"model or the environment, not this repo:\n", res.Control)
	fmt.Fprintf(w, "  %s: %s  ->  %s: %s   (change %+.2f)\n",
		baseline, base.String(), candidate, cand.String(), cand.Value()-base.Value())
	if base.OK() && cand.OK() && base.Value() != cand.Value() {
		fmt.Fprintln(w, "  The control moved: read the deltas above as differences ON TOP of that drift.")
	}
}

// formatStat renders "mean +- sd" at a precision that suits the magnitude, so
// a token count and a dollar cost can share a table.
func formatStat(s bench.MetricStat) string {
	if s.N == 0 {
		return "-"
	}
	if s.N == 1 || s.StdDev == 0 {
		return formatValue(s.Mean)
	}
	return formatValue(s.Mean) + " +- " + formatValue(s.StdDev)
}

func formatValue(v float64) string {
	av := v
	if av < 0 {
		av = -av
	}
	switch {
	case av == 0:
		return "0"
	case av >= 1000:
		return fmt.Sprintf("%.0f", v)
	case av >= 10:
		return fmt.Sprintf("%.1f", v)
	case av >= 1:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.4f", v)
	}
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// flush renders one buffered table. A write failure on the report's own output
// stream cannot be reported through that stream, so it goes to stderr and the
// report continues -- results.json is already on disk before anything prints.
func flush(tw *tabwriter.Writer) {
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "seambench: writing the report table: %v\n", err)
	}
}
