// The deploy-drain scenario: the continue-work beat. The prompt names the
// workstream and the symptom -- "pick up the zero-downtime deploy work,
// we're still dropping requests" -- so every arm has a fair entry point; the
// seeded plan says the next step is graceful shutdown, and the memory
// lb-healthz-drain-window says what graceful means HERE: the load balancer
// polls /healthz every 5s and needs two consecutive failures to stop routing,
// so a process that just calls srv.Shutdown on SIGTERM still gets new
// connections routed into it while it dies. Fail healthz first, wait out the
// drain window, then shut down.
//
// The textbook fix (signal.NotifyContext + srv.Shutdown) is exactly what an
// arm without the memory writes -- and it is genuinely insufficient for this
// infrastructure, which is the honest contrast: the LB's poll cadence is not
// derivable from the repo. An arm that flips healthz unprompted (the
// readiness-drain idiom) passes on its own merits.
package bench

import (
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	deployDrainName = "deploy-drain"
	// deployDrainPlan is the seeded plan slug; its step tasks carry it.
	deployDrainPlan = "zero-downtime-deploys"
	// deployDrainMemory is the scenario's load-bearing memory: its description
	// alone (the line the briefing's memory index shows) carries the poll
	// cadence and the fail-first sequence. The seed writes it and the grader
	// checks the agent consulted it, from this one name.
	deployDrainMemory = "lb-healthz-drain-window"
	// deployDrainStep is the plan's sole claimable step -- the work the run is
	// graded on. Its wording names the goal, not the mechanism.
	deployDrainStep = "Shut down gracefully on SIGTERM without dropping routed requests"
	// deployDrainSession is the completed session the finding hangs off.
	deployDrainSession = "cc/5e77a1b9"
	// deployDrainFinding is that session's ~18h-old summary. It confirms the
	// symptom and locates the work WITHOUT the drain constraint: the LB's
	// behaviour must reach the agent through the memory or not at all.
	deployDrainFinding = "Step 1 done: captured the LB-side errors through two deploys -- the connection resets cluster in the seconds around the instance swap, so the old process is exiting with requests still arriving. Next is step 2, graceful shutdown; main.go still calls ListenAndServe directly and nothing handles SIGTERM today."
)

// deployDrain is briefing-surfaced (finding + plan + memory index land at
// SessionStart), so it runs headless via plain `claude -p`.
var deployDrain = Scenario{
	Name:   deployDrainName,
	Prompt: "Pick up the zero-downtime deploy work again -- we're still dropping requests every time we ship.",
	Seed:   seedDeployDrain,
	Grader: deployDrainGrader,
}

func seedDeployDrain(s *demokit.Seeder, repoPath string) error {
	if err := seedProject(s, repoPath); err != nil {
		return err
	}
	sess, err := seedFinishedSession(s, deployDrainSession, "5e77a1b9-30dc-4f76", deployDrainFinding, 18, 50)
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
	// The load-bearing memory, on top of the common noise.
	mems = append(mems, seedMemory{core.KindGotcha, deployDrainMemory,
		"The LB polls /healthz every 5s and stops routing only after 2 consecutive failures: on SIGTERM fail healthz FIRST, wait out the >=12s drain window, then Shutdown.",
		"Graceful shutdown alone does not stop the load balancer from routing NEW connections into a dying process. The LB health-checks /healthz every 5 seconds and needs two consecutive failures before it takes an instance out of rotation, so for up to ~10s after SIGTERM it keeps sending fresh requests -- srv.Shutdown refuses them and the client sees a reset.\n\nWe shipped plain signal-plus-Shutdown once; the deploy-time resets did not move at all.\n\nThe sequence that works here: on SIGTERM (1) flip /healthz to 503 immediately, (2) keep serving normally while the LB notices -- wait at least 12s, (3) then srv.Shutdown with a deadline to drain what is in flight. The health endpoint failing first IS the drain mechanism.", 5})
	if err := seedMemories(s, sess.Name, mems); err != nil {
		return err
	}
	if err := seedPlanSteps(s, deployDrainPlan, sess.Name, []seedStep{
		{"Capture the LB-side errors during a deploy and confirm the resets", true, -1, 3, 8},
		{deployDrainStep, false, 0, 1, 0},
		{"Rely on the drain window in the deploy flow and drop the sleep hack", false, 1, 1, 0},
	}); err != nil {
		return err
	}
	// One failed trial: the edge-retry dead end, already tried and closed.
	return seedTrials(s, sess, []seedTrial{{
		lab:      "deploy-drops",
		title:    "Retry failed requests at the load balancer during deploys",
		changes:  "Enabled the LB's retry-on-connection-failure policy for the deploy window.",
		expected: "Clients stop seeing errors during instance swaps.",
		actual:   "GET errors disappeared, but the retries duplicated POST /auth/refresh -- a duplicated rotation trips the replay detector and revokes the token family, so users got logged out mid-deploy. Edge retries are off the table for anything that touches auth.",
		outcome:  core.OutcomeFail,
		agoHours: 40,
	}})
}
