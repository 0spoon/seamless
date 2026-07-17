// scenes.js -- verbatim transcript data for the landing page's with/without
// terminal scenes (plan:terminal-scenes). Consumed by the scene animator.
//
// Source: real headless Claude Code (Opus 4.8) sessions run against a seeded
// throwaway Seamless instance in a fictional `myapp` repo. The with-side has the
// SessionStart briefing + UserPromptSubmit recall hooks; the without-side is
// vanilla. Same prompt, same repo, same model. Session ids are on each pane
// (`source`); raw takes are not committed.
//
// VERBATIM: agent/user text and tool labels are the model's own output, unedited.
// Curation = dropping whole steps and collapsing spans into {role:"ffwd"} markers
// (rendered as a dim "fast-forward" line); no line is ever reworded. Tool results
// are short verbatim excerpts kept only where they carry the beat.
//
// Step shapes (role):
//   user   {text}                     the typed prompt
//   inject {tag, text, focus?}        a hook injection (seam-briefing | seam-recall),
//                                      full verbatim block; focus[] = lines to emphasize
//   agent  {text}                     assistant prose, verbatim
//   tool   {label, result?}           a tool call; result = short verbatim excerpt
//   ffwd   {text}                     an editorial span summary for skipped steps
//   comment/cmd/files/fm              the files-as-truth epilogue folded into scene 1's
//                                      with-side ending (a shell view of the harvested
//                                      memory file). Illustrative like the hero, not
//                                      verbatim -- the last comment line says so on-screen.
//
// Scene shape: {id, kicker, title, prompt, layout, caption, panes:[{key,label,source,outcome,steps}]}.
// layout: "with-without" (two panes, toggle/side-by-side) | "split" (two agents, one timeline).
//
// Split-only step metadata (curation of the shared timeline; text stays verbatim):
//   beat     integer -- the shared clock. The player merges both panes' steps and
//            plays them in beat order; steps sharing a beat fire together (both
//            prompts type at once), so a claim collision reads as a race: B's
//            winning claim on beat N, A's bounce on beat N+1, right after.
//   emphasis "win" | "bounce" -- flashes a claim tool line so the collision pops.
//
// The files-as-truth beat (a with-side session ends; its lesson is a plain file on
// disk, the same store the hero shows) is folded into scene 1's with-side ending as
// comment/cmd/files/fm steps -- illustrative like the hero, not a recorded line.

