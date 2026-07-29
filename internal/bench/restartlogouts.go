// The restart-logouts scenario: the handoff beat, and the only two-session
// scenario in the suite -- the one shape that tests the WRITE half of the
// memory loop rather than consumption of pre-seeded memories.
//
// Session A gets an aggregated incident log dropped into the repo (runner
// evidence, untracked, removed after the session) and is asked to find the
// root cause and write it up -- explicitly not to fix anything. The log shows
// two platform instance recycles, each followed within minutes by a burst of
// "unknown refresh token" 401s: the token store lives in process memory, a
// replacement instance boots empty, and every open tab's background refresh
// timer walks into it. One uncorrelated midday replay-revocation line
// exonerates the tempting decoy (replay detection), and nothing correlates
// with browser restarts, which exonerates the other (session-lifetime
// cookies).
//
// Session B runs in a FRESH working tree with the log gone -- the evidence
// has evaporated, exactly like a real "the writeup went to a Slack thread"
// morning -- and is asked to land the fix. What session A recorded in
// Seamless (a finding at session_end, a memory) is the only channel across
// the boundary: a Seamless-ful arm's session B inherits the diagnosis in its
// briefing; the vanilla arm's session B faces three code-visible candidate
// causes blind. The right fix makes token families survive a restart
// (persistence), and the seeded persist-refresh-tokens memory shapes HOW
// without revealing the root cause.
package bench

import (
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	restartLogoutsName = "restart-logouts"
	// restartLogoutsPlan is the seeded plan slug; its step tasks carry it.
	restartLogoutsPlan = "logout-incident"
	// restartLogoutsRootStep is the claimable step session A works;
	// restartLogoutsFixStep unblocks behind it for session B.
	restartLogoutsRootStep = "Find the root cause of the morning logout bursts"
	restartLogoutsFixStep  = "Land the fix for the logout bursts"
	// restartLogoutsSession is the completed session the (deliberately thin)
	// prior finding hangs off: triage happened, the log was requested.
	restartLogoutsSession = "cc/8b3d92f0"
	// restartLogoutsFinding sets up the investigation without diagnosing
	// anything: the diagnosis is session A's job, from the log.
	restartLogoutsFinding = "Support tagged 61 logged-out tickets this month; they arrive in bursts, not a steady trickle, and the reporters were mid-session, not returning after days away. Asked infra for an aggregated app log with the platform events interleaved so the bursts can be correlated against what the platform was doing."
	// restartLogoutsLogPath is where session A's evidence materializes; the
	// prompt points the agent at it.
	restartLogoutsLogPath = "logs/app.log"
)

// restartLogouts runs as two headless sessions against one arm: the daemon
// (and its data dir) persists across the boundary, the working tree and the
// evidence do not.
var restartLogouts = Scenario{
	Name: restartLogoutsName,
	Steps: []Step{
		{
			Name:     "investigate",
			Prompt:   "We got a spike of 'I was logged out' complaints this morning, and it's not the first time this has happened. I dropped the aggregated log at logs/app.log. Figure out what actually happened and write the root cause up so whoever picks this up tomorrow has it -- don't ship a fix yet.",
			Evidence: map[string]string{restartLogoutsLogPath: restartLogoutsLog},
		},
		{
			Name:      "fix",
			Prompt:    "Picking up from yesterday: the mass-logout investigation is done. Land the fix for it.",
			FreshRepo: true,
		},
	},
	Seed:   seedRestartLogouts,
	Grader: restartLogoutsGrader,
}

func seedRestartLogouts(s *demokit.Seeder, repoPath string) error {
	if err := seedProject(s, repoPath); err != nil {
		return err
	}
	sess, err := seedFinishedSession(s, restartLogoutsSession, "8b3d92f0-25aa-4c17", restartLogoutsFinding, 44, 30)
	if err != nil {
		return err
	}
	// persist-refresh-tokens shapes the eventual fix (hash at rest) without
	// revealing the root cause; the rest is the common noise. No memory names
	// restarts, and no trial exists -- the investigation is session A's.
	mems, err := noiseMemories(
		"persist-refresh-tokens",
		"refresh-token-single-use", "auth-cookies-httponly-secure",
		"auth-cookies-samesite-lax", "dashboard-latency-budget",
		"deploy-runbook", "postgres-timeouts", "chroma-boot-race",
	)
	if err != nil {
		return err
	}
	if err := seedMemories(s, sess.Name, mems); err != nil {
		return err
	}
	return seedPlanSteps(s, restartLogoutsPlan, sess.Name, []seedStep{
		{"Triage the logged-out tickets and pull the aggregated app log", true, -1, 2, 5},
		{restartLogoutsRootStep, false, 0, 1, 0},
		{restartLogoutsFixStep, false, 1, 1, 0},
	})
}

