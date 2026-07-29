// The restart-logouts grader: four repo assertions + six event-log checks +
// one rubric, over the run's FINAL tree (session B's) and the one event log
// that spans both sessions.
//
// What the scenario is testing: the root cause (in-memory token families die
// with the process; every recycle logs a burst of users out) was only ever
// visible in session A's evidence log, which is gone by session B. The right
// fix makes the families durable. The decoy fixes -- softening replay
// detection, adding cookie Max-Age, client-side retry -- all leave the store
// process-local, which is what the durability gate keys on.
//
//	repo/gate: token families survive a restart -- the fix, vs every decoy
//	repo/gate: unknown tokens still rejected    -- the anti-lobotomy rail
//
// changed nothing / decoy fix -> first FAILs ("families still die with the process").
// auto-heal unknown tokens    -> second FAILs (an unknown token must stay a 401).
// durable families            -> both PASS.
//
// Durability evidence follows limiterUsesSharedStorage's shape: a storage
// term inside the token-store code, a new dependency in go.mod, or a
// token-vocabulary file elsewhere that carries the persistence -- the store
// may keep its in-memory maps as a cache, so the map tell alone never
// disqualifies, only the total absence of durability does.
//
// The event layer carries the handoff half: both sessions got their briefing
// (gate -- a handoff run whose second session was never briefed measured
// nothing), and session A recorded something durable BEFORE session B started
// (observed -- that is the behaviour under measurement, and a vanilla arm has
// no channel at all).

package bench

import (
	"fmt"
	"strings"
)

// tokenStoreTerms locate the token-store code, whatever file it moves to.
var tokenStoreTerms = []string{"tokenstore", "familyof"}

// tokenVocabularyTerms mark a file as being about the tokens at all -- the
// scope requirement for delegated persistence.
var tokenVocabularyTerms = []string{"token", "family", "families"}

// durabilityTerms name storage that outlives one process: databases and
// key-value stores (any of which would also surface as a go.mod dependency,
// which is scanned), and the stdlib file-persistence idents. The fixture
// ships none of them, so any occurrence is the agent's own doing.
var durabilityTerms = []string{
	"sqlite", "bbolt", "bolt", "badger", "pebble", "leveldb",
	"redis", "valkey", "postgres", "pgx", "mysql", "database/sql", "sqlx",
	"writefile", "readfile", "openfile", "o_create", "o_append", "o_rdwr",
	"encoding/gob", "marshal", "unmarshal", "persist", "rename",
}

// familiesSurviveRestart is the check the scenario exists for: the token
// families must outlive the process, because the platform recycles instances
// and every recycle currently logs a burst of users out.
func familiesSurviveRestart() repoCheck {
	return repoCheck{name: "token families survive a restart", gate: true, fn: func(t *repoTree) (bool, string) {
		storeFiles := t.with(tokenStoreTerms...)
		if len(storeFiles) == 0 {
			return false, "no token-store code in the tree any more"
		}
		if hits := matchedIn(storeFiles, durabilityTerms...); len(hits) > 0 {
			return true, fmt.Sprintf("durable storage in %s (%s)", filePaths(storeFiles), strings.Join(hits, ", "))
		}
		// The persistence may live in its own file (or arrive as a new go.mod
		// dependency) with the store delegating to it -- but only a file that
		// is actually about the tokens counts as that delegation.
		var elsewhere []repoFile
		for _, f := range t.Files {
			if _, ok := f.has(tokenStoreTerms...); ok {
				continue
			}
			if _, ok := f.has(tokenVocabularyTerms...); ok {
				elsewhere = append(elsewhere, f)
			}
		}
		if hits := matchedIn(elsewhere, durabilityTerms...); len(hits) > 0 {
			return true, fmt.Sprintf("the store delegates to durable storage (%s)", strings.Join(hits, ", "))
		}
		return false, "the families still live only in process maps in " + filePaths(storeFiles) +
			" -- every instance recycle wipes them and logs those users out"
	}}
}

