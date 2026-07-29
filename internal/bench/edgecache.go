// The edge-caching scenario: the briefing-catch beat, forked from the
// terminal-scene fixture (internal/demokit scenes.go -- forked, not imported;
// those defs are branding surface that must stay stable while this suite
// churns).
//
// Starting state: myapp's plan:page-speed is 2/4 done with "Add HTTP caching to
// the dashboard responses" the one claimable step, a ~20h-old finding that says
// the service sends no Cache-Control at all today, and a memory set whose
// load-bearing item is the edge-cache gotcha: the CDN strips Vary from 304s, so
// the per-user HTML must never carry a positive caching header while the
// content-hashed static assets can carry a long immutable one.
//
// The prompt ("the HTML responses are slow -- add caching") is the trap, and it
// is deliberately neutral: it names the symptom and the obvious remedy and says
// nothing about the constraint. The naive fix -- Cache-Control on the HTML --
// caches one signed-in user's page at the edge and serves it to the next. A
// Seamless-ful arm gets the constraint in its SessionStart briefing; the vanilla
// arm has only the repo, where the danger is not derivable (nothing in myapp
// shows the CDN's 304 behaviour). That contrast is the uplift this scenario
// measures.

package bench

import (
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	edgeCacheName    = "edge-caching"
	edgeCacheProject = "myapp"
	// edgeCachePlan is the seeded plan slug; its step tasks carry it.
	edgeCachePlan = "page-speed"
	// edgeCacheMemory is the scenario's load-bearing memory: its description
	// alone (the line the briefing's memory index shows) is enough to avoid the
	// landmine, and its body carries the why. The seed writes it and the grader
	// checks the agent consulted it, from this one name.
	edgeCacheMemory = "edge-cache-gotcha"
	// edgeCacheStep is the plan's sole claimable step -- the work the run is
	// graded on. Shared by the seed table and the grader so they cannot drift.
	// Its wording restates the prompt and gives nothing else away.
	edgeCacheStep = "Add HTTP caching to the dashboard responses"
	// edgeCacheSession is the completed session the finding hangs off.
	edgeCacheSession = "cc/7f4e2b16"
	// edgeCacheFinding is that session's ~20h-old summary. It sets the work up
	// -- what shipped, where the time goes, what to do next -- WITHOUT naming
	// the constraint: the caching danger must reach the agent through the
	// memory or not at all, or the scenario measures nothing.
	edgeCacheFinding = "Finished page-speed step 2: the dashboard's JS and CSS now ship from /static/ under content-hashed filenames, so an asset URL's bytes never change once it is deployed. Profiling from step 1: the home handler itself renders in ~4ms and the rest of the page-load time is round-trips -- every navigation re-fetches the page and both assets, because the service sends no Cache-Control on anything today. Next is step 3 -- the caching pass on the response headers; the handlers are in server.go."
)

// edgeCaching is briefing-surfaced (finding + plan + memory index land at
// SessionStart), so it runs headless via plain `claude -p`; RequiresRecall
// stays false.
var edgeCaching = Scenario{
	Name:   edgeCacheName,
	Prompt: "the HTML responses are slow -- add caching",
	Seed:   seedEdgeCaching,
	Grader: edgeCacheGrader,
}

// edgeCacheMemories is the seeded myapp memory set: the load-bearing edge-cache
// gotcha plus enough surrounding project memory (the auth constraints the
// service already lives under, a latency budget, two runbooks, two unrelated
// gotchas) that the briefing's index is realistically noisy rather than a single
// planted answer. Nothing here but edge-cache-gotcha says anything about what
// may or may not be cached.
var edgeCacheMemories = []struct {
	kind    core.MemoryKind
	name    string
	desc    string
	body    string
	agoDays int
}{
	// The scenario's load-bearing memory.
	{core.KindGotcha, edgeCacheMemory,
		"CDN strips Vary on 304s; never cache HTML",
		"Never put Cache-Control (or any positive caching header) on an HTML response.\n\nOur CDN strips the Vary header from 304 Not Modified responses. HTML varies by the session cookie, so a cached page built for one signed-in user gets served to the next -- their name, their data, wrong person. We shipped this once; it took an afternoon to trace.\n\nCaching that is safe here:\n- Static assets (JS, CSS, images) keyed by a content hash in the filename -- long max-age, immutable.\n- Nothing else. Leave HTML uncached, or send Cache-Control: no-store on any authenticated route.\n\nWhen a task says \"HTML responses are slow -- add caching\", cache the assets, not the HTML.", 4},
	{core.KindConstraint, "refresh-token-single-use",
		"A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.",
		"Every call to POST /auth/refresh must mint a new token pair and invalidate the presented refresh token. A replay of an already-rotated token is the signature of a stolen token: revoke the entire family and force re-login. Never hand back the same refresh token twice.", 16},
	{core.KindConstraint, "auth-cookies-httponly-secure",
		"Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).",
		"Set the session and refresh cookies with HttpOnly, Secure, and SameSite=Lax. Do not return the tokens in the response body and do not let any client script read them: an XSS bug then can't exfiltrate a session. The browser attaches them automatically.", 14},
	{core.KindConstraint, "auth-cookies-samesite-lax",
		"Auth cookies must stay SameSite=Lax, not Strict: Strict logs out users arriving from an external link. Harden elsewhere (__Host-, TTL).",
		"Keep the session and refresh cookies on SameSite=Lax; do not move them to Strict. We shipped Strict once on a scanner's \"harden the cookies\" finding and it broke login for everyone arriving from an external link: Strict withholds the cookie on the first cross-site top-level navigation, so a user clicking in from an email, a partner site, or an OAuth callback lands logged out, re-auths, and files a bug. Lax already blocks the CSRF vector that matters here. If a scanner flags Lax, suppress that rule or harden elsewhere -- add the __Host- prefix, keep HttpOnly and Secure, shorten the token TTL -- but never set these cookies to Strict.", 13},
	{core.KindReference, "dashboard-latency-budget",
		"The dashboard's server-side p95 budget is 300ms; the pager fires at 450ms sustained over 5 minutes.",
		"The dashboard route (GET /) has a server-side p95 budget of 300ms measured at the load balancer, and the on-call pager fires when p95 stays above 450ms for 5 minutes. The budget covers the origin's own time only -- CDN and client time are tracked separately in the RUM dashboard, which is where the page-load complaints come from.", 10},
	{core.KindRunbook, "cdn-purge-by-tag",
		"Purge the CDN by surrogate tag, not by URL: a full purge takes ~8 minutes and is rate-limited to three per hour.",
		"Every origin response carries a Surrogate-Key header naming the route it belongs to. To evict something from the CDN, purge that tag rather than the URL: tag purges are near-instant, while a full purge takes about 8 minutes to propagate and the provider rate-limits us to three per hour. Record every purge in the deploy channel -- a purge storm during an incident is how we lost an afternoon.", 9},
	{core.KindRunbook, "deploy-runbook",
		"Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.",
		"1. Build the static binary. 2. Run pending migrations against the primary. 3. Start the new instance and poll /healthz until it reports ready. 4. Flip the load balancer to the new instance. 5. Keep the old one warm for one rollback window, then retire it.", 6},
	{core.KindGotcha, "postgres-timeouts",
		"Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.",
		"The primary sets a low statement_timeout so a runaway query can't stall writes. Long analytics/report queries hit it and error out. Route them to the read replica, which raises statement_timeout for the reporting role; never lift the timeout on the primary.", 12},
	{core.KindGotcha, "chroma-boot-race",
		"Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.",
		"On a cold start the API comes up before Chroma finishes loading its collections, and the first embedding query fails. Retry it with exponential backoff (a few hundred ms, up to ~10s) rather than exiting -- the readiness probe should gate traffic, not the process.", 8},
}

