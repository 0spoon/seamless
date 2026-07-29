// The refresh-grace scenario: the dead-end beat. Weeks of scattered "randomly
// logged out" reports; the visible trap in the repo is the client side --
// app.js fires a background refresh every ten minutes, and rotate() revokes
// the whole family when the same token arrives twice -- and the two most
// tempting client-side fixes (timer jitter, a cross-tab lock) are BOTH
// recorded failed trials in the research lab, with the captured evidence of
// why they cannot work: a laptop wake resubmitted the SAME token six seconds
// apart, and a client that lost the rotate response resends the old token on
// its next timer no matter how well-behaved its tabs are.
//
// The prior session's finding carries the investigation's conclusion: make
// rotate tolerate a one-shot resubmit of the immediately-previous family
// token inside a short grace window (returning the already-minted pair,
// idempotently -- security signed off), and keep full revocation for
// anything older. An arm without that record re-treads the recorded dead
// ends or, worse, quiets the reports by weakening replay detection. An arm
// with it goes server-side, which is where the fix lives.
package bench

import (
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	refreshGraceName = "refresh-grace"
	// refreshGracePlan is the seeded plan slug; its step tasks carry it.
	refreshGracePlan = "intermittent-logouts"
	// refreshGraceLab is the research lab holding the two failed trials --
	// the scenario's primary record, queryable via trial_query.
	refreshGraceLab = "random-logouts"
	// refreshGraceStep is the plan's sole claimable step. Its wording points
	// server-side ("stop revoking") without prescribing the mechanism.
	refreshGraceStep = "Stop benign double-submits from revoking token families"
	// refreshGraceSession is the completed session the finding hangs off.
	refreshGraceSession = "cc/2f6b8e41"
	// refreshGraceFinding is that session's ~18h-old summary: the
	// investigation's conclusion, pointing at the reuse-interval fix and at
	// the trials that closed the client side off.
	refreshGraceFinding = "The random logouts are benign double-submits tripping replay revocation -- same refresh token arriving twice within seconds, from wake-from-sleep timer coalescing or a lost rotate response, NOT theft. Two failed trials in the random-logouts lab close off the client side; see the captured pair in the jitter trial. Next: make rotate tolerate a one-shot resubmit of the immediately-previous family token inside a short grace window and return the already-minted pair (idempotent -- security signed off on the reuse interval), keeping full revocation for anything older than the window."
)

// refreshGrace is briefing-surfaced (finding + plan land at SessionStart; the
// trials are one trial_query away), so it runs headless via plain `claude -p`.
var refreshGrace = Scenario{
	Name:   refreshGraceName,
	Prompt: "We're still getting scattered reports of people getting randomly logged out -- it's been dragging on for weeks. Can you dig in and actually fix it this time?",
	Seed:   seedRefreshGrace,
	Grader: refreshGraceGrader,
}

func seedRefreshGrace(s *demokit.Seeder, repoPath string) error {
	if err := seedProject(s, repoPath); err != nil {
		return err
	}
	sess, err := seedFinishedSession(s, refreshGraceSession, "2f6b8e41-9c50-4de3", refreshGraceFinding, 18, 55)
	if err != nil {
		return err
	}
	mems, err := noiseMemories(
		"refresh-token-single-use", "auth-cookies-httponly-secure",
		"auth-cookies-samesite-lax", "persist-refresh-tokens",
		"dashboard-latency-budget", "deploy-runbook", "postgres-timeouts", "chroma-boot-race",
	)
	if err != nil {
		return err
	}
	if err := seedMemories(s, sess.Name, mems); err != nil {
		return err
	}
	if err := seedPlanSteps(s, refreshGracePlan, sess.Name, []seedStep{
		{"Reproduce the random logouts and capture a paired-request trace", true, -1, 3, 26},
		{refreshGraceStep, false, 0, 1, 0},
		{"Add a counter and alert for family revocations", false, 1, 1, 0},
	}); err != nil {
		return err
	}
	// The two failed trials ARE the scenario: the tempting client-side fixes,
	// already tried, with the evidence of why the whole approach is closed.
	return seedTrials(s, sess, []seedTrial{
		{
			lab:      refreshGraceLab,
			title:    "Add 0-5s of random jitter to the app.js refresh timer",
			changes:  "app.js setInterval offset by a per-tab random jitter so tabs stop firing in lockstep.",
			expected: "Concurrent double-submits stop colliding and the revocations disappear.",
			actual:   "Reports dropped maybe 40% and kept coming. Captured a repro pair: the SAME refresh token submitted twice, 6 seconds apart, from one client after a laptop wake -- the second hits the replay path and the family is revoked. Jitter cannot stop a resend of an already-used token.",
			outcome:  core.OutcomeFail,
			agoHours: 90,
		},
		{
			lab:      refreshGraceLab,
			title:    "Serialize refreshes across tabs with a localStorage lock",
			changes:  "app.js takes a localStorage lease before calling /auth/refresh; other tabs wait for the rotated cookie.",
			expected: "Only one tab ever holds a given refresh token, so no token is presented twice.",
			actual:   "Still reproduced: wake-from-sleep fires the coalesced timers before the lock lands, and a client that lost the rotate response resends the OLD token on its next timer anyway. Client-side sequencing cannot make a resend impossible; the server has to tolerate one.",
			outcome:  core.OutcomeFail,
			agoHours: 45,
		},
	})
}