window.SEAMLESS_SCENES = [
  {
    "id": "cold-start",
    "kicker": "cold start",
    "tab": "cold start",
    "title": "Continue where the last session left off",
    "prompt": "continue where we left off",
    "layout": "with-without",
    "caption": "real sessions against a seeded demo instance — transcripts verbatim, only the typing is animated",
    "panes": [
      {
        "key": "without",
        "label": "without Seamless",
        "source": "5c514c38-f6b7-4112-a7d4-0c89be07e85f",
        "outcome": "No memory to stand on. Ships a per-process limiter — the exact per-instance bug: behind a load balancer each instance only counts its own slice of traffic.",
        "steps": [
          {
            "role": "user",
            "text": "continue where we left off"
          },
          {
            "role": "ffwd",
            "text": "reads the scaffold — git log, the README, all five source files; greps for a saved memory index (there is none)"
          },
          {
            "role": "agent",
            "text": "I don't have persisted memory of the prior session (no memory index exists yet), and the working tree is clean at the initial scaffold commit. So let me go by what the code itself marks as unfinished. There's exactly one explicit pending item — the `TODO` in `auth.go:13`:\n\n> this endpoint has no rate limit... Add a limiter before the token lookup.\n\nThat's the clear \"left off\" thread. Let me confirm the build is green, then implement it."
          },
          {
            "role": "ffwd",
            "text": "writes an in-process rate limiter, wires it into the handler, adds a test, verifies 401→429 under a burst"
          },
          {
            "role": "agent",
            "text": "Confirmed working end-to-end: the first 5 requests pass the limiter (reaching the token lookup → `401` for the missing cookie), and requests 6–8 are throttled with `429`."
          }
        ]
      },
      {
        "key": "with",
        "label": "with Seamless",
        "source": "52a41f8a-0bad-4d9e-89aa-3ea7913bb9f4",
        "outcome": "The briefing hands it step 5 and the rate-limit-not-in-memory gotcha. Builds the limiter shared-storage-first (Redis-ready) and marks the in-memory store dev-only.",
        "steps": [
          {
            "role": "inject",
            "tag": "seam-briefing",
            "text": "<seam-briefing>\nSeam project: myapp -- 7 memories (2 constraints), 1 recent findings.\nCONSTRAINT: auth-cookies-httponly-secure: Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).\nCONSTRAINT: refresh-token-single-use: A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.\nPLAN: auth-refresh -- 4/6 done, 1 claimable, 0 in flight\n(claim a step with tasks_claim; attach notes/tasks to a plan with the plan:<slug> convention)\n\nMemories (myapp):\n- edge-cache-gotcha: CDN strips Vary on 304s; never cache HTML\n- deploy-runbook: Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.\n- rate-limit-not-in-memory: In-memory rate limit on the refresh endpoint resets per instance; use shared storage.\n- chroma-boot-race: Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.\n- postgres-timeouts: Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.\n\nRecent findings:\n- cc/1a2b3c4d (18h): Finished auth-refresh step 4: the auth responses now set the session and refresh cookies HttpOnly+Secure+SameSite=Lax, and the token pair no longer rides in the JSON body. Reuse-detection from step 3…\nRecall on demand with recall; read a memory with memory_read.\nSeam session: cc/52a41f8a (ambient)\n</seam-briefing>",
            "focus": [
              "PLAN: auth-refresh -- 4/6 done, 1 claimable, 0 in flight",
              "rate-limit-not-in-memory",
              "cc/1a2b3c4d (18h): Finished auth-refresh step 4"
            ]
          },
          {
            "role": "user",
            "text": "continue where we left off"
          },
          {
            "role": "agent",
            "text": "I'll check where we left off on the auth-refresh plan."
          },
          {
            "role": "tool",
            "label": "tasks_ready",
            "result": "ready: Rate-limit POST /auth/refresh (per-IP and per-family)"
          },
          {
            "role": "agent",
            "text": "The claimable step is **\"Rate-limit POST /auth/refresh (per-IP and per-family)\"**. Let me read the plan note for acceptance criteria and check the relevant code."
          },
          {
            "role": "agent",
            "text": "Let me claim the step, read the plan note, and check the relevant memory + code in parallel."
          },
          {
            "role": "tool",
            "label": "tasks_claim 01KXMMEFQZ...",
            "result": "claimed"
          },
          {
            "role": "tool",
            "label": "memory_read rate-limit-not-in-memory",
            "result": "A rate limiter kept in a process map only sees one instance's traffic. myapp runs several instances behind the load balancer, so an attacker's request …"
          },
          {
            "role": "ffwd",
            "text": "reads the code — no shared storage is wired in — then writes the limiter behind a storage-agnostic interface"
          },
          {
            "role": "agent",
            "text": "Now wire the store in `main`, with a loud warning that the in-memory store is single-instance only:"
          },
          {
            "role": "ffwd",
            "text": "adds tests; go vet + build + test all green; records the finding on the plan"
          },
          {
            "role": "agent",
            "text": "Done. Step 5 of the **auth-refresh** plan is complete and committed to the plan's task/note trail. The plan is now 5/6, and the final step is unblocked."
          },
          {
            "role": "comment",
            "text": "# the session ended. the SQLite index could vanish — this would not."
          },
          {
            "role": "cmd",
            "text": "ls -t memory/myapp/"
          },
          {
            "role": "files",
            "files": [
              { "name": "rate-limit-shared-storage.md", "tag": "just now" },
              { "name": "rate-limit-not-in-memory.md" },
              { "name": "edge-cache-gotcha.md" },
              { "name": "refresh-token-single-use.md" },
              { "name": "persist-refresh-tokens.md" },
              { "name": "auth-cookies-samesite-lax.md" },
              { "name": "deploy-runbook.md" }
            ]
          },
          {
            "role": "cmd",
            "text": "head -4 memory/myapp/rate-limit-shared-storage.md"
          },
          { "role": "fm", "v": "---" },
          { "role": "fm", "k": "kind:", "v": "gotcha" },
          { "role": "fm", "k": "description:", "v": "Rate limiter must use shared storage, not a per-process map — each instance counts only its own traffic" },
          { "role": "fm", "k": "source_session:", "v": "cc/52a41f8a" },
          {
            "role": "comment",
            "text": "# what it learned is a file — illustrative, like the hero, not a line from the recorded take."
          }
        ]
      }
    ]
  },
  {
    "id": "hardening-trap",
    "kicker": "a hardening trap",
    "tab": "hardening trap",
    "title": "“The scanner says tighten the auth cookies”",
    "prompt": "our security scanner is failing CI on the auth cookies -- it flags SameSite=Lax as too weak and wants SameSite=Strict. make the change so the build passes.",
    "layout": "with-without",
    "caption": "real sessions against a seeded demo instance — transcripts verbatim, only the typing is animated",
    "panes": [
      {
        "key": "without",
        "label": "without Seamless",
        "source": "f1671577-2c2f-47e0-b6ec-bf69d4a61bd8",
        "outcome": "Names the external-link bounce in the abstract — then flips Lax→Strict anyway because CI demands it. Ships the regression the constraint was written to prevent.",
        "steps": [
          {
            "role": "user",
            "text": "our security scanner is failing CI on the auth cookies -- it flags SameSite=Lax as too weak and wants SameSite=Strict. make the change so the build passes."
          },
          {
            "role": "ffwd",
            "text": "greps for SameSite, reads auth.go"
          },
          {
            "role": "tool",
            "label": "Edit auth.go",
            "result": "SameSite: Lax → Strict"
          },
          {
            "role": "agent",
            "text": "Done. Changed `SameSite` to `http.SameSiteStrictMode` in `setAuthCookie` (auth.go:44) and updated the comment to match.\n\nThe scanner will now pass. One thing worth flagging so it doesn't surprise you: **`SameSite=Strict` has a real behavioral consequence.** With Strict, the browser won't send these cookies on any cross-site navigation — including a user *following a link into your app from an external site* (email, Slack, another domain). The first request after such a navigation arrives with no session/refresh cookie, so the user appears logged out until they land on a same-site request."
          }
        ]
      },
      {
        "key": "with",
        "label": "with Seamless",
        "source": "da6a9772-99c4-4219-aa45-4db542df3c7c",
        "outcome": "The briefing's pinned constraint says the opposite. Refuses the one-line change, cites the past incident, and hardens elsewhere instead.",
        "steps": [
          {
            "role": "inject",
            "tag": "seam-briefing",
            "text": "<seam-briefing>\nSeam project: myapp -- 9 memories (3 constraints), 1 recent findings.\nCONSTRAINT: auth-cookies-samesite-lax: Auth cookies must stay SameSite=Lax, not Strict: Strict logs out users arriving from an external link. Harden elsewhere (__Host-, TTL).\nCONSTRAINT: auth-cookies-httponly-secure: Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).\nCONSTRAINT: refresh-token-single-use: A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.\nPLAN: auth-refresh -- 4/6 done, 1 claimable, 0 in flight\n(claim a step with tasks_claim; attach notes/tasks to a plan with the plan:<slug> convention)\n\nMemories (myapp):\n- edge-cache-gotcha: CDN strips Vary on 304s; never cache HTML\n- deploy-runbook: Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.\n- rate-limit-not-in-memory: In-memory rate limit on the refresh endpoint resets per instance; use shared storage.\n- persist-refresh-tokens: Persist refresh tokens to the database the safe way: one hard rule about what the token column may store.\n- chroma-boot-race: Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.\n- postgres-timeouts: Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.\n\nRecent findings:\n- cc/1a2b3c4d (18h): Finished auth-refresh step 4: the auth responses now set the session and refresh cookies HttpOnly+Secure+SameSite=Lax, and the token pair no longer rides in the JSON body. Reuse-detection from step 3…\nRecall on demand with recall; read a memory with memory_read.\nSeam session: cc/da6a9772 (ambient)\n</seam-briefing>",
            "focus": [
              "CONSTRAINT: auth-cookies-samesite-lax: Auth cookies must stay SameSite=Lax, not Strict"
            ]
          },
          {
            "role": "user",
            "text": "our security scanner is failing CI on the auth cookies -- it flags SameSite=Lax as too weak and wants SameSite=Strict. make the change so the build passes."
          },
          {
            "role": "agent",
            "text": "I need to flag something before making this change. There's a recorded project constraint that directly conflicts with it.\n\nLet me look at the actual cookie code first."
          },
          {
            "role": "ffwd",
            "text": "greps for SameSite, reads auth.go"
          },
          {
            "role": "agent",
            "text": "Before I make this change, I need to flag a conflict — because this one will bite real users.\n\n## The problem\n\nThere's a **recorded project constraint** that says exactly the opposite of what the scanner wants:\n\n> **`auth-cookies-samesite-lax`**: Auth cookies must stay SameSite=Lax, not Strict — Strict logs out users arriving from an external link. Harden elsewhere (`__Host-`, TTL).\n\nThe code today (`auth.go:44`) is `SameSiteLaxMode`, and that's deliberate. If I flip it to `SameSiteStrictMode`:\n\n- A user who clicks a link to your app from email, Slack, another site, etc. arrives with **no cookies sent** on that first top-level navigation (that's what Strict does).\n- Result: they look logged out and get bounced to login, even with a valid session. This is precisely the regression the constraint was written to prevent.\n\nSo the one-line change makes CI green but reintroduces a known, user-facing breakage. I don't want to silently do that.\n\n## What I'd recommend instead\n\nSatisfy the scanner's actual intent (cookie hardening) without Strict:\n\n1. **`__Host-` prefix** on the cookie names — forces Secure, Path=/, host-locked, no Domain. Many scanners accept this as strong-cookie compliance.\n2. **Short TTL + `MaxAge`** on the access cookie, leaning on refresh-token rotation (which you already have).\n\nBut — and this matters — if your scanner is doing a **literal string check for `SameSite=Strict`**, then `__Host-` won't clear it, and there's a genuine tradeoff to make. In that case the real options are:\n\n- **Suppress/waive the rule** for the auth cookies with a documented exception referencing the constraint (best if the scanner supports inline waivers/allowlists).\n- **Split the difference**: keep the *session/access* cookie on Lax (that's the one external links need), and this doesn't fully help since the scanner flags both.\n- Accept the Strict change and the logout regression (I'd advise against).\n\n## What I need from you\n\nWhich do you want?\n\n- **(A)** Harden with `__Host-` + TTL and configure a scanner waiver for the SameSite rule — keeps Lax, keeps external links working. *(my recommendation)*\n- **(B)** You confirm you want Strict anyway, knowing external-link arrivals get logged out — I'll make the one-liner.\n\nTell me which scanner it is (and whether it supports inline suppressions) and I'll wire up (A) concretely."
          }
        ]
      }
    ]
  },
  {
    "id": "coordination",
    "kicker": "two agents, one queue",
    "tab": "two agents",
    "title": "Two agents pick up the same plan",
    "prompt": "pick up the next step of the plan",
    "layout": "split",
    "caption": "two live agents against one seeded instance, same prompt — transcripts verbatim, one shared timeline",
    "panes": [
      {
        "key": "B",
        "label": "agent B",
        "source": "b541a655-fbe7-4280-9d53-c6c627d4cba5",
        "outcome": "Claims the rate-limit step first. Its lease is what makes the other agent's claim bounce.",
        "steps": [
          {
            "role": "user",
            "beat": 0,
            "text": "pick up the next step of the plan"
          },
          {
            "role": "tool",
            "beat": 1,
            "label": "tasks_ready",
            "result": "2 steps ready"
          },
          {
            "role": "agent",
            "beat": 2,
            "text": "Both remaining steps are unblocked. I'll take the rate-limiting step next (step 5) — it's the natural continuation of the token/reuse work, and there's a directly relevant project constraint (`rate-limit-not-in-memory`) to honor. Let me claim it and look at the code."
          },
          {
            "role": "tool",
            "beat": 3,
            "emphasis": "win",
            "label": "tasks_claim 01KXMRCX...",
            "result": "claimed_by \"01KXP8FEJQ6R5Z5NMVZ7082X5A\""
          },
          {
            "role": "ffwd",
            "beat": 7,
            "text": "builds the per-IP + per-family limiter, reconciling live with the other agent's edits to the same files"
          },
          {
            "role": "agent",
            "beat": 8,
            "text": "Done. Step 5 of the **auth-refresh** plan — *Rate-limit POST /auth/refresh (per-IP and per-family)* — is complete, and the plan is now 5/6."
          }
        ]
      },
      {
        "key": "A",
        "label": "agent A",
        "source": "85a94e88-75d6-4345-9b1a-0243870da2c5",
        "outcome": "Reaches for the same step — the queue says it is already held. Pivots to the metrics step without being told.",
        "steps": [
          {
            "role": "user",
            "beat": 0,
            "text": "pick up the next step of the plan"
          },
          {
            "role": "tool",
            "beat": 1,
            "label": "tasks_ready",
            "result": "2 steps ready"
          },
          {
            "role": "agent",
            "beat": 2,
            "text": "I have the full picture. The next step is **\"Rate-limit POST /auth/refresh (per-IP and per-family)\"** — the older of the two claimable steps, and there's a clear `TODO` in `auth.go:13-14` plus the `rate-limit-not-in-memory` constraint. Let me claim it."
          },
          {
            "role": "tool",
            "beat": 4,
            "emphasis": "bounce",
            "label": "tasks_claim 01KXMRCX...",
            "result": "task already claimed: held by \"01KXP8FEJQ6R5Z5NMVZ7082X5A\""
          },
          {
            "role": "agent",
            "beat": 5,
            "text": "The rate-limit step is already being worked by another session. That leaves the other claimable step — **\"Emit metrics and an alert for refresh-reuse revocations.\"** Let me claim that one."
          },
          {
            "role": "tool",
            "beat": 6,
            "label": "tasks_claim 01KXN64BN3...",
            "result": "claimed"
          },
          {
            "role": "ffwd",
            "beat": 7,
            "text": "implements the metrics + alert on the reuse path; finds and fixes a real reuse-detection bug along the way"
          },
          {
            "role": "agent",
            "beat": 8,
            "text": "Done. Picked up and completed the plan's next available step."
          }
        ]
      }
    ]
  },
  {
    "id": "token-safety",
    "kicker": "a danger the code can't reveal",
    "tab": "token safety",
    "title": "Persist the refresh tokens",
    "prompt": "persist the refresh tokens to the database so sessions survive a restart",
    "layout": "with-without",
    "caption": "real sessions against a seeded demo instance — transcripts verbatim, only the typing is animated",
    "panes": [
      {
        "key": "without",
        "label": "without Seamless",
        "source": "67f82d0c-58b0-46b4-b212-dc3b8cd36191",
        "outcome": "Mirrors the in-memory maps straight into SQL — a raw-refresh-token column. A DB snapshot is now every live session, in plaintext.",
        "steps": [
          {
            "role": "user",
            "text": "persist the refresh tokens to the database so sessions survive a restart"
          },
          {
            "role": "ffwd",
            "text": "explores the token store, confirms the pure-Go SQLite driver is available"
          },
          {
            "role": "agent",
            "text": "Now the store, translated 1:1 from the maps to the tables:"
          },
          {
            "role": "ffwd",
            "text": "writes db.go, ports the token store to SQL, wires it through, tests restart-survival — all green"
          },
          {
            "role": "agent",
            "text": "Done. Refresh-token families now persist to SQLite, so sessions survive a restart."
          }
        ]
      },
      {
        "key": "with",
        "label": "with Seamless",
        "source": "5b64a7b0-f57f-428e-95f6-1ef68914f400",
        "outcome": "Sees persist-refresh-tokens in the briefing, reads it at the moment of persisting, and stores only a SHA-256 hash. A stolen DB snapshot is useless.",
        "steps": [
          {
            "role": "inject",
            "tag": "seam-briefing",
            "text": "<seam-briefing>\nSeam project: myapp -- 9 memories (3 constraints), 1 recent findings.\nCONSTRAINT: auth-cookies-samesite-lax: Auth cookies must stay SameSite=Lax, not Strict: Strict logs out users arriving from an external link. Harden elsewhere (__Host-, TTL).\nCONSTRAINT: auth-cookies-httponly-secure: Access and refresh tokens ride in HttpOnly, Secure, SameSite=Lax cookies -- never in localStorage or a JSON body (XSS reads both).\nCONSTRAINT: refresh-token-single-use: A refresh token is single-use: rotate it on every /auth/refresh, and if an old one is replayed, revoke the whole family.\nPLAN: auth-refresh -- 4/6 done, 1 claimable, 0 in flight\n(claim a step with tasks_claim; attach notes/tasks to a plan with the plan:<slug> convention)\n\nMemories (myapp):\n- edge-cache-gotcha: CDN strips Vary on 304s; never cache HTML\n- deploy-runbook: Deploy myapp: build the binary, run migrations, wait for /healthz, then flip the load balancer.\n- rate-limit-not-in-memory: In-memory rate limit on the refresh endpoint resets per instance; use shared storage.\n- persist-refresh-tokens: Persist refresh tokens to the database the safe way: one hard rule about what the token column may store.\n- chroma-boot-race: Chroma isn't ready when the API boots; retry the first query with backoff instead of crashing the process.\n- postgres-timeouts: Postgres statement_timeout kills long analytics queries; run them on the read replica with a raised timeout.\n\nRecent findings:\n- cc/1a2b3c4d (18h): Finished auth-refresh step 4: the auth responses now set the session and refresh cookies HttpOnly+Secure+SameSite=Lax, and the token pair no longer rides in the JSON body. Reuse-detection from step 3…\nRecall on demand with recall; read a memory with memory_read.\nSeam session: cc/5b64a7b0 (ambient)\n</seam-briefing>",
            "focus": [
              "persist-refresh-tokens: Persist refresh tokens to the database the safe way"
            ]
          },
          {
            "role": "user",
            "text": "persist the refresh tokens to the database so sessions survive a restart"
          },
          {
            "role": "agent",
            "text": "I'll start by loading the relevant memory and the plan state, and exploring the auth code."
          },
          {
            "role": "agent",
            "text": "Now let me read the key memory about persisting refresh tokens and check the plan step."
          },
          {
            "role": "tool",
            "label": "memory_read persist-refresh-tokens",
            "result": "When you persist refresh tokens, store only a SHA-256 hash of each token, never the raw value; on rotate, hash the presented token and look it up by h …"
          },
          {
            "role": "agent",
            "text": "Builds clean with zero deps. The memory gives a hard rule: **store only a SHA-256 hash of each refresh token, never the raw value**. Let me check whether I can pull a Postgres driver (the project's DB is Postgres per the other memories)."
          },
          {
            "role": "ffwd",
            "text": "writes the migration and the Postgres-backed store — hashing every token — wires it through, adds a test that asserts no raw token is ever persisted"
          },
          {
            "role": "agent",
            "text": "Done. Refresh tokens now persist to Postgres so live sessions survive a restart (and are shared across instances instead of living in per-process memory)."
          }
        ]
      }
    ]
  }
];
