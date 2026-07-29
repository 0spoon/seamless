// The deploy-drain grader: four repo assertions + five event-log checks + one
// rubric.
//
// What the scenario is testing: the memory lb-healthz-drain-window says
// graceful shutdown on THIS infrastructure means failing /healthz first and
// waiting out the LB's poll window before srv.Shutdown. The textbook fix --
// signal handling + Shutdown, healthz untouched -- is what an agent writes
// without that memory, and it leaves the resets exactly where they were.
//
// The two gates are deliberately ORTHOGONAL:
//
//	repo/gate: graceful shutdown on SIGTERM -- did the agent do the work at all?
//	repo/gate: healthz drains before shutdown -- did it do the part only the memory knows?
//
// changed nothing        -> first FAILs, second FAILs ("healthz never fails").
// textbook Shutdown only -> first PASSes, second FAILs naming the memory.
// drain-then-shutdown    -> both PASS.
//
// The discriminator leans on a clean structural fact: the natural textbook
// fix never touches the health endpoint, so "a healthz-serving function knows
// how to answer 503" separates the two shapes at FUNCTION granularity without
// caring how the flag, channel, or atomic behind it is spelled. Ordering
// (drain wait strictly before Shutdown) is beyond token matching, so the wait
// is an OBSERVED check and the rubric carries the sequencing question.
//
// Known blind spot: a healthz handler that writes a literal 503 status code
// (w.WriteHeader(503)) instead of naming http.StatusServiceUnavailable is
// invisible to the token scan (numeric literals are not code text) and grades
// as the textbook shape. Raw numerics are rare against the stdlib constant;
// the error direction understates uplift, which is the safe one.

package bench

import (
	"fmt"
	"strings"
)

// shutdownTerms mark graceful-shutdown machinery.
var shutdownTerms = []string{"shutdown"}

// signalTerms mark SIGTERM handling: the idents and the os/signal import path
// (import paths are code text).
var signalTerms = []string{"sigterm", "notifycontext", "os/signal", "signal"}

// healthzTerms locate the health-endpoint surface: its route string and its
// conventional handler name.
var healthzTerms = []string{"healthz", "health"}

// unavailableTerms mark a handler that can answer "take me out of rotation".
// statusserviceunavailable is the idiomatic spelling; the others catch a
// renamed constant or a string status.
var unavailableTerms = []string{"statusserviceunavailable", "unavailable", "shutting down", "draining"}

// drainWaitTerms mark a deliberate wait in the shutdown path.
var drainWaitTerms = []string{"sleep", "after", "timer", "tick", "drain", "delay", "wait"}

// gracefulShutdownOnSigterm is the "did the agent do the work?" gate: the
// tree must handle SIGTERM and shut the server down rather than letting the
// process die mid-request. Both an informed and a textbook fix pass here.
func gracefulShutdownOnSigterm() repoCheck {
	return repoCheck{name: "graceful shutdown on SIGTERM", gate: true, fn: func(t *repoTree) (bool, string) {
		shutdownFiles := t.with(shutdownTerms...)
		if len(shutdownFiles) == 0 {
			return false, "no shutdown machinery in the tree -- the process still dies mid-request"
		}
		if hits := matchedIn(t.Files, signalTerms...); len(hits) > 0 {
			return true, fmt.Sprintf("%s (%s)", filePaths(shutdownFiles), strings.Join(hits, ", "))
		}
		return false, "shutdown code in " + filePaths(shutdownFiles) + " but nothing handles SIGTERM"
	}}
}