// edgeCacheSteps is plan:page-speed as a dependency chain: 2 done, the caching
// step ready, the alerting step blocked on it -- exactly one claimable target.
var edgeCacheSteps = []struct {
	title   string
	done    bool
	dep     int // index of the step this depends on; -1 = none
	agoDays int
	closedH int // hours after created that a done step closed
}{
	{"Profile the dashboard request path and record where the time goes", true, -1, 6, 5},
	{"Serve the dashboard's JS and CSS from /static/ with content-hashed names", true, 0, 3, 9},
	{edgeCacheStep, false, 1, 1, 0},
	{"Alert on the dashboard's p95 latency budget", false, 2, 1, 0},
}

func seedEdgeCaching(s *demokit.Seeder, repoPath string) error {
	if err := s.EnsureProject(edgeCacheProject, edgeCacheProject); err != nil {
		return err
	}
	if repoPath != "" {
		if err := s.MapRepo(repoPath, edgeCacheProject); err != nil {
			return err
		}
	}

	end := s.Now().Add(-20 * time.Hour)
	start := end.Add(-35 * time.Minute)
	sess := core.Session{
		ID: s.IdAt(start), Name: edgeCacheSession, ProjectSlug: edgeCacheProject,
		Status: core.SessionCompleted, Findings: edgeCacheFinding,
		ExternalSessionID: "7f4e2b16-98ca-4d02",
		CWD:               "/home/dev/myapp", Source: "startup", Ambient: true,
		CreatedAt: start, UpdatedAt: end,
	}
	if err := s.CreateSession(sess); err != nil {
		return err
	}

	for _, m := range edgeCacheMemories {
		created := s.DaysAgo(m.agoDays).Add(11 * time.Hour)
		if err := s.WriteMemory(core.Memory{
			ID: s.IdAt(created), Kind: m.kind, Name: m.name, Description: m.desc,
			Project: edgeCacheProject, Body: m.body, Created: created, Updated: created,
			ValidFrom: created, SourceSession: sess.Name,
		}); err != nil {
			return err
		}
	}

	const plan = edgeCachePlan
	ids := make([]string, len(edgeCacheSteps))
	for i, st := range edgeCacheSteps {
		// Same-day steps get staggered minutes so creation order is distinct.
		created := s.DaysAgo(st.agoDays).Add(11*time.Hour + time.Duration(i)*time.Minute)
		t := core.Task{
			ID: s.IdAt(created), ProjectSlug: edgeCacheProject, Title: st.title,
			Body:      "Step of plan:" + plan + ". See the page-speed plan note for acceptance criteria.",
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

	// One failed trial: the round-trip dead end a continuing agent should not
	// retry. It reports what profiling saw -- assets re-fetched, no headers
	// anywhere -- and prescribes nothing, so the landmine stays un-defused.
	at := s.Now().Add(-30 * time.Hour)
	return s.CreateTrial(core.Trial{
		ID: s.IdAt(at), Lab: "dashboard-latency",
		Title:     "Push the dashboard's CSS and JS with HTTP/2 server push",
		Changes:   "Added Link: rel=preload headers with nopush disabled on GET / so the edge pushes style.css and app.js.",
		Expected:  "The two asset round-trips disappear from the first paint and p95 page-load drops.",
		Actual:    "No measurable change: Chrome removed server-push support in 106, so the pushes were ignored and both assets were still fetched on every navigation. Push was never the lever -- the responses carry no caching or validator headers at all.",
		Outcome:   core.OutcomeFail,
		SessionID: sess.ID, ProjectSlug: edgeCacheProject, CreatedAt: at,
	})
}
