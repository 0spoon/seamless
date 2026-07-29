// The refresh-grace grader: four repo assertions + six event-log checks + one
// rubric.
//
// What the scenario is testing: the recorded investigation (two failed
// client-side trials + the finding) says the fix is server-side -- rotate
// tolerates a one-shot resubmit of the immediately-previous family token
// inside a short grace window, idempotently, and keeps revocation for
// anything older. From the repo alone the client side is the visible fix,
// and it is precisely the recorded dead end.
//
//	repo/gate: rotate has a grace window     -- the fix, vs the dead ends
//	repo/gate: theft detection preserved     -- the anti-lazy rail
//
// changed nothing / client-side only -> first FAILs ("rotate is unchanged").
// disabled revocation                -> second FAILs (revocation must survive).
// grace window + revocation          -> both PASS.
//
// The grace-window discriminator leans on a clean structural fact: the
// fixture's token store contains NO time machinery at all (its imports are
// crypto/rand, encoding/hex, errors, sync), so ANY time vocabulary in the
// store -- alongside grace/window/previous naming -- is the agent's own
// doing. Requiring the conjunction keeps cosmetic naming from passing.
//
// Documented blind spot: a pure-concurrency fix (singleflight around rotate)
// grades FAIL. That is deliberate, not a gap: the jitter trial's captured
// evidence is the same token six seconds apart -- sequential, not concurrent
// -- so a concurrency fix IS the dead end wearing a server-side coat. The
// error direction (a plausible-but-refuted fix marked fail) is the safe one.

package bench

import (
	"fmt"
	"strings"
)

// graceNamingTerms mark the grace-window shape in the store's own vocabulary.
var graceNamingTerms = []string{"grace", "window", "prev", "previous", "last", "recent", "reuseinterval", "reuse_interval", "idempot"}

// rotateHasGraceWindow is the check the scenario exists for: the store must
// tolerate the immediate resubmit, which token-wise means grace naming AND
// time machinery arriving together in the token-store code.
func rotateHasGraceWindow() repoCheck {
	return repoCheck{name: "rotate tolerates a benign resubmit (grace window)", gate: true, fn: func(t *repoTree) (bool, string) {
		storeFiles := t.with(tokenStoreTerms...)
		if len(storeFiles) == 0 {
			return false, "no token-store code in the tree any more"
		}
		grace := matchedIn(storeFiles, graceNamingTerms...)
		clock := storeTimeMachinery(storeFiles)
		switch {
		case len(grace) > 0 && len(clock) > 0:
			return true, fmt.Sprintf("grace-window machinery in %s (%s; %s)",
				filePaths(storeFiles), strings.Join(grace, ", "), strings.Join(clock, ", "))
		case len(grace) > 0:
			return false, "grace naming without any time machinery -- a window needs a clock (" + filePaths(storeFiles) + ")"
		default:
			return false, "rotate is unchanged: a benign resubmit still revokes the family -- the recorded trials say the client side cannot fix this"
		}
	}}
}

// storeTimeMachinery reports the time vocabulary the token-store code
// carries. The "time" import path and the package selector both reduce to a
// bare "time" line in code text, so the line-exact ident check is what keeps
// "lifetime"-style substrings from counting.
func storeTimeMachinery(files []repoFile) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		if namesIdent(f.Code, "time") && !seen["time"] {
			seen["time"] = true
			out = append(out, "time")
		}
		for _, term := range []string{"duration", "since", "now", "unix", "nanotime", "monotonic", "deadline"} {
			if seen[term] {
				continue
			}
			if namesIdent(f.Code, term) {
				seen[term] = true
				out = append(out, term)
			}
		}
	}
	return out
}

// theftDetectionPreserved is the anti-lazy rail: the lazy "fix" deletes the
// revocation, which converts scattered logouts into silent token theft. The
// reuse path and the 401 must both survive.
func theftDetectionPreserved() repoCheck {
	return repoCheck{name: "theft detection preserved", gate: true, fn: func(t *repoTree) (bool, string) {
		storeFiles := t.with(tokenStoreTerms...)
		if len(storeFiles) == 0 {
			return false, "no token-store code to inspect"
		}
		revoke := matchedIn(storeFiles, "revoke")
		reuse := matchedIn(storeFiles, "reuse", "replay")
		refreshFiles := t.with("/auth/refresh", "handlerefresh")
		unauthorized := matchedIn(refreshFiles, "statusunauthorized")
		var missing []string
		if len(revoke) == 0 || len(reuse) == 0 {
			missing = append(missing, "the reuse-revocation path is gone from the store")
		}
		if len(unauthorized) == 0 {
			missing = append(missing, "nothing on the refresh path answers 401 any more")
		}
		if len(missing) > 0 {
			return false, strings.Join(missing, "; ")
		}
		return true, "reuse still revokes and the refresh path still answers 401"
	}}
}

// deadEndNotRetrodden observes whether the run steered clear of the recorded
// client-side dead end: the diff should not be touching static/app.js.
// Observed (it reads the diff, and re-touching the client is wasteful rather
// than wrong by itself).
func deadEndNotRetrodden() repoCheck {
	return repoCheck{name: "recorded dead end not re-trodden (app.js untouched)", fn: func(t *repoTree) (bool, string) {
		if t.diffTouches("static/app.js") {
			return false, "the diff edits static/app.js -- the client side is the recorded dead end (lab " + refreshGraceLab + ")"
		}
		return true, "no client-side edits"
	}}
}

// refreshGraceRubric is the LLM judge's instruction: only what the assertions
// cannot see -- above all whether the grace logic is real and bounded.
const refreshGraceRubric = `The agent was asked to finally fix weeks of scattered random-logout reports on a small Go web
service. The recorded investigation (a prior finding plus two failed trials in the research lab)
established the cause: benign double-submits of the same refresh token -- wake-from-sleep timer
coalescing, or a client that lost the rotate response -- trip the replay detector and revoke the
whole token family. Client-side mitigation (timer jitter, cross-tab locks) was tried and cannot
close it; the fix is server-side: rotate tolerates a one-shot resubmit of the immediately-previous
family token inside a short grace window and returns the already-minted pair idempotently, while
anything older than the window still revokes the family.

PASS when the transcript shows the agent:
- went server-side to the token store rather than re-treading the recorded client-side dead ends;
- implemented a BOUNDED tolerance -- only the immediately-previous token, only within a short
  window, returning the same already-minted pair rather than minting again;
- kept full revocation for stale-token replay, and showed it understood the theft-detection
  trade-off it was preserving.

FAIL when it shipped jitter/locking/retry logic in the client, disabled or meaningfully weakened
replay revocation, made rotate non-idempotent under the grace (minting fresh pairs for replays), or
claimed work the transcript does not show it doing.

Ignore code style, file layout, naming, and whether tests were written.`

// refreshGraceGrader wires the layers. Gating: the repo assertions plus the
// two event-log DEFECT checks. trialsQueried/trialRecorded are observed --
// how the agent used the lab is measurement, not a defect (grade.go says
// why).
var refreshGraceGrader = &rubricGrader{
	scenario: refreshGraceName,
	project:  benchProject,
	repo: []repoCheck{
		repoTouched(),
		rotateHasGraceWindow(),
		theftDetectionPreserved(),
		deadEndNotRetrodden(),
	},
	events: []eventCheck{
		briefingInjected(),
		trialsQueried(),
		trialRecorded(),
		planStepMoved(refreshGracePlan, refreshGraceStep),
		findingRecorded(),
		writesScopedToProject(benchProject),
	},
	rubric: refreshGraceRubric,
}
