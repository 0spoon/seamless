// Shared seeding vocabulary for the benchmark scenarios: the myapp project,
// the common memory pool the scenarios draw their noise from, and the
// session/plan/trial helpers every seed is built from.
//
// The memory pool is FORKED from internal/demokit's scene specs, not imported
// (bench.go's package comment says why). One pool, five scenarios: each
// scenario picks its load-bearing memories plus enough of the common noise
// that the briefing's memory index reads like a real project's, not like a
// single planted answer.

package bench

import (
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

// benchProject is the demo project every scenario seeds; the demo repo maps
// to it so the arm's sessions bind there.
const benchProject = "myapp"

// seedMemory is one memory a scenario seeds, backdated agoDays.
type seedMemory struct {
	kind    core.MemoryKind
	name    string
	desc    string
	body    string
	agoDays int
}

// commonMemories is the shared myapp memory pool. Scenario seeds pick from it
// by name (noiseMemories) and append their own load-bearing entries; a
// load-bearing entry for one scenario (auth-cookies-samesite-lax) is ordinary
// noise for the others, exactly like a real project's memory index.
var commonMemories = map[string]seedMemory{
	"refresh-token-single-use": {core.KindConstraint, "refresh-token-single-use",
		"A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.",
		"Every call to POST /auth/refresh must mint a new token pair and invalidate the presented refresh token. A replay of an already-rotated token is the signature of a stolen token: revoke the entire family and force re-login. Never hand back the same refresh token twice.", 16},
	"auth-cookies-httponly-secure": {core.KindConstraint, "auth-cookies-httponly-secure",
		"Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).",
		"Set the session and refresh cookies with HttpOnly, Secure, and SameSite=Lax. Do not return the tokens in the response body and do not let any client script read them: an XSS bug then can't exfiltrate a session. The browser attaches them automatically.", 14},
	// Load-bearing for cookie-hardening; realistic constraint noise elsewhere.
	// Its description alone -- the one line the briefing's memory index shows
	// -- carries both the veto (never Strict) and the agreed resolution
	// (__Host-), because that is all a headless run is guaranteed to surface.
	"auth-cookies-samesite-lax": {core.KindConstraint, "auth-cookies-samesite-lax",
		"Auth cookies stay SameSite=Lax, never Strict (Strict logs out everyone arriving from an external link); resolve scanner findings with the __Host- prefix instead.",
		"Keep the session and refresh cookies on SameSite=Lax; do not move them to Strict. We shipped Strict once on a scanner's \"harden the cookies\" finding and it broke login for everyone arriving from an external link: Strict withholds the cookie on the first cross-site top-level navigation, so a user clicking in from an email, a partner site, or an OAuth callback lands logged out, re-auths, and files a bug. Lax already blocks the CSRF vector that matters here.\n\nThe agreed resolution when a scanner flags these cookies: add the __Host- prefix to the cookie names (they already ship Secure with Path=/), keep HttpOnly and Secure, and leave SameSite=Lax alone. Rename the cookie at every site that sets OR reads it -- a half-rename silently signs everyone out.", 13},
	"persist-refresh-tokens": {core.KindGotcha, "persist-refresh-tokens",
		"Persist refresh tokens to disk the safe way: store only a SHA-256 hash of each token, never the raw value.",
		"When you persist refresh tokens, store only a SHA-256 hash of each token, never the raw value; on rotate, hash the presented token and look it up by hash. A database snapshot, backup, or read-replica leak of a raw-token column is instant account takeover for every live session -- refresh tokens are bearer credentials. Hashing makes a stolen snapshot useless. The in-memory store keeps raw tokens today, which is fine for process memory but not for anything that reaches disk or a backup.", 8},
	"dashboard-latency-budget": {core.KindReference, "dashboard-latency-budget",
		"The dashboard's server-side p95 budget is 300ms; the pager fires at 450ms sustained over 5 minutes.",
		"The dashboard route (GET /) has a server-side p95 budget of 300ms measured at the load balancer, and the on-call pager fires when p95 stays above 450ms for 5 minutes. The budget covers the origin's own time only -- CDN and client time are tracked separately in the RUM dashboard, which is where the page-load complaints come from.", 10},
	"cdn-purge-by-tag": {core.KindRunbook, "cdn-purge-by-tag",
		"Purge the CDN by surrogate tag, not by URL: a full purge takes ~8 minutes and is rate-limited to three per hour.",
		"Every origin response carries a Surrogate-Key header naming the route it belongs to. To evict something from the CDN, purge that tag rather than the URL: tag purges are near-instant, while a full purge takes about 8 minutes to propagate and the provider rate-limits us to three per hour. Record every purge in the deploy channel -- a purge storm during an incident is how we lost an afternoon.", 9},
	"deploy-runbook": {core.KindRunbook, "deploy-runbook",
		"Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.",
		"1. Build the static binary. 2. Run pending migrations against the primary. 3. Start the new instance and poll /healthz until it reports ready. 4. Flip the load balancer to the new instance. 5. Keep the old one warm for one rollback window, then retire it.", 6},
	"postgres-timeouts": {core.KindGotcha, "postgres-timeouts",
		"Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.",
		"The primary sets a low statement_timeout so a runaway query can't stall writes. Long analytics/report queries hit it and error out. Route them to the read replica, which raises statement_timeout for the reporting role; never lift the timeout on the primary.", 12},
	"chroma-boot-race": {core.KindGotcha, "chroma-boot-race",
		"Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.",
		"On a cold start the API comes up before Chroma finishes loading its collections, and the first embedding query fails. Retry it with exponential backoff (a few hundred ms, up to ~10s) rather than exiting -- the readiness probe should gate traffic, not the process.", 11},
}

// noiseMemories picks named entries from the common pool; an unknown name is
// a programming error surfaced at seed time (and by the seed tests).
func noiseMemories(names ...string) ([]seedMemory, error) {
	out := make([]seedMemory, 0, len(names))
	for _, n := range names {
		m, ok := commonMemories[n]
		if !ok {
			return nil, fmt.Errorf("bench: no common memory named %q", n)
		}
		out = append(out, m)
	}
	return out, nil
}

// seedProject ensures the myapp project and maps the arm's demo repo to it
// ("" skips the mapping, as SeedFunc documents).
func seedProject(s *demokit.Seeder, repoPath string) error {
	if err := s.EnsureProject(benchProject, benchProject); err != nil {
		return err
	}
	if repoPath == "" {
		return nil
	}
	return s.MapRepo(repoPath, benchProject)
}

// seedFinishedSession creates the completed prior session a scenario's finding
// hangs off, ended agoHours ago after runMinutes of work.
func seedFinishedSession(s *demokit.Seeder, name, externalID, findings string, agoHours, runMinutes int) (core.Session, error) {
	end := s.Now().Add(-time.Duration(agoHours) * time.Hour)
	start := end.Add(-time.Duration(runMinutes) * time.Minute)
	sess := core.Session{
		ID: s.IdAt(start), Name: name, ProjectSlug: benchProject,
		Status: core.SessionCompleted, Findings: findings,
		ExternalSessionID: externalID,
		CWD:               "/home/dev/myapp", Source: "startup", Ambient: true,
		CreatedAt: start, UpdatedAt: end,
	}
	if err := s.CreateSession(sess); err != nil {
		return core.Session{}, err
	}
	return sess, nil
}

// seedMemories writes a scenario's memory set, each backdated to its own
// agoDays at a stable hour.
func seedMemories(s *demokit.Seeder, sourceSession string, mems []seedMemory) error {
	for _, m := range mems {
		created := s.DaysAgo(m.agoDays).Add(10 * time.Hour)
		if err := s.WriteMemory(core.Memory{
			ID: s.IdAt(created), Kind: m.kind, Name: m.name, Description: m.desc,
			Project: benchProject, Body: m.body, Created: created, Updated: created,
			ValidFrom: created, SourceSession: sourceSession,
		}); err != nil {
			return err
		}
	}
	return nil
}

// seedStep is one plan step in a scenario's dependency chain.
type seedStep struct {
	title   string
	done    bool
	dep     int // index of the step this depends on; -1 = none
	agoDays int
	closedH int // hours after created that a done step closed
}

// seedPlanSteps creates a plan's step tasks as a dependency chain. Every
// scenario seeds exactly one claimable step -- the work the run is graded on
// -- which the chain expresses as "not done, dependency done".
func seedPlanSteps(s *demokit.Seeder, plan, createdBy string, steps []seedStep) error {
	ids := make([]string, len(steps))
	for i, st := range steps {
		// Same-day steps get staggered minutes so creation order is distinct.
		created := s.DaysAgo(st.agoDays).Add(10*time.Hour + time.Duration(i)*time.Minute)
		t := core.Task{
			ID: s.IdAt(created), ProjectSlug: benchProject, Title: st.title,
			Body:      "Step of plan:" + plan + ". See the " + plan + " plan note for acceptance criteria.",
			Status:    core.TaskOpen,
			CreatedBy: createdBy, PlanSlug: plan,
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
	return nil
}

// seedTrial is one recorded trial in a scenario's research lab, backdated
// agoHours and tied to the prior session.
type seedTrial struct {
	lab      string
	title    string
	changes  string
	expected string
	actual   string
	outcome  core.TrialOutcome
	agoHours int
}

// seedTrials records a scenario's trials.
func seedTrials(s *demokit.Seeder, sess core.Session, trials []seedTrial) error {
	for _, tr := range trials {
		at := s.Now().Add(-time.Duration(tr.agoHours) * time.Hour)
		if err := s.CreateTrial(core.Trial{
			ID: s.IdAt(at), Lab: tr.lab,
			Title: tr.title, Changes: tr.changes, Expected: tr.expected, Actual: tr.actual,
			Outcome:   tr.outcome,
			SessionID: sess.ID, ProjectSlug: benchProject, CreatedAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}
