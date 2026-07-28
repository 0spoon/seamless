// The auth-refresh scenario: the continue-work beat, forked from the
// terminal-scene fixture (internal/demokit scenes.go -- forked, not imported;
// those defs are branding surface that must stay stable while this suite
// churns).
//
// Starting state: myapp's plan:auth-refresh is 4/6 done with step 5
// ("Rate-limit POST /auth/refresh") the one claimable step, an ~18h-old
// finding that ends "Next is step 5", and a memory set whose load-bearing
// item is the rate-limit-not-in-memory gotcha: the correct limiter keeps its
// counter in shared storage, not a per-process map. A Seamless-ful arm gets
// the finding, the plan line, and the gotcha in its SessionStart briefing;
// the vanilla arm has only the repo. A run passes when the agent lands the
// step-5 rate limit on shared storage.

package bench

import (
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	authRefreshName    = "auth-refresh"
	authRefreshProject = "myapp"
	// authRefreshPlan is the seeded plan slug; its step tasks carry it.
	authRefreshPlan = "auth-refresh"
	// authRefreshMemory is the scenario's load-bearing memory: its description
	// alone names the correct step-5 design. The seed writes it and the grader
	// checks the agent consulted it, from this one name.
	authRefreshMemory = "rate-limit-not-in-memory"
	// authRefreshStep5 is the plan's sole claimable step -- the work the run is
	// graded on. Shared by the seed table and the grader so they cannot drift.
	authRefreshStep5 = "Rate-limit POST /auth/refresh (per-IP and per-family)"
	// authRefreshSession is yesterday's completed session; memories and the
	// trial hang off it, and its finding is what makes "continue where we
	// left off" answerable.
	authRefreshSession = "cc/1a2b3c4d"
	// authRefreshFinding is that session's ~18h-old summary: the briefing
	// surfaces it and the with-side reads "Next is step 5" from it.
	authRefreshFinding = "Finished auth-refresh step 4: the auth responses now set the session and refresh cookies HttpOnly+Secure+SameSite=Lax, and the token pair no longer rides in the JSON body. Reuse-detection from step 3 revokes the whole family on replay. Next is step 5 -- rate-limiting POST /auth/refresh; the handler is in auth.go and has no limiter yet."
)

// authRefresh is briefing-surfaced (finding + plan + memory index land at
// SessionStart), so it runs headless via plain `claude -p`; RequiresRecall
// stays false.
var authRefresh = Scenario{
	Name:   authRefreshName,
	Prompt: "continue where we left off",
	Seed:   seedAuthRefresh,
	Grader: authRefreshGrader,
}

// authRefreshMemories is the seeded myapp memory set: three auth constraints,
// the load-bearing rate-limit gotcha, and enough unrelated project memory
// that the briefing's index is realistically noisy rather than a single
// planted answer.
var authRefreshMemories = []struct {
	kind    core.MemoryKind
	name    string
	desc    string
	body    string
	agoDays int
}{
	{core.KindConstraint, "refresh-token-single-use",
		"A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.",
		"Every call to POST /auth/refresh must mint a new token pair and invalidate the presented refresh token. A replay of an already-rotated token is the signature of a stolen token: revoke the entire family and force re-login. Never hand back the same refresh token twice.", 16},
	{core.KindConstraint, "auth-cookies-httponly-secure",
		"Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).",
		"Set the session and refresh cookies with HttpOnly, Secure, and SameSite=Lax. Do not return the tokens in the response body and do not let any client script read them: an XSS bug then can't exfiltrate a session. The browser attaches them automatically.", 14},
	// The non-derivable landmine (nothing in myapp shows an external-link
	// entry flow): kept in the fork because it seeds a future briefing-catch
	// scenario and keeps the briefing's constraint section realistic.
	{core.KindConstraint, "auth-cookies-samesite-lax",
		"Auth cookies must stay SameSite=Lax, not Strict: Strict logs out users arriving from an external link. Harden elsewhere (__Host-, TTL).",
		"Keep the session and refresh cookies on SameSite=Lax; do not move them to Strict. We shipped Strict once on a scanner's \"harden the cookies\" finding and it broke login for everyone arriving from an external link: Strict withholds the cookie on the first cross-site top-level navigation, so a user clicking in from an email, a partner site, or an OAuth callback lands logged out, re-auths, and files a bug. Lax already blocks the CSRF vector that matters here. If a scanner flags Lax, suppress that rule or harden elsewhere -- add the __Host- prefix, keep HttpOnly and Secure, shorten the token TTL -- but never set these cookies to Strict.", 13},
	{core.KindGotcha, "edge-cache-gotcha",
		"CDN strips Vary on 304s; never cache HTML",
		"Never put Cache-Control (or any positive caching header) on an HTML response.\n\nOur CDN strips the Vary header from 304 Not Modified responses. HTML varies by the session cookie, so a cached page built for one signed-in user gets served to the next -- their name, their data, wrong person. We shipped this once; it took an afternoon to trace.\n\nCaching that is safe here:\n- Static assets (JS, CSS, images) keyed by a content hash in the filename -- long max-age, immutable.\n- Nothing else. Leave HTML uncached, or send Cache-Control: no-store on any authenticated route.\n\nWhen a task says \"HTML responses are slow -- add caching\", cache the assets, not the HTML.", 4},
	// The scenario's load-bearing memory: its description alone (surfaced in
	// the briefing's memory index) names the correct step-5 design.
	{core.KindGotcha, authRefreshMemory,
		"In-memory rate limit on the refresh endpoint resets per instance; use shared storage.",
		"A rate limiter kept in a process map only sees one instance's traffic. myapp runs several instances behind the load balancer, so an attacker's requests fan out across them and each instance sees a fraction of the limit -- the endpoint is effectively unthrottled. Keep the counter in shared storage (Redis) keyed by IP and token family, with a short sliding window.", 7},
	{core.KindGotcha, "persist-refresh-tokens",
		"Persist refresh tokens to the database the safe way: one hard rule about what the token column may store.",
		"When you persist refresh tokens, store only a SHA-256 hash of each token, never the raw value; on rotate, hash the presented token and look it up by hash. A database snapshot, backup, or read-replica leak of a raw-token column is instant account takeover for every live session -- refresh tokens are bearer credentials. Hashing makes a stolen snapshot useless. The in-memory store keeps raw tokens today, which is fine for process memory but not for anything that reaches disk or a backup.", 8},
	{core.KindGotcha, "chroma-boot-race",
		"Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.",
		"On a cold start the API comes up before Chroma finishes loading its collections, and the first embedding query fails. Retry it with exponential backoff (a few hundred ms, up to ~10s) rather than exiting -- the readiness probe should gate traffic, not the process.", 9},
	{core.KindRunbook, "deploy-runbook",
		"Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.",
		"1. Build the static binary. 2. Run pending migrations against the primary. 3. Start the new instance and poll /healthz until it reports ready. 4. Flip the load balancer to the new instance. 5. Keep the old one warm for one rollback window, then retire it.", 6},
	{core.KindGotcha, "postgres-timeouts",
		"Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.",
		"The primary sets a low statement_timeout so a runaway query can't stall writes. Long analytics/report queries hit it and error out. Route them to the read replica, which raises statement_timeout for the reporting role; never lift the timeout on the primary.", 12},
}

