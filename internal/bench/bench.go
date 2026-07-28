// Package bench defines the agent-scenario benchmark: the scenarios an agent
// is run against, the named conditions (arms) each scenario runs under, and
// the grading contract that turns a captured run into pass/fail + metrics.
// cmd/seambench drives it; scripts/fixture/harness.sh --mode bench builds the
// arms it runs in.
//
// This is the AGENT-SCENARIO benchmark (cmd/seambench, make seambench), not
// the Go hot-path micro-benchmarks behind make bench -- keep the two distinct.
//
// Scenario fixtures here are FORKED from the terminal-scene specs in
// internal/demokit (scenes.go), on purpose: the scene defs are branding
// surface (the landing-page recordings) that must stay stable, while this
// suite grows and churns scenario by scenario. The fork reuses demokit's
// seeding primitives -- the backdating helpers and store/files wrappers --
// but owns its data.
package bench

import (
	"context"
	"slices"

	"github.com/0spoon/seamless/internal/demokit"
)

// SeedFunc builds a scenario's fixture state inside a fresh throwaway
// Seamless data dir. The runner hands it a demokit seeder already opened on
// that dir, plus the arm's demo-repo path to map to the scenario's project so
// sessions starting there bind to it ("" skips the mapping). It writes only
// to the data dir and DB, never into the repo working tree (memory
// scene-demo-repo-must-be-seamless-free), and must never be pointed at a live
// instance -- demokit.New's contract.
type SeedFunc func(s *demokit.Seeder, repoPath string) error

// Scenario is one benchmark scenario: a seeded starting state, the prompt the
// agent gets, and how the outcome is graded.
type Scenario struct {
	// Name identifies the scenario in condition matrices, trial tags, and
	// reports.
	Name string
	// Prompt is the user prompt the headless runner feeds the agent, the same
	// on every arm.
	Prompt string
	// Seed builds the scenario's fixture state (memories, plan tasks,
	// findings, trials) via demokit.
	Seed SeedFunc
	// Grader scores a captured run; nil until the grader step of
	// plan:seambench lands.
	Grader Grader
	// RequiresRecall marks scenarios whose signal depends on the mid-session
	// UserPromptSubmit <seam-recall> injection. Headless `claude -p` fires
	// SessionStart but not UserPromptSubmit, so these cannot run as plain -p
	// takes; the runner validates the recall mechanism at the hook-API level
	// instead (memory headless-cc-p-skips-userpromptsubmit-hook).
	RequiresRecall bool
}

// RunArtifacts is what the headless runner captures from one completed run
// and hands to the grader. Every path points inside the run's preserved
// artifact directory (see RunDir), not at the live arm, so grading is fully
// decoupled from running: a run dir can be graded later, on another machine,
// or synthesized by a test.
type RunArtifacts struct {
	Scenario   string
	Condition  Condition
	Dir        string // the run's artifact directory
	RepoDir    string // preserved copy of the arm's demo-repo working tree after the run
	RepoDiff   string // unified git diff against the pre-run snapshot
	DataDir    string // preserved copy of the arm's Seamless data dir; "" on vanilla arms
	Transcript string // path to the copied agent transcript (.jsonl); "" if none was produced
}

// Result is one graded run: the verdict, the per-check trace behind it, and
// the measurements the report aggregates.
type Result struct {
	Pass    bool
	Details []string // one human-readable line per check
	Metrics Metrics
}

// Grader scores one captured run, combining repo-state assertions, event-log
// checks against the arm's data dir, and an optional LLM judge over the
// transcript. Implementations land with the grader step of plan:seambench.
type Grader interface {
	Grade(ctx context.Context, a RunArtifacts) (Result, error)
}

// scenarios is the benchmark scenario table. Grow it here; every entry needs
// a distinct name, a prompt, and a seed (bench_test asserts the shape).
var scenarios = []Scenario{authRefresh}

// Scenarios returns the benchmark scenario table.
func Scenarios() []Scenario { return slices.Clone(scenarios) }

// ScenarioByName returns the named scenario, reporting whether it exists.
func ScenarioByName(name string) (Scenario, bool) {
	for _, sc := range scenarios {
		if sc.Name == name {
			return sc, true
		}
	}
	return Scenario{}, false
}