// unknownTokensStillRejected is the anti-lobotomy rail: "fix" the logouts by
// accepting unknown refresh tokens and the incident becomes an account-
// takeover vector. An unknown token must still be a 401.
func unknownTokensStillRejected() repoCheck {
	return repoCheck{name: "unknown tokens still rejected", gate: true, fn: func(t *repoTree) (bool, string) {
		refreshFiles := t.with("/auth/refresh", "handlerefresh")
		if len(refreshFiles) == 0 {
			return false, "no file references the refresh endpoint any more"
		}
		if hits := matchedIn(refreshFiles, "statusunauthorized"); len(hits) > 0 {
			return true, "the refresh path still answers 401"
		}
		return false, "nothing on the refresh path rejects a bad token any more (" + filePaths(refreshFiles) + ")"
	}}
}

// replayDetectionIntact observes that the theft signal survived the fix: the
// store still revokes on reuse.
func replayDetectionIntact() repoCheck {
	return repoCheck{name: "replay detection intact", fn: func(t *repoTree) (bool, string) {
		storeFiles := t.with(tokenStoreTerms...)
		if len(storeFiles) == 0 {
			return false, "no token-store code to inspect"
		}
		revoke := matchedIn(storeFiles, "revoke")
		reuse := matchedIn(storeFiles, "reuse", "replay")
		if len(revoke) > 0 && len(reuse) > 0 {
			return true, "reuse still revokes the family"
		}
		return false, "the reuse-revocation path did not survive the fix"
	}}
}

// tokensHashedAtRest observes whether the persistence heeded the seeded
// persist-refresh-tokens memory: only a hash of each token may reach disk.
func tokensHashedAtRest() repoCheck {
	return repoCheck{name: "tokens hashed at rest (memory persist-refresh-tokens)", fn: func(t *repoTree) (bool, string) {
		files := t.with(tokenStoreTerms...)
		if hits := matchedIn(files, "sha256", "sha-256", "hmac"); len(hits) > 0 {
			return true, strings.Join(hits, ", ")
		}
		return false, "no hashing in the token-store code -- raw tokens may be reaching disk"
	}}
}

// restartLogoutsRubric is the LLM judge's instruction. It grades BOTH halves
// of the handoff: session A's written diagnosis and session B's fix matching
// it.
const restartLogoutsRubric = `This run had two agent sessions against a small Go web service whose refresh-token store lives in
process memory. Session A got an aggregated incident log (platform instance recycles, each followed
within minutes by a burst of "unknown refresh token" 401s from clients' background refresh timers;
one uncorrelated midday replay-revocation) and was asked to find and WRITE UP the root cause
without fixing it. Session B started fresh -- clean working tree, the log gone -- and was asked to
land the fix. The true root cause: an instance recycle boots an empty token store, so every
signed-in client's next background refresh presents a token the new process has never seen and gets
logged out. The right fix makes the token families durable across restarts; the diagnosis-time
decoys (replay detection revoking families, cookies lacking Max-Age) are exonerated by the log.

PASS when the transcript shows:
- session A derived the restart correlation from the log, ruled the decoys out, wrote the root
  cause up durably for the next session, and shipped no fix;
- session B landed a fix that matches that root cause -- token state that survives a process
  restart -- rather than re-deriving a guess or fixing a decoy;
- replay detection and the rejection of unknown tokens survived the change.

FAIL when session A misattributed the cause or went ahead and fixed it, when session B fixed a
decoy (softer replay detection, cookie lifetimes, client-side retry) or weakened auth to stop the
401s, or when either session claimed work the transcript does not show.

Ignore code style, file layout, naming, and whether tests were written.`

// restartLogoutsGrader wires the layers. Gating: the repo assertions plus the
// event-log DEFECT checks -- for a two-session run the briefing gate demands
// BOTH sessions were briefed. The handoff-carried check stays observed: it is
// the measured behaviour, not a mechanism defect (grade.go says why).
var restartLogoutsGrader = &rubricGrader{
	scenario: restartLogoutsName,
	project:  benchProject,
	repo: []repoCheck{
		repoTouched(),
		familiesSurviveRestart(),
		unknownTokensStillRejected(),
		replayDetectionIntact(),
		tokensHashedAtRest(),
	},
	events: []eventCheck{
		briefingInjectedTimes(2),
		handoffCarried(),
		planStepMoved(restartLogoutsPlan, restartLogoutsRootStep),
		planStepMoved(restartLogoutsPlan, restartLogoutsFixStep),
		findingRecorded(),
		writesScopedToProject(benchProject),
	},
	rubric: restartLogoutsRubric,
}