// authRefreshSteps is plan:auth-refresh as a dependency chain: 4 done, step 5
// ready, step 6 blocked on step 5. Unlike the scene fixture there is no race
// variant -- the bench wants exactly one claimable continue-work target.
var authRefreshSteps = []struct {
	title   string
	done    bool
	dep     int // index of the step this depends on; -1 = none
	agoDays int
	closedH int // hours after created that a done step closed
}{
	{"Add the token_families table and the refresh-token store", true, -1, 5, 6},
	{"Issue a rotating token pair from POST /auth/refresh", true, 0, 4, 5},
	{"Detect refresh-token reuse and revoke the whole family", true, 1, 3, 20},
	{"Set HttpOnly, Secure, SameSite cookies on the auth responses", true, 2, 1, 6},
	{authRefreshStep5, false, 3, 1, 0},
	{"Emit metrics and an alert for refresh-reuse revocations", false, 4, 1, 0},
}

func seedAuthRefresh(s *demokit.Seeder, repoPath string) error {
	if err := s.EnsureProject(authRefreshProject, authRefreshProject); err != nil {
		return err
	}
	if repoPath != "" {
		if err := s.MapRepo(repoPath, authRefreshProject); err != nil {
			return err
		}
	}

	end := s.Now().Add(-18 * time.Hour)
	start := end.Add(-45 * time.Minute)
	sess := core.Session{
		ID: s.IdAt(start), Name: authRefreshSession, ProjectSlug: authRefreshProject,
		Status: core.SessionCompleted, Findings: authRefreshFinding,
		ExternalSessionID: "b2e7c9d4-1f3a-4a68",
		CWD:               "/home/dev/myapp", Source: "startup", Ambient: true,
		CreatedAt: start, UpdatedAt: end,
	}
	if err := s.CreateSession(sess); err != nil {
		return err
	}

	for _, m := range authRefreshMemories {
		created := s.DaysAgo(m.agoDays).Add(10 * time.Hour)
		if err := s.WriteMemory(core.Memory{
			ID: s.IdAt(created), Kind: m.kind, Name: m.name, Description: m.desc,
			Project: authRefreshProject, Body: m.body, Created: created, Updated: created,
			ValidFrom: created, SourceSession: sess.Name,
		}); err != nil {
			return err
		}
	}

	const plan = authRefreshPlan
	ids := make([]string, len(authRefreshSteps))
	for i, st := range authRefreshSteps {
		// Same-day steps get staggered minutes so creation order is distinct.
		created := s.DaysAgo(st.agoDays).Add(10*time.Hour + time.Duration(i)*time.Minute)
		t := core.Task{
			ID: s.IdAt(created), ProjectSlug: authRefreshProject, Title: st.title,
			Body:      "Step of plan:" + plan + ". See the auth-refresh plan note for acceptance criteria.",
			Status:    core.TaskOpen,
			CreatedBy: sess.Name, PlanSlug: plan,
			CreatedAt: created, UpdatedAt: created,
		}
		if st.dep >= 0 {
			t.DependsOn = []string{ids[st.dep]}
		}
		if st.done {
			t.Status = core.TaskDone
			closed := created.Add(time.Duration(st.closedH) * time.Hour)
			t.ClosedAt = &closed
			t.UpdatedAt = closed
		}
		if err := s.CreateTask(t); err != nil {
			return err
		}
		ids[i] = t.ID
	}

	// One failed trial: the pool-timeout dead end a continuing agent should
	// not retry.
	at := s.Now().Add(-26 * time.Hour)
	return s.CreateTrial(core.Trial{
		ID: s.IdAt(at), Lab: "refresh-500s",
		Title:     "Bump the DB pool timeout to stop intermittent /auth/refresh 500s",
		Changes:   "Raised the pgx pool size 10->25 and MaxConnIdleTime 30s->5m.",
		Expected:  "The intermittent 500s on POST /auth/refresh disappear under load.",
		Actual:    "500s continued. Root cause was the reuse-detector deleting the token family mid-request, not pool exhaustion -- a pool bump can't fix it.",
		Outcome:   core.OutcomeFail,
		SessionID: sess.ID, ProjectSlug: authRefreshProject, CreatedAt: at,
	})
}