// healthzFailsFirst is the check the scenario exists for: the LB only stops
// routing when /healthz fails, so a healthz-serving function must know how to
// answer 503 (memory lb-healthz-drain-window). The natural textbook fix never
// touches the health endpoint, which is what makes this a clean function-level
// discriminator.
func healthzFailsFirst() repoCheck {
	return repoCheck{name: "healthz drains before shutdown", gate: true, fn: func(t *repoTree) (bool, string) {
		var healthFuncs []string
		var able []string
		for _, f := range t.Files {
			if !f.Parsed {
				// A half-finished edit still grades: judge the file whole.
				if _, ok := f.has(healthzTerms...); ok {
					healthFuncs = append(healthFuncs, f.Path)
					if _, ok := f.has(unavailableTerms...); ok {
						able = append(able, f.Path)
					}
				}
				continue
			}
			for _, fn := range f.Funcs {
				if _, ok := firstTerm(fn.Code, healthzTerms...); !ok {
					continue
				}
				healthFuncs = append(healthFuncs, fn.Name)
				if _, ok := firstTerm(fn.Code, unavailableTerms...); ok {
					able = append(able, fn.Name)
				}
			}
		}
		switch {
		case len(healthFuncs) == 0:
			return false, "the health endpoint is gone from the tree"
		case len(able) > 0:
			return true, "healthz can answer unavailable: " + strings.Join(able, ", ")
		default:
			return false, fmt.Sprintf(
				"no healthz code can fail the check -- the LB keeps routing into the dying process for its full poll window (memory %s)",
				deployDrainMemory)
		}
	}}
}

// drainWaitObserved reports whether the shutdown path carries a deliberate
// wait. Observed, not a gate: token matching cannot prove ordering, so the
// rubric owns the sequencing question.
func drainWaitObserved() repoCheck {
	return repoCheck{name: "a drain wait in the shutdown path", fn: func(t *repoTree) (bool, string) {
		for _, f := range t.Files {
			for _, fn := range f.Funcs {
				if !strings.Contains(fn.Code, "shutdown") {
					continue
				}
				if term, ok := firstTerm(fn.Code, drainWaitTerms...); ok {
					return true, fmt.Sprintf("%q in %s", term, fn.Name)
				}
			}
		}
		return false, "no wait between failing healthz and Shutdown is visible to the token scan"
	}}
}

// deployDrainRubric is the LLM judge's instruction: only what the assertions
// cannot see -- above all the ORDER of the sequence.
const deployDrainRubric = `The agent was asked to continue a zero-downtime-deploys effort on a small Go web service that
drops requests during every deploy. The load balancer in front of it polls /healthz every 5 seconds
and takes an instance out of rotation only after two consecutive failures, so the correct shutdown
sequence on SIGTERM is: fail /healthz immediately, KEEP SERVING while the LB notices (at least
~12s), then call srv.Shutdown to drain in-flight requests. Plain signal handling + Shutdown without
the healthz flip leaves the resets in place -- the LB keeps routing new connections into the dying
process.

PASS when the transcript shows the agent:
- identified graceful shutdown as the step to continue, rather than inventing unrelated work;
- sequenced the shutdown correctly: healthz fails first, a drain wait, then Shutdown;
- showed it understood WHY -- that the LB only reacts to the health check, on a poll cadence.

FAIL when it shipped signal handling + Shutdown with the health endpoint untouched, removed or
broke the health endpoint, shut down before the LB could notice, worked on something else, or
claimed work the transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// deployDrainGrader wires the layers. Gating: the repo assertions plus the
// two event-log DEFECT checks; the remaining event checks measure how the
// agent used Seamless without gating the verdict (grade.go says why).
var deployDrainGrader = &rubricGrader{
	scenario: deployDrainName,
	project:  benchProject,
	repo: []repoCheck{
		repoTouched(),
		gracefulShutdownOnSigterm(),
		healthzFailsFirst(),
		drainWaitObserved(),
	},
	events: []eventCheck{
		briefingInjected(),
		memoryConsulted(deployDrainMemory),
		planStepMoved(deployDrainPlan, deployDrainStep),
		findingRecorded(),
		writesScopedToProject(benchProject),
	},
	rubric: deployDrainRubric,
}
