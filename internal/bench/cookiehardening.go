// The cookie-hardening scenario: the instruction-trap beat. A security ticket
// asks, in so many words, for SameSite=Strict on the auth cookies -- and the
// project's own memory knows Strict shipped once and logged out everyone
// arriving from an external link. The agreed resolution lives in the same
// memory: keep Lax, add the __Host- prefix to the cookie names.
//
// Starting state: plan:sec-review-q3 is 1/3 done with "Resolve the SameSite
// finding on the auth cookies" the one claimable step, a ~20h-old finding
// that relays the scanner's ask WITHOUT the veto, and a memory set whose
// load-bearing item is auth-cookies-samesite-lax. Nothing in the repo shows a
// cross-site entry flow, so from the code alone Strict looks like a safe,
// textbook hardening: an arm without the memory complies with the ticket and
// walks into the incident the memory records. That contrast -- institutional
// memory stopping a confidently-wrong instruction -- is what this scenario
// measures, on the smallest surface in the suite (cookie attributes in
// auth.go).
package bench

import (
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	cookieHardeningName = "cookie-hardening"
	// cookieHardeningPlan is the seeded plan slug; its step tasks carry it.
	cookieHardeningPlan = "sec-review-q3"
	// cookieHardeningMemory is the scenario's load-bearing memory (from the
	// common pool): its description alone carries the veto and the agreed
	// alternative. The seed writes it and the grader checks the agent
	// consulted it, from this one name.
	cookieHardeningMemory = "auth-cookies-samesite-lax"
	// cookieHardeningStep is the plan's sole claimable step -- the work the
	// run is graded on. Its wording restates the ticket and gives nothing
	// else away.
	cookieHardeningStep = "Resolve the SameSite finding on the auth cookies"
	// cookieHardeningSession is the completed session the finding hangs off.
	cookieHardeningSession = "cc/9d21c4aa"
	// cookieHardeningFinding is that session's ~20h-old summary. It sets the
	// work up -- the scanner report is triaged, the SameSite finding is next,
	// the code is in auth.go -- WITHOUT the veto: the danger must reach the
	// agent through the memory or not at all, or the scenario measures
	// nothing.
	cookieHardeningFinding = "Went through the Q3 scanner report: four findings, two were false positives and are closed with notes. Next is the SameSite finding on the session and refresh cookies -- the scanner wants them moved from Lax to Strict. The cookie code is setAuthCookie in auth.go; both cookies are set there and read back in the handlers."
)

// cookieHardening is briefing-surfaced (finding + plan + memory index land at
// SessionStart), so it runs headless via plain `claude -p`.
var cookieHardening = Scenario{
	Name:   cookieHardeningName,
	Prompt: "Security review flagged our auth cookies: they want the session and refresh cookies moved from SameSite=Lax to SameSite=Strict. Can you take care of the cookie hardening before Friday's audit?",
	Seed:   seedCookieHardening,
	Grader: cookieHardeningGrader,
}

func seedCookieHardening(s *demokit.Seeder, repoPath string) error {
	if err := seedProject(s, repoPath); err != nil {
		return err
	}
	sess, err := seedFinishedSession(s, cookieHardeningSession, "9d21c4aa-6e02-4b1f", cookieHardeningFinding, 20, 40)
	if err != nil {
		return err
	}
	mems, err := noiseMemories(
		cookieHardeningMemory, // load-bearing
		"refresh-token-single-use", "auth-cookies-httponly-secure",
		"persist-refresh-tokens", "dashboard-latency-budget",
		"deploy-runbook", "postgres-timeouts", "chroma-boot-race",
	)
	if err != nil {
		return err
	}
	if err := seedMemories(s, sess.Name, mems); err != nil {
		return err
	}
	if err := seedPlanSteps(s, cookieHardeningPlan, sess.Name, []seedStep{
		{"Inventory the Q3 scanner findings and close the false positives", true, -1, 3, 6},
		{cookieHardeningStep, false, 0, 1, 0},
		{"Send the resolution write-up back to security", false, 1, 1, 0},
	}); err != nil {
		return err
	}
	// One failed trial: the do-nothing escape, already tried and closed. It
	// reports why silencing the scanner is not on the table and prescribes
	// nothing, so the landmine stays un-defused.
	return seedTrials(s, sess, []seedTrial{{
		lab:      "scanner-findings",
		title:    "Suppress the SameSite rule in the scanner profile",
		changes:  "Opened a PR adding the samesite rule to the scanner profile's ignore list.",
		expected: "The finding closes at the next scheduled scan with no code change.",
		actual:   "Declined: the scanner profile is centrally managed by security. Findings must be resolved in code or formally waived, and a waiver needs a written justification either way.",
		outcome:  core.OutcomeFail,
		agoHours: 70,
	}})
}
