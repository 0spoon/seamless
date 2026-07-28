// The auth-refresh grader: three repo assertions + five event-log checks + one
// rubric. Everything it is built from lives in grade.go / repostate.go /
// eventlog.go, so the next scenario is a table like this one and no new
// machinery.
//
// What the scenario is testing: the seeded plan's one claimable step is
// "rate-limit POST /auth/refresh", and the memory rate-limit-not-in-memory says
// the counter must live in shared storage because myapp runs several instances
// behind a load balancer. The wrong answer -- the one an agent reaches for
// without that memory -- is a per-process map. So the repo assertions ask two
// questions: is there a limiter on the refresh path at all, and does its
// counter outlive one process?
//
// They are written to accept any reasonable correct solution (a Redis client, a
// SQL-backed counter, an injected shared store) and to reject the obvious wrong
// one, rather than to recognize one exact implementation.

package bench

import (
	"fmt"
	"strings"
)

// limiterTerms are the code tokens that mark rate-limiting code. Matching is
// substring-over-lowercased-identifiers, so rateLimiter, RateLimit,
// throttleRefresh, and http.StatusTooManyRequests all hit.
var limiterTerms = []string{"ratelimit", "rate_limit", "limiter", "throttl", "toomanyrequests", "quota"}

// refreshPathTerms locate the endpoint under test -- its route string and its
// handler -- so a limiter that exists but is wired to nothing does not pass.
var refreshPathTerms = []string{"/auth/refresh", "handlerefresh"}

// sharedBackendTerms name a store that outlives one process and is reachable
// from every instance. The fixture repo is stdlib-only and ships none of them,
// so any occurrence is the agent's own doing.
var sharedBackendTerms = []string{
	"redis", "valkey", "memcache", "dynamodb", "cassandra", "etcd", "consul",
	"postgres", "pgx", "lib/pq", "mysql", "database/sql", "sqlx",
}

// sharedIntentTerms are naming that only shows up when the counter is
// deliberately not process-local (sharedStore, distributedLimiter, clusterKey).
// They are matched inside the limiter's own code and never in comments -- an
// identifier is a design, a comment is a wish.
var sharedIntentTerms = []string{"shared", "distributed", "cluster"}

// repoTouched is diagnostic, not a gate: the tree is the ground truth, and the
// diff exists to separate "the agent changed nothing" from "the runner captured
// no diff" when the tree already proves otherwise.
func repoTouched() repoCheck {
	return repoCheck{name: "the working tree changed", fn: func(t *repoTree) (bool, string) {
		if !t.changed() {
			return false, "empty diff -- the agent changed nothing, or the run captured no diff"
		}
		return true, fmt.Sprintf("%d diff lines", strings.Count(strings.TrimSpace(t.Diff), "\n")+1)
	}}
}

// rateLimiterOnRefreshPath asserts a limiter exists AND the refresh endpoint
// goes through it (directly, or via middleware registered beside its route).
func rateLimiterOnRefreshPath() repoCheck {
	return repoCheck{name: "rate limiter on the refresh path", gate: true, fn: func(t *repoTree) (bool, string) {
		limiterFiles := t.with(limiterTerms...)
		refreshFiles := t.with(refreshPathTerms...)
		if len(refreshFiles) == 0 {
			return false, "no file references the refresh endpoint any more"
		}
		var wired []repoFile
		for _, f := range refreshFiles {
			if _, ok := f.has(limiterTerms...); ok {
				wired = append(wired, f)
			}
		}
		switch {
		case len(wired) > 0:
			return true, fmt.Sprintf("%s (%s)", filePaths(wired), strings.Join(matchedIn(wired, limiterTerms...), ", "))
		case len(limiterFiles) > 0:
			return false, "limiter code in " + filePaths(limiterFiles) + " but nothing on the refresh path uses it"
		default:
			return false, "no rate-limiting code in the tree"
		}
	}}
}

// limiterUsesSharedStorage is the check the scenario exists for: the counter
// must not be a per-process map (memory rate-limit-not-in-memory).
func limiterUsesSharedStorage() repoCheck {
	return repoCheck{name: "limiter counter is not per-process", gate: true, fn: func(t *repoTree) (bool, string) {
		limiterFiles := t.with(limiterTerms...)
		if len(limiterFiles) == 0 {
			return false, "no rate-limiting code to inspect"
		}
		terms := append(append([]string{}, sharedBackendTerms...), sharedIntentTerms...)
		if hits := matchedIn(limiterFiles, terms...); len(hits) > 0 {
			return true, fmt.Sprintf("shared storage in %s (%s)", filePaths(limiterFiles), strings.Join(hits, ", "))
		}

		// The backing store can live in another file (or arrive as a new
		// dependency) with the limiter delegating to it -- but only when the
		// limiter is not itself holding the counter in a map.
		var elsewhere []repoFile
		for _, f := range t.Files {
			if _, ok := f.has(limiterTerms...); !ok {
				elsewhere = append(elsewhere, f)
			}
		}
		holdsMap := false
		for _, f := range limiterFiles {
			holdsMap = holdsMap || f.Maps
		}
		hits := matchedIn(elsewhere, sharedBackendTerms...)
		switch {
		case len(hits) > 0 && !holdsMap:
			return true, fmt.Sprintf("limiter delegates to a shared store (%s)", strings.Join(hits, ", "))
		case holdsMap:
			return false, "the counter is a per-process map in " + filePaths(limiterFiles) +
				" -- resets per instance (memory rate-limit-not-in-memory)"
		default:
			return false, "no shared or persistent storage behind the limiter in " + filePaths(limiterFiles)
		}
	}}
}

// authRefreshRubric is the LLM judge's instruction: only what the assertions
// cannot see. It deliberately says nothing about file layout or naming, which
// the repo checks already cover far more cheaply.
const authRefreshRubric = `The agent was told to continue where a previous session left off on a small Go web service.
The correct next step is the plan's step 5: rate-limiting POST /auth/refresh, per IP and per token family.
The service runs several instances behind a load balancer, so a limiter whose counter lives in a
per-process map is effectively no limiter at all -- the counter must live in storage shared across
instances (Redis or equivalent).

PASS when the transcript shows the agent:
- identified rate-limiting the refresh endpoint as the work to continue, rather than inventing
  unrelated work or redoing a step that was already finished;
- built a limiter whose counter is not process-local, and showed it understood why that matters;
- left the endpoint's existing behaviour (token rotation, reuse detection, cookie flags) intact.

FAIL when it shipped a per-process in-memory limiter with no sign it considered multi-instance
traffic, worked on the wrong thing, or claimed work the transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// authRefreshGrader wires the layers. Gating: the repo assertions (the outcome,
// comparable across every arm) plus the two event-log DEFECT checks (the
// mechanism never fired; a durable write escaped the project). The remaining
// event checks measure how the agent used Seamless without gating the verdict
// -- see grade.go for why that split is what keeps the uplift number honest.
var authRefreshGrader = &rubricGrader{
	scenario: authRefreshName,
	project:  authRefreshProject,
	repo: []repoCheck{
		repoTouched(),
		rateLimiterOnRefreshPath(),
		limiterUsesSharedStorage(),
	},
	events: []eventCheck{
		briefingInjected(),
		memoryConsulted(authRefreshMemory),
		planStepMoved(authRefreshPlan, authRefreshStep5),
		findingRecorded(),
		writesScopedToProject(authRefreshProject),
	},
	rubric: authRefreshRubric,
}
