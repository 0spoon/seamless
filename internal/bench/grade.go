// The grading half of a run. A grader reads ONE preserved run directory and
// nothing else: never a live arm, never the runner's process state. That is what
// lets a run be graded later, on another machine, or synthesized by a test.
//
// A scenario's grader is a composition of three signal layers:
//
//  1. repo-state assertions  -- did the agent make the RIGHT change? (repostate.go)
//  2. event-log checks       -- did the mechanism fire, and get used? (eventlog.go)
//  3. an optional LLM judge  -- the fuzzy remainder (judge.go)
//
// so a new scenario is "these repo assertions + these event-log checks + this
// rubric" and nothing more.
//
// GATE vs OBSERVED. Every check reports, but only a gating check can fail the
// run. The split is deliberate and load-bearing for the number this benchmark
// produces: the primary metric is pass-rate(condition) - pass-rate(vanilla), so
// the verdict has to mean the same thing on every arm. Outcome checks (the repo
// assertions) gate everywhere. Of the event-log checks only defects gate -- the
// mechanism failing to fire at all, or a write misfiring out of the scenario's
// project. "The agent read the memory / moved the task / left a finding" is
// recorded and measured but does not gate, because gating it would mark a
// Seamless arm that solved the task correctly as a failure while the vanilla
// arm doing the same thing passes, which understates the very uplift being
// measured.
//
// The LLM judge never gates: it is additive commentary in Details, and its
// absence (no provider configured, or an outage) degrades the run instead of
// failing it.

package bench

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrMissingArtifacts means the run directory cannot be graded at all -- no
// preserved working tree, or a data dir with no database. It is distinct from a
// graded failure: the run produced no evidence rather than the wrong evidence,
// and a report must not count it as a fail.
var ErrMissingArtifacts = errors.New("bench: run artifacts are not gradeable")

// repoCheck is one assertion over the preserved working tree. fn returns the
// verdict plus a one-line evidence string, which is recorded either way.
type repoCheck struct {
	name string
	gate bool
	fn   func(*repoTree) (bool, string)
}

// eventCheck is one assertion over the preserved data dir: its event log, its
// DB end state, or both. The error return is for a broken artifact (an
// unreadable DB), never for a failed assertion.
type eventCheck struct {
	name string
	gate bool
	fn   func(*runLog) (bool, string, error)
}

// rubricGrader is the composition every scenario's grader is built from.
type rubricGrader struct {
	// scenario and project name the run being graded; project is the slug the
	// scenario seeded, which the event-log checks scope to.
	scenario string
	project  string
	repo     []repoCheck
	events   []eventCheck
	// rubric is the LLM judge's per-scenario instruction. Empty disables the
	// judge layer for this scenario regardless of whether one is configured.
	rubric string
	// judge is nil unless a run enables it (WithJudge).
	judge Judge
}

var _ Grader = (*rubricGrader)(nil)

// WithJudge returns a copy of g with the LLM judge layer enabled. The scenario
// table holds judge-less graders so that grading needs no provider by default;
// cmd/seambench attaches one per run when the owner asks for it.
func WithJudge(g Grader, j Judge) Grader {
	rg, ok := g.(*rubricGrader)
	if !ok || j == nil {
		return g
	}
	c := *rg
	c.judge = j
	return &c
}

// GradeRunDir loads a preserved run directory and grades it with the grader of
// the scenario its manifest names -- the whole grading entry point, since a run
// dir is the entire handoff from the runner. Pass a judge to enable the LLM
// layer for this run, or nil to grade on assertions + event log alone.
//
// The returned Result carries only the grader's half of Metrics; the run-shape
// half (turns, tokens, cost, duration) stays where the runner wrote it, on the
// returned RunRecord.
func GradeRunDir(ctx context.Context, dir string, judge Judge) (RunRecord, Result, error) {
	rec, a, err := LoadRun(dir)
	if err != nil {
		return RunRecord{}, Result{}, err
	}
	sc, ok := ScenarioByName(rec.Scenario)
	if !ok {
		return rec, Result{}, fmt.Errorf("bench: run %s names unknown scenario %q", dir, rec.Scenario)
	}
	if sc.Grader == nil {
		return rec, Result{}, fmt.Errorf("bench: scenario %s has no grader", sc.Name)
	}
	res, err := WithJudge(sc.Grader, judge).Grade(ctx, a)
	if err != nil {
		return rec, Result{}, err
	}
	return rec, res, nil
}

// Grade scores one captured run. It returns an error only when the artifacts
// cannot be read; a run that simply did the wrong thing comes back as
// Pass=false with the failing checks in Details.
func (g *rubricGrader) Grade(ctx context.Context, a RunArtifacts) (Result, error) {
	res := Result{Pass: true}

	tree, err := loadRepoTree(a.RepoDir, a.RepoDiff)
	if err != nil {
		return Result{}, err
	}
	for _, c := range g.repo {
		ok, detail := c.fn(tree)
		res.Pass = res.Pass && (ok || !c.gate)
		res.Details = append(res.Details, checkLine("repo", c.gate, ok, c.name, detail))
	}

	// A vanilla arm preserves no data dir: it is the control, and the verdict
	// rests on the repo assertions alone.
	if a.DataDir == "" {
		res.Details = append(res.Details, "event: n/a -- no data dir preserved (Seamless-free arm: the control)")
	} else {
		rl, err := openRunLog(ctx, a.DataDir, g.project)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = rl.close() }()
		if rl.Saturated {
			res.Details = append(res.Details,
				fmt.Sprintf("event: WARNING -- log hit the %d-event read cap; metrics are a lower bound", eventReadCap))
		}
		res.Metrics = rl.metrics()
		for _, c := range g.events {
			ok, detail, err := c.fn(rl)
			if err != nil {
				return Result{}, err
			}
			res.Pass = res.Pass && (ok || !c.gate)
			res.Details = append(res.Details, checkLine("event", c.gate, ok, c.name, detail))
		}
	}

	res.Details = append(res.Details, g.judgeLine(ctx, a))
	return res, nil
}

// repoTouched is the one repo check every scenario shares. It is diagnostic,
// not a gate: the tree is the ground truth, and the diff exists to separate
// "the agent changed nothing" from "the runner captured no diff" when the
// tree already proves otherwise.
func repoTouched() repoCheck {
	return repoCheck{name: "the working tree changed", fn: func(t *repoTree) (bool, string) {
		if !t.changed() {
			return false, "empty diff -- the agent changed nothing, or the run captured no diff"
		}
		return true, fmt.Sprintf("%d diff lines", strings.Count(strings.TrimSpace(t.Diff), "\n")+1)
	}}
}

// checkLine renders one check for Result.Details. The layer and the gate/obs
// marker are in the line because a reader of a failed run needs to know at a
// glance which check moved the verdict.
func checkLine(layer string, gate, ok bool, name, detail string) string {
	scope := "obs"
	if gate {
		scope = "gate"
	}
	verdict := "FAIL"
	if ok {
		verdict = "PASS"
	}
	line := fmt.Sprintf("%s/%s: %s -- %s", layer, scope, name, verdict)
	if detail != "" {
		line += ": " + detail
	}
	return line
}