// restartLogoutsLog is session A's evidence: an aggregated morning window
// with platform events interleaved. The signal is the correlation -- each
// instance recycle is followed within minutes by a burst of "unknown refresh
// token" 401s as every open tab's 10-minute background refresh timer fires
// into a freshly-booted (empty) token store. The lone 11:19 replay-revocation
// line is deliberate: replay detection fires all day at background rates and
// does NOT correlate with the bursts, so the tempting decoy is exonerated by
// the agent's own evidence. Line shapes match the fixture's real log calls
// (main.go's startup line, logRequests, auth.go's failure lines).
const restartLogoutsLog = `# aggregated myapp log -- this morning, all instances, platform events interleaved
06:04:11 app[i-04f2] GET / 200
06:07:38 app[i-04f2] POST /auth/refresh 200
06:08:02 app[i-04f2] GET /static/style.css 200
06:09:55 app[i-04f2] POST /auth/refresh 200
06:10:02 platform: instance i-04f2 terminated (scheduled recycle); replacement i-09aa launching
06:10:07 app[i-09aa] myapp 1.4.2 listening on :8080
06:10:19 app[i-09aa] GET / 200
06:10:31 app[i-09aa] auth: refresh failed: unknown refresh token
06:10:31 app[i-09aa] POST /auth/refresh 401
06:11:04 app[i-09aa] auth: refresh failed: unknown refresh token
06:11:04 app[i-09aa] POST /auth/refresh 401
06:11:47 app[i-09aa] GET / 200
06:12:12 app[i-09aa] auth: refresh failed: unknown refresh token
06:12:12 app[i-09aa] POST /auth/refresh 401
06:12:58 app[i-09aa] auth: refresh failed: unknown refresh token
06:12:58 app[i-09aa] POST /auth/refresh 401
06:13:22 app[i-09aa] GET /static/app.js 200
06:14:05 app[i-09aa] auth: refresh failed: unknown refresh token
06:14:05 app[i-09aa] POST /auth/refresh 401
06:15:33 app[i-09aa] auth: refresh failed: unknown refresh token
06:15:33 app[i-09aa] POST /auth/refresh 401
06:16:20 app[i-09aa] GET / 200
06:17:09 app[i-09aa] auth: refresh failed: unknown refresh token
06:17:09 app[i-09aa] POST /auth/refresh 401
06:18:44 app[i-09aa] auth: refresh failed: unknown refresh token
06:18:44 app[i-09aa] POST /auth/refresh 401
06:19:51 app[i-09aa] auth: refresh failed: unknown refresh token
06:19:51 app[i-09aa] POST /auth/refresh 401
06:21:07 app[i-09aa] POST /auth/refresh 200
06:24:30 app[i-09aa] GET / 200
06:31:12 app[i-09aa] POST /auth/refresh 200
06:48:56 app[i-09aa] GET / 200
07:15:40 app[i-09aa] POST /auth/refresh 200
08:02:19 app[i-09aa] GET / 200
08:41:33 app[i-09aa] POST /auth/refresh 200
09:27:48 app[i-09aa] GET /static/style.css 200
10:14:22 app[i-09aa] POST /auth/refresh 200
11:19:06 app[i-09aa] auth: refresh failed: refresh token reuse detected; family revoked
11:19:06 app[i-09aa] POST /auth/refresh 401
11:20:11 app[i-09aa] GET / 200
11:58:47 app[i-09aa] POST /auth/refresh 200
12:36:02 app[i-09aa] GET / 200
13:22:51 app[i-09aa] POST /auth/refresh 200
14:03:18 platform: instance i-09aa terminated (scheduled recycle); replacement i-11c7 launching
14:03:24 app[i-11c7] myapp 1.4.2 listening on :8080
14:03:41 app[i-11c7] GET / 200
14:04:02 app[i-11c7] auth: refresh failed: unknown refresh token
14:04:02 app[i-11c7] POST /auth/refresh 401
14:04:55 app[i-11c7] auth: refresh failed: unknown refresh token
14:04:55 app[i-11c7] POST /auth/refresh 401
14:05:47 app[i-11c7] GET /static/app.js 200
14:06:31 app[i-11c7] auth: refresh failed: unknown refresh token
14:06:31 app[i-11c7] POST /auth/refresh 401
14:08:09 app[i-11c7] auth: refresh failed: unknown refresh token
14:08:09 app[i-11c7] POST /auth/refresh 401
14:09:26 app[i-11c7] auth: refresh failed: unknown refresh token
14:09:26 app[i-11c7] POST /auth/refresh 401
14:11:13 app[i-11c7] auth: refresh failed: unknown refresh token
14:11:13 app[i-11c7] POST /auth/refresh 401
14:12:40 app[i-11c7] GET / 200
14:13:29 app[i-11c7] auth: refresh failed: unknown refresh token
14:13:29 app[i-11c7] POST /auth/refresh 401
14:15:02 app[i-11c7] POST /auth/refresh 200
14:22:18 app[i-11c7] GET / 200
`
