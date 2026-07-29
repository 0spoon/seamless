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

// Step is one agent session within a scenario, run in order. Each step is its
// own headless invocation, so SessionStart (and the briefing on a Seamless-ful
// arm) fires per step -- which is exactly what a handoff scenario measures:
// what one session recorded is all a later session can inherit.
type Step struct {
	// Name labels the step in artifacts and logs ("investigate", "fix"); the
	// runner falls back to a positional label when empty.
	Name string
	// Prompt is what this step's agent session is asked.
	Prompt string
	// Evidence maps repo-relative paths to contents the runner materializes
	// into the working tree before this step and removes after it. Evidence is
	// scenario WORLD-STATE (an incident log the agent was pointed at), never
	// Seamless scaffolding, so it does not breach the seeds-write-only-to-the-
	// data-dir rule (memory scene-demo-repo-must-be-seamless-free). Paths must
	// be repo-local and must not collide with a file the fixture ships; the
	// runner removes the files before any diff or capture, so evidence never
	// appears in a graded tree.
	Evidence map[string]string
	// FreshRepo resets the working tree to the arm snapshot before this step,
	// so nothing the previous step's agent left in the tree -- notes files
	// included -- carries over. The Seamless data dir is deliberately NOT
	// reset: persistence across sessions is the thing being measured, and on a
	// vanilla arm nothing persists, which is exactly the control.
	FreshRepo bool
}

// Scenario is one benchmark scenario: a seeded starting state, the prompt the
// agent gets, and how the outcome is graded.
type Scenario struct {
	// Name identifies the scenario in condition matrices, trial tags, and
	// reports.
	Name string
	// Prompt is the user prompt the headless runner feeds the agent, the same
	// on every arm. It is sugar for a single-session scenario; a multi-session
	// scenario sets Steps instead, and setting both is a table error
	// (selectScenarios and bench_test refuse it).
	Prompt string
	// Steps is the ordered agent-session list for a multi-session scenario.
	// Leave nil and set Prompt for the common single-session case.
	Steps []Step
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
	// instead (memory headless-cc-p-skips-userpromptsubmit-hook). Incompatible
	// with Steps: the recall path is a component check, not a session sequence.
	RequiresRecall bool
}

// Sessions normalizes the scenario to its ordered session list: Prompt is
// sugar for a single anonymous step.
func (sc Scenario) Sessions() []Step {
	if len(sc.Steps) > 0 {
		return slices.Clone(sc.Steps)
	}
	return []Step{{Prompt: sc.Prompt}}
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
	// Steps holds the EARLIER sessions' preserved artifacts of a multi-step
	// run, oldest first; the final session's transcript and tree are the
	// top-level fields above. Empty for a single-session run.
	Steps []StepArtifacts
}

// StepArtifacts is one non-final session's preserved evidence in a multi-step
// run. The final tree is what gets graded; these exist so the judge and a
// debugging human can see what each earlier session did.
type StepArtifacts struct {
	Name       string
	RepoDiff   string // that session's diff against the arm snapshot
	Transcript string // path to that session's transcript; "" if none was produced
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

// scenarios is the benchmark scenario table -- one entry per mechanism of
// value, each with a small graded surface and an honest vanilla contrast:
//
//	cookie-hardening  a constraint memory vetoes a confidently-wrong ticket
//	stale-assets      a gotcha steers the fix around invisible CDN behaviour
//	deploy-drain      continue-work: the plan names the step, the memory its shape
//	restart-logouts   the two-session handoff: session A's record is session B's context
//	refresh-grace     recorded failed trials rule the tempting fix out
//
// Grow it here; every entry needs a distinct name, a prompt (or Steps), and a
// seed (bench_test asserts the shape).
var scenarios = []Scenario{cookieHardening, staleAssets, deployDrain, restartLogouts, refreshGrace}

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
