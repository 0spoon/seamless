// Scene-mode seeding for the landing page's with/without terminal captures
// (plan:terminal-scenes). Unlike the console fleet seed in seeder.go/data.go,
// this stands up a MINIMAL, precise state: one project (myapp), one active plan,
// a handful of memories, one backdated finding, and one failed trial -- exactly
// what scene 1's briefing and scene 2's landmine need, and nothing that would
// clutter a recorded briefing. The repo->project mapping is seeded here too, so
// a single `demoseed -scenes -repo <path>` run (before the daemon starts) leaves
// the throwaway instance ready to record against.

package demokit

import (
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

const sceneProject = "myapp"

// sceneFinding is the "yesterday's work" summary scene 1 opens on: the with-side
// agent reads it from the briefing and knows to continue at step 5.
const sceneFinding = "Finished auth-refresh step 4: the auth responses now set the session and refresh cookies HttpOnly+Secure+SameSite=Lax, and the token pair no longer rides in the JSON body. Reuse-detection from step 3 revokes the whole family on replay. Next is step 5 -- rate-limiting POST /auth/refresh; the handler is in auth.go and has no limiter yet."

// edgeCacheBody is the full guidance the with-side agent reads (via memory_read)
// after the briefing flags edge-cache-gotcha: cache the content-hashed static
// assets, never the HTML. Scene 2 surfaces the memory through the briefing's
// memory-index line (its description), not the prompt-recall matcher -- that
// matcher scores name+description only, and the verbatim short description shares
// too few tokens with a natural caching prompt to clear its floors.
const edgeCacheBody = `Never put Cache-Control (or any positive caching header) on an HTML response.

Our CDN strips the Vary header from 304 Not Modified responses. HTML varies by the session cookie, so a cached page built for one signed-in user gets served to the next -- their name, their data, wrong person. We shipped this once; it took an afternoon to trace.

Caching that is safe here:
- Static assets (JS, CSS, images) keyed by a content hash in the filename -- long max-age, immutable.
- Nothing else. Leave HTML uncached, or send Cache-Control: no-store on any authenticated route.

When a task says "HTML responses are slow -- add caching", cache the assets, not the HTML.`

// scenes seeds the whole terminal-scenes fixture into the throwaway data dir.
func (s *Seeder) Scenes(repoPath string, race bool) {
	if _, err := store.EnsureProject(s.ctx, s.db, sceneProject, sceneProject); err != nil {
		log.Fatalf("demoseed: scenes: project: %v", err)
	}
	if repoPath != "" {
		abs, err := filepath.Abs(repoPath)
		if err != nil {
			log.Fatalf("demoseed: scenes: repo path: %v", err)
		}
		if err := store.AddRepoMapping(s.ctx, s.db, abs, sceneProject); err != nil {
			log.Fatalf("demoseed: scenes: map repo: %v", err)
		}
		log.Printf("demoseed: scenes: mapped %s -> %s", abs, sceneProject)
	}

	// Yesterday's completed session, ~18h old, whose finding scene 1 opens on.
	yest := s.sceneSession("cc/1a2b3c4d", s.now.Add(-18*time.Hour), sceneFinding)

	s.sceneMemories(yest.name)
	s.scenePlan(race, yest.name)
	s.sceneTrial(yest)
	s.sceneSummary(repoPath, race)
}

// sceneSession creates one completed myapp session with the given finding,
// ending at `end` (so the briefing ages it from there).
func (s *Seeder) sceneSession(name string, end time.Time, findings string) sessRec {
	start := end.Add(-45 * time.Minute)
	sess := core.Session{
		ID: s.IdAt(start), Name: name, ProjectSlug: sceneProject, Status: core.SessionCompleted,
		Findings:          findings,
		ExternalSessionID: fmt.Sprintf("%08x-%04x-%04x", s.rng.Uint32(), s.rng.Uint32()&0xffff, s.rng.Uint32()&0xffff),
		CWD:               "/home/dev/myapp", Source: "startup", Ambient: true,
		CreatedAt: start, UpdatedAt: end,
	}
	if err := store.CreateSession(s.ctx, s.db, sess); err != nil {
		log.Fatalf("demoseed: scenes: session %s: %v", name, err)
	}
	return sessRec{id: sess.ID, name: name, project: sceneProject, start: start, end: end}
}

// sceneMemories seeds the myapp memory set: three constraints (always pinned in
// the briefing -- auth-cookies-samesite-lax is scene 2's briefing-catch landmine),
// the four hero-terminal files, and two recall-beat gotchas (rate-limit and
// persist-token) engineered to fire the mid-session <seam-recall> injection.
// Kinds and filenames match the hero term on docs/index.html so the closing
// `ls memory/myapp/` beat mirrors it.
func (s *Seeder) sceneMemories(source string) {
	mems := []struct {
		kind, name, desc, body string
		agoDays                int
	}{
		{"constraint", "refresh-token-single-use",
			"A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.",
			"Every call to POST /auth/refresh must mint a new token pair and invalidate the presented refresh token. A replay of an already-rotated token is the signature of a stolen token: revoke the entire family and force re-login. Never hand back the same refresh token twice.", 16},
		{"constraint", "auth-cookies-httponly-secure",
			"Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).",
			"Set the session and refresh cookies with HttpOnly, Secure, and SameSite=Lax. Do not return the tokens in the response body and do not let any client script read them: an XSS bug then can't exfiltrate a session. The browser attaches them automatically.", 14},
		// Scene 2's briefing-catch landmine. The danger is NON-derivable from the
		// repo: nothing in myapp shows an external-link / OAuth-callback entry flow,
		// so an agent reasoning only from the code cannot know Strict breaks login.
		// It lives in a pinned constraint so the briefing surfaces it verbatim and
		// the with-side can cite it in one line. See [[scene-demo-repo-must-be-seamless-free]].
		{"constraint", "auth-cookies-samesite-lax",
			"Auth cookies must stay SameSite=Lax, not Strict: Strict logs out users arriving from an external link. Harden elsewhere (__Host-, TTL).",
			"Keep the session and refresh cookies on SameSite=Lax; do not move them to Strict. We shipped Strict once on a scanner's \"harden the cookies\" finding and it broke login for everyone arriving from an external link: Strict withholds the cookie on the first cross-site top-level navigation, so a user clicking in from an email, a partner site, or an OAuth callback lands logged out, re-auths, and files a bug. Lax already blocks the CSRF vector that matters here. If a scanner flags Lax, suppress that rule or harden elsewhere -- add the __Host- prefix, keep HttpOnly and Secure, shorten the token TTL -- but never set these cookies to Strict.", 13},
		{"gotcha", "edge-cache-gotcha",
			"CDN strips Vary on 304s; never cache HTML",
			edgeCacheBody, 4},
		// The recall-variant memory: its name+description share enough exact-form
		// tokens with the natural step-5 prompt ("add an in-memory rate limit to the
		// refresh endpoint") to clear PromptRecall's overlap>=2 AND score>=1.5 floors,
		// so the passive <seam-recall> injection fires mid-session and catches the
		// mistake. See [[promptrecall-lexical-name-desc-only]].
		{"gotcha", "rate-limit-not-in-memory",
			"In-memory rate limit on the refresh endpoint resets per instance; use shared storage.",
			"A rate limiter kept in a process map only sees one instance's traffic. myapp runs several instances behind the load balancer, so an attacker's requests fan out across them and each instance sees a fraction of the limit -- the endpoint is effectively unthrottled. Keep the counter in shared storage (Redis) keyed by IP and token family, with a short sliding window.", 7},
		// The recall-beat memory. A non-constraint gotcha whose description is a
		// generic pointer (topic + "one hard rule", NOT the fix) so the session-start
		// briefing index line does NOT give the answer away; the punchline lives in
		// the body. name+description share persist/refresh/tokens/database -- exact
		// word-forms of the natural prompt "persist the refresh tokens to the
		// database ..." -- so the mid-session <seam-recall> injection clears the
		// overlap>=2 / score>=1.5 floors and surfaces it at the moment of action,
		// prompting a memory_read the briefing alone would not. See
		// [[promptrecall-lexical-name-desc-only]].
		{"gotcha", "persist-refresh-tokens",
			"Persist refresh tokens to the database the safe way: one hard rule about what the token column may store.",
			"When you persist refresh tokens, store only a SHA-256 hash of each token, never the raw value; on rotate, hash the presented token and look it up by hash. A database snapshot, backup, or read-replica leak of a raw-token column is instant account takeover for every live session -- refresh tokens are bearer credentials. Hashing makes a stolen snapshot useless. The in-memory store keeps raw tokens today, which is fine for process memory but not for anything that reaches disk or a backup.", 8},
		{"gotcha", "chroma-boot-race",
			"Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.",
			"On a cold start the API comes up before Chroma finishes loading its collections, and the first embedding query fails. Retry it with exponential backoff (a few hundred ms, up to ~10s) rather than exiting -- the readiness probe should gate traffic, not the process.", 9},
		{"runbook", "deploy-runbook",
			"Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.",
			"1. Build the static binary. 2. Run pending migrations against the primary. 3. Start the new instance and poll /healthz until it reports ready. 4. Flip the load balancer to the new instance. 5. Keep the old one warm for one rollback window, then retire it.", 6},
		{"gotcha", "postgres-timeouts",
			"Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.",
			"The primary sets a low statement_timeout so a runaway query can't stall writes. Long analytics/report queries hit it and error out. Route them to the read replica, which raises statement_timeout for the reporting role; never lift the timeout on the primary.", 12},
	}
	for _, m := range mems {
		created := s.DaysAgo(m.agoDays).Add(time.Duration(9+s.rng.Intn(6)) * time.Hour)
		mem := core.Memory{
			ID: s.IdAt(created), Kind: core.MemoryKind(m.kind), Name: m.name, Description: m.desc,
			Project: sceneProject, Body: m.body, Created: created, Updated: created,
			ValidFrom: created, SourceSession: source,
		}
		if _, err := s.mgr.WriteMemory(s.ctx, mem); err != nil {
			log.Fatalf("demoseed: scenes: memory %s: %v", m.name, err)
		}
	}
}

// scenePlan seeds plan:auth-refresh as 6 dependency-chained steps: 4 done, step 5
// ready (the briefing's "1 claimable"), step 6 blocked on step 5. With race=true
// step 6 depends on step 4 instead, so it too is claimable and scene 3's two
// agents each get a distinct step after the claim collision.
func (s *Seeder) scenePlan(race bool, by string) {
	const slug = "auth-refresh"
	steps := []struct {
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
		{"Rate-limit POST /auth/refresh (per-IP and per-family)", false, 3, 1, 0},
		{"Emit metrics and an alert for refresh-reuse revocations", false, raceDep(race), 1, 0},
	}
	ids := make([]string, len(steps))
	for i, st := range steps {
		created := s.DaysAgo(st.agoDays).Add(time.Duration(9+s.rng.Intn(6)) * time.Hour)
		t := core.Task{
			ID: s.IdAt(created), ProjectSlug: sceneProject, Title: st.title,
			Body:      "Step of plan:" + slug + ". See the auth-refresh plan note for acceptance criteria.",
			Status:    core.TaskOpen,
			CreatedBy: by, PlanSlug: slug,
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
		if err := store.CreateTask(s.ctx, s.db, t); err != nil {
			log.Fatalf("demoseed: scenes: task %q: %v", st.title, err)
		}
		ids[i] = t.ID
	}
}

// raceDep picks step 6's dependency: step 4 (index 3, done -> step 6 ready) in
// race mode, else step 5 (index 4, open -> step 6 blocked).
func raceDep(race bool) int {
	if race {
		return 3
	}
	return 4
}

// sceneTrial seeds one failed trial: the dead-end scene 4 ("deja vu fix") plays
// off -- a pool-timeout bump that didn't fix the real bug.
func (s *Seeder) sceneTrial(sess sessRec) {
	at := s.now.Add(-26 * time.Hour)
	t := core.Trial{
		ID: s.IdAt(at), Lab: "refresh-500s",
		Title:     "Bump the DB pool timeout to stop intermittent /auth/refresh 500s",
		Changes:   "Raised the pgx pool size 10->25 and MaxConnIdleTime 30s->5m.",
		Expected:  "The intermittent 500s on POST /auth/refresh disappear under load.",
		Actual:    "500s continued. Root cause was the reuse-detector deleting the token family mid-request, not pool exhaustion -- a pool bump can't fix it.",
		Outcome:   core.OutcomeFail,
		SessionID: sess.id, ProjectSlug: sceneProject, CreatedAt: at,
	}
	if err := store.CreateTrial(s.ctx, s.db, t); err != nil {
		log.Fatalf("demoseed: scenes: trial: %v", err)
	}
}

// sceneSummary prints what was seeded and the one-line hint for the next step.
func (s *Seeder) sceneSummary(repoPath string, race bool) {
	claimable := "1 (step 5)"
	if race {
		claimable = "2 (steps 5+6, race mode)"
	}
	mapped := "(none -- pass -repo to map the demo repo)"
	if repoPath != "" {
		mapped = repoPath + " -> myapp"
	}
	fmt.Printf(`demoseed: scenes fixture seeded
  project    myapp
  plan       auth-refresh -- 4/6 done, %s claimable
  memories   9 (3 constraints + 4 hero files + rate-limit & persist-token recall gotchas)
  finding    1 (~18h old: "%s...")
  trial      1 failed (lab refresh-500s)
  repo map   %s
`, claimable, sceneFinding[:48], mapped)
}
