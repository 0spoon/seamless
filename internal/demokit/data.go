// Console-fleet fixture data: the invented six-week history that demoseed's
// default (SeedConsoleFleet) mode seeds so the console can be screenshotted for
// the landing page. All content below is fictional -- a solo dev's agent fleet
// on a Go SaaS backend (orbital), its frontend (orbital-web), a homelab, and
// dotfiles. Not part of the shipped product; never point demoseed at a live dir.

package demokit

// memSpec is one memory to seed. hot ranks injection popularity: 0 = never
// surfaced (cold tail), higher = injected more often. desc <= 150 chars.
type memSpec struct {
	project string
	kind    string
	name    string
	desc    string
	body    string // "" = derived from desc
	hot     int
}

var memSpecs = []memSpec{
	// ---------------- orbital: gotchas (13) ----------------
	{"orbital", "gotcha", "stripe-webhook-replay-window", "Stripe re-sends webhooks for 3 days: dedupe on provider event id, not idempotency key -- the key rotates on retry.", "Confirmed in the 07-08 storm: retries arrive with a fresh idempotency key, so a key-based dedupe passes every replay through. The provider event id is the only stable handle across the full 3-day replay window.\n\nSee [[payments-idempotent-by-event-id]] and the webhook-retry-storm lab.", 9},
	{"orbital", "gotcha", "webhook-sig-header-lowercase", "Behind the LB, X-Signature arrives lowercased; verify case-insensitively or replays pass and first deliveries fail.", "The load balancer normalizes header casing. Direct traffic kept X-Signature intact, which is why local repro kept passing. Root cause of the 07-08 alert noise; found via the [[stripe-webhook-replay-window]] investigation.", 8},
	{"orbital", "gotcha", "edge-cache-vary-cookie", "The edge cache treats Vary: Cookie as uncacheable -- session cookie on static assets nuked the hit rate to 4%.", "Any response carrying Vary: Cookie is a guaranteed miss at the edge. The session middleware was stamping the cookie on every route including /static. Split static onto a cookieless path; hit rate went 4% -> 71%.", 8},
	{"orbital", "gotcha", "cdn-cookie-cache-miss", "CDN misses on every asset when the session cookie rides along; static routes must stay cookieless.", "", 3},
	{"orbital", "gotcha", "pgx-pool-exhaustion-on-listen", "pgxpool: a LISTEN connection held past checkout starves the pool under burst; dedicate a conn outside the pool.", "", 6},
	{"orbital", "gotcha", "clock-skew-jwt-iat", "API nodes drift up to 90s; JWT iat validation must allow 2m skew or logins fail only on node 3.", "", 4},
	{"orbital", "gotcha", "outbox-poll-vs-notify-latency", "Outbox dispatch on NOTIFY alone stalls after conn resets; keep the 5s poll as the safety net.", "", 5},
	{"orbital", "gotcha", "replay-dedupe-event-id-unique-index", "Dedupe store needs the UNIQUE index on provider_event_id, not app id -- burst inserts race otherwise.", "", 7},
	{"orbital", "gotcha", "invoice-finalize-burst-shape", "Invoice finalization fires webhook bursts of 40-60x baseline for ~4 min at billing-cycle boundaries.", "", 4},
	{"orbital", "gotcha", "sqlite-counter-checkpoint-stall", "Rate-counter SQLite WAL checkpoints stall p99 under sustained writes; truncate on the idle tick instead.", "", 3},
	{"orbital", "gotcha", "grpc-keepalive-lb-idle-reset", "The LB silently drops idle gRPC streams at 350s; keepalive must ping under that or reconnect storms follow.", "", 2},
	{"orbital", "gotcha", "migration-lock-timeout-default", "goose migrations default to no lock_timeout: a long DDL behind traffic wedges the deploy. Set 5s + retry.", "", 0},
	{"orbital", "gotcha", "adyen-notification-order", "Adyen notifications arrive out of order under load; AUTHORISATION after CAPTURE is normal, not an error.", "", 0},

	// ---------------- orbital: decisions (9) ----------------
	{"orbital", "decision", "events-outbox-over-cdc", "Chose transactional outbox over Debezium CDC: one fewer moving part, same at-least-once guarantee.", "Debezium would add Kafka Connect + schema registry to the footprint. The outbox table with a poll/notify dispatcher gives the same at-least-once delivery inside the existing Postgres. Revisit only if fan-out consumers multiply.", 7},
	{"orbital", "decision", "webhook-replay-dedupe-store", "Dedupe store is a Postgres table keyed on provider event id with 4-day TTL -- outlives the 3-day replay window.", "Sized from the 07-08 storm: 4-day TTL covers Stripe's replay window with a day of slack. Partitioned by day for cheap TTL drops. See [[stripe-webhook-replay-window]] and [[replay-dedupe-event-id-unique-index]].", 8},
	{"orbital", "decision", "sqlite-for-rate-counters", "Rate-limit counters live in per-node SQLite, not Redis: bounded error accepted for zero new infra.", "", 5},
	{"orbital", "decision", "api-versioning-header-not-path", "Version via Accept header, not /v2 paths: proxies keep one route table, clients migrate per-endpoint.", "", 4},
	{"orbital", "decision", "payments-provider-abstraction-thin", "Provider layer stays a thin port: normalize events + ids only, no generic payment object -- leaky abstractions cost more.", "", 3},
	{"orbital", "decision", "dedupe-store-schema-decision", "Dedupe rows carry (provider, event_id, first_seen, delivery_count); no payload copy -- the outbox already has it.", "", 6},
	{"orbital", "decision", "no-retry-on-4xx-webhook-push", "Outbound webhook pushes never retry 4xx (only 5xx/timeouts): a consumer bug must not become our queue backlog.", "", 2},
	{"orbital", "decision", "read-replica-for-reporting-only", "Reporting queries pinned to the replica; app reads stay on primary -- replica lag broke idempotency checks once.", "", 3},
	{"orbital", "decision", "go-1-25-min-toolchain", "Repo pins Go 1.25 as minimum: range-over-func iterators used in the outbox dispatcher.", "", 0},

	// ---------------- orbital: runbooks (6) ----------------
	{"orbital", "runbook", "rotate-webhook-signing-keys", "Rotate signing keys: add new key to ring, deploy, flip primary, keep old 72h (replay window), drop.", "1. Add the new key to the ring (secondary slot) and deploy.\n2. Flip primary once every node reports the new ring.\n3. Keep the old key verifying for 72h -- Stripe replays up to 3 days.\n4. Drop the old key; confirm zero sig-mismatch alerts for 24h.", 7},
	{"orbital", "runbook", "restore-orbital-db-from-pitr", "PITR restore: pick LSN from incident channel, restore to fork, run smoke suite, cut DNS, backfill outbox.", "", 2},
	{"orbital", "runbook", "dedupe-backfill-runbook", "Backfill the dedupe store from the outbox after an outage: replay 4 days of event ids, idempotent upsert.", "", 5},
	{"orbital", "runbook", "drain-node-for-maintenance", "Drain a node: cordon at LB, wait for in-flight webhooks (max 90s), stop dispatcher, then the service.", "", 3},
	{"orbital", "runbook", "unstick-outbox-dispatcher", "Dispatcher stuck: check advisory lock holder, verify NOTIFY channel, bounce with --resume-from last acked id.", "", 4},
	{"orbital", "runbook", "billing-cycle-preflight", "Before the monthly cycle: verify dedupe TTL headroom, raise ingest worker cap to 4x, silence the burst alert.", "", 2},

	// ---------------- orbital: constraints (8) ----------------
	{"orbital", "constraint", "payments-idempotent-by-event-id", "Every payment mutation must be idempotent on provider event id -- retries are the normal path, not the edge.", "", 9},
	{"orbital", "constraint", "no-pii-in-logs", "Log scrubber only covers known fields: never log raw request bodies on payment routes.", "", 6},
	{"orbital", "constraint", "webhook-ack-under-5s", "Webhook handlers must ack < 5s (provider timeout): enqueue and return, never process inline.", "", 7},
	{"orbital", "constraint", "outbox-writes-same-tx", "Outbox rows are written in the SAME transaction as the domain change -- a separate tx reintroduces dual-write loss.", "", 6},
	{"orbital", "constraint", "money-as-integer-minor-units", "All money is integer minor units + currency code. No floats anywhere between ingest and ledger.", "", 4},
	{"orbital", "constraint", "api-breaking-change-90d-notice", "Public API breaking changes need 90 days notice + a deprecation header on every affected response.", "", 2},
	{"orbital", "constraint", "single-writer-per-ledger-account", "Ledger writes serialize per account (advisory lock): concurrent postings to one account are a bug upstream.", "", 3},
	{"orbital", "constraint", "prod-migrations-are-forward-only", "Production migrations are forward-only; a rollback is a new forward migration. Down files exist for dev only.", "", 2},

	// ---------------- orbital: references (6) ----------------
	{"orbital", "reference", "stripe-webhook-docs", "https://stripe.com/docs/webhooks -- retry schedule, signature scheme, replay windows.", "", 4},
	{"orbital", "reference", "pgx-v5-pool-tuning", "pgx v5 pool knobs that actually matter for us: MaxConns, HealthCheckPeriod, and the LISTEN caveat.", "", 3},
	{"orbital", "reference", "gcra-rate-limiting-paper", "GCRA (generic cell rate algorithm) writeup -- the model behind the limiter; O(1) memory per key.", "", 2},
	{"orbital", "reference", "adyen-notification-webhooks", "Adyen notification webhook reference: HMAC scheme, ordering caveats, test-card event zoo.", "", 0},
	{"orbital", "reference", "postgres-16-release-notes", "PG16 notes relevant to us: logical replication perf, REINDEX CONCURRENTLY fixes, pg_stat_io.", "", 2},
	{"orbital", "reference", "goose-migration-tool", "goose migration conventions: naming, embedded FS usage, the lock_timeout wrapper we add.", "", 0},

	// ---------------- orbital: protocols (5) ----------------
	{"orbital", "protocol", "incident-sev2-checklist", "SEV2: page owner, freeze deploys, open incident doc from template, timeline every 15m.", "", 5},
	{"orbital", "protocol", "deploy-canary-then-fleet", "Deploys go canary (1 node, 15m soak, error budget check) then fleet. No direct-to-fleet, including hotfixes.", "", 4},
	{"orbital", "protocol", "schema-change-review-pair", "Any migration touching payments tables needs a second reviewer and an EXPLAIN on the biggest table copy.", "", 2},
	{"orbital", "protocol", "postmortem-within-72h", "Postmortems land within 72h while the timeline is fresh; blameless, action items get owners + dates.", "", 3},
	{"orbital", "protocol", "webhook-consumer-onboarding", "New webhook consumers: shared-secret exchange, staging soak with replay traffic, then prod allowlist.", "", 0},

	// ---------------- orbital: stages (4) ----------------
	{"orbital", "stage", "billing-webhooks-v2-replay-landed", "Replay protection + dedupe store landed; load test at 50x burst is the remaining step.", "", 6},
	{"orbital", "stage", "postgres-16-migration-complete", "PG16 migration complete 2026-07-02: logical replica promoted, old primary retired after 48h soak.", "", 3},
	{"orbital", "stage", "outbox-dispatcher-v2-canary", "Dispatcher v2 (batched NOTIFY + poll fallback) on canary since 07-11; fleet rollout after the billing cycle.", "", 4},
	{"orbital", "stage", "rate-limiting-plan-presented", "API rate-limiting plan captured and presented; awaiting owner approval before steps are cut.", "", 3},

	// ---------------- orbital: refuted (2) ----------------
	{"orbital", "refuted", "redis-needed-for-rate-limits", "REFUTED: assumed Redis was required for cross-node rate limits; per-node SQLite with bounded error won.", "", 0},
	{"orbital", "refuted", "webhook-storm-was-provider-bug", "REFUTED: 07-08 storm blamed on Stripe; actual cause was our LB lowercasing the signature header.", "", 2},

	// ---------------- orbital-web: gotchas (8) ----------------
	{"orbital-web", "gotcha", "next-image-cdn-loop", "next/image + our CDN rewrite loops on already-optimized URLs; exclude /static/opt/ from the loader.", "", 6},
	{"orbital-web", "gotcha", "safari-datetime-local-utc", "Safari parses datetime-local as UTC not local; round-trip through epoch ms at the form boundary.", "", 4},
	{"orbital-web", "gotcha", "tokens-css-vars-flash", "Theme tokens as CSS vars: setting them in a client effect flashes default theme on first paint -- inline script in head.", "", 7},
	{"orbital-web", "gotcha", "rsc-fetch-dedupe-cache-scope", "RSC fetch dedupe is per-request, not per-render-pass: parallel routes double-fetch unless the call sites share the exact URL.", "", 4},
	{"orbital-web", "gotcha", "playwright-webserver-port-race", "Playwright webServer reuseExistingServer races the dev server port on CI; pin a per-worker port range.", "", 3},
	{"orbital-web", "gotcha", "storybook-vite-alias-drift", "Storybook's vite config does not inherit tsconfig paths; alias drift breaks stories only, silently.", "", 2},
	{"orbital-web", "gotcha", "focus-trap-portal-modal", "Focus trap breaks when the modal renders in a portal after a suspense boundary; trap must mount post-resolve.", "", 2},
	{"orbital-web", "gotcha", "intl-numberformat-node-icu", "CI node lacks full ICU: Intl.NumberFormat currency tests pass locally, fail on CI. Use --with-intl or polyfill.", "", 0},

	// ---------------- orbital-web: decisions (6) ----------------
	{"orbital-web", "decision", "playwright-over-cypress", "Playwright over Cypress: parallel workers cut e2e from 14m to 4m; trace viewer beats video for flakes.", "", 5},
	{"orbital-web", "decision", "design-tokens-single-source", "Design tokens live in tokens.json, generated to CSS vars + TS consts -- hand-edited CSS vars drift.", "", 6},
	{"orbital-web", "decision", "tokens-ts-consts-pattern", "Generated TS consts are the only token import surface for components; raw var() strings are lint-banned.", "", 5},
	{"orbital-web", "decision", "rsc-boundary-at-route-level", "Server/client component boundary sits at route segments, not leaf components: fewer islands, saner props.", "", 3},
	{"orbital-web", "decision", "no-css-in-js-runtime", "No runtime CSS-in-JS: tokens + CSS modules only. Style computation stays off the render path.", "", 3},
	{"orbital-web", "decision", "visual-diff-over-snapshot-dom", "Visual diffs (screenshot) over DOM snapshots for components: DOM snapshots rot on refactors that change nothing visible.", "", 2},

	// ---------------- orbital-web: runbooks (3) ----------------
	{"orbital-web", "runbook", "purge-cdn-after-token-change", "Token change: regen, visual-diff storybook, purge CDN by tag (not full), verify hashed asset names moved.", "", 4},
	{"orbital-web", "runbook", "refresh-visual-baselines", "Refresh visual baselines: land the intended change, run baseline job on main, review diff gallery, commit.", "", 2},
	{"orbital-web", "runbook", "bisect-bundle-regression", "Bundle regression: build with ANALYZE=1 on the two commits, diff stats.json, check for duplicate pkg versions first.", "", 3},

	// ---------------- orbital-web: constraints (6) ----------------
	{"orbital-web", "constraint", "bundle-budget-180kb", "First-load JS budget 180KB gz per route: CI fails the PR, no override label.", "", 6},
	{"orbital-web", "constraint", "tokens-generated-never-edited", "Generated token files (CSS vars + TS consts) are never hand-edited; tokens.json is the only source.", "", 5},
	{"orbital-web", "constraint", "a11y-keyboard-first", "Every interactive component ships keyboard-first: no pointer-only affordances, focus visible always.", "", 3},
	{"orbital-web", "constraint", "no-client-fetch-for-initial-data", "Initial route data comes from the server render; client fetch-on-mount for first paint is banned.", "", 2},
	{"orbital-web", "constraint", "images-through-cdn-loader", "All images go through the CDN loader with explicit sizes; raw <img> fails lint outside email templates.", "", 2},
	{"orbital-web", "constraint", "e2e-green-before-merge", "e2e suite must be green before merge -- flaky reruns allowed once, then the test gets quarantined WITH an owner.", "", 3},

	// ---------------- orbital-web: references (4) ----------------
	{"orbital-web", "reference", "nextjs-app-router-caching", "App router caching layers cheat sheet: fetch cache, route cache, router cache, and what invalidates each.", "", 3},
	{"orbital-web", "reference", "tokens-json-schema", "tokens.json schema + the generator flags; the census script that lists consumers per token.", "", 2},
	{"orbital-web", "reference", "storybook-6-migration-notes", "Old Storybook 6 migration notes -- kept for the addon compatibility table.", "", 0},
	{"orbital-web", "reference", "web-vitals-field-data", "Field data dashboard for LCP/INP by route; the query behind the weekly vitals snapshot.", "", 2},

	// ---------------- orbital-web: protocols (2) ----------------
	{"orbital-web", "protocol", "component-api-review", "New shared components get an API review: props sketch in a note, two consumers signed up, then build.", "", 2},
	{"orbital-web", "protocol", "release-notes-user-facing", "User-facing changes need a release-note line written WITH the PR, not reconstructed at release time.", "", 0},

	// ---------------- orbital-web: stage (1) ----------------
	{"orbital-web", "stage", "design-tokens-refresh-generate-done", "Token generator landed: tokens.json -> CSS vars + TS consts. Component migration is the last step.", "", 4},

	// ---------------- homelab: gotchas (6) ----------------
	{"homelab", "gotcha", "proxmox-backup-vzdump-lock", "vzdump holds a guest lock: nightly backup + auto-update collide and the VM stays locked until manual unlock.", "", 4},
	{"homelab", "gotcha", "caddy-internal-tls-mdns", "Caddy internal CA + mDNS: .local hosts get cert warnings on iOS only; pin the CA profile via config profile.", "", 3},
	{"homelab", "gotcha", "pbs-gc-needs-quorum-datastore", "PBS garbage collection silently no-ops when the datastore is above 90% -- prune first, then GC, or nothing shrinks.", "", 3},
	{"homelab", "gotcha", "zfs-scrub-io-starves-vms", "Weekly zfs scrub starves VM IO on the spinning pool; cap scrub speed or schedule against the backup window.", "", 2},
	{"homelab", "gotcha", "wireguard-mtu-pppoe", "WireGuard over the PPPoE uplink needs MTU 1412; default 1420 works until a full-size packet stalls the tunnel.", "", 2},
	{"homelab", "gotcha", "usb-passthrough-resets-on-migrate", "USB passthrough (zigbee stick) does not survive live migration; pin the VM or re-attach post-move.", "", 0},

	// ---------------- homelab: decisions (4) ----------------
	{"homelab", "decision", "wireguard-over-tailscale", "Plain WireGuard over Tailscale for the lab: one config file, no external coordination plane.", "", 3},
	{"homelab", "decision", "pbs-over-rsync-snapshots", "PBS over rsync snapshots: dedup + verify beats hand-rolled hardlink trees; restore drill is the real test.", "", 2},
	{"homelab", "decision", "caddy-one-block-per-service", "Caddyfile: one site block per service, no wildcard mega-block -- diffable, and a bad block fails alone.", "", 2},
	{"homelab", "decision", "vlan-per-trust-tier", "Three VLANs by trust tier (mgmt / services / iot), not per-device: firewall rules stay reviewable.", "", 2},

	// ---------------- homelab: runbooks (5) ----------------
	{"homelab", "runbook", "restore-drill-quarterly", "Quarterly: restore latest PBS snapshot of vault VM to spare node, boot isolated VLAN, verify secrets round-trip.", "", 4},
	{"homelab", "runbook", "rotate-vault-vm-cert", "Vault cert rotation: issue from internal CA, hot-reload, verify agent auto-auth, then revoke old serial.", "", 3},
	{"homelab", "runbook", "proxmox-node-upgrade", "Node upgrade: migrate VMs off, snapshot boot disk, apt dist-upgrade, verify cluster quorum, migrate back.", "", 2},
	{"homelab", "runbook", "recover-locked-vm-after-vzdump", "Unlock a vzdump-wedged VM: qm unlock <id>, verify no backup task alive first (kill orphan worker if so).", "", 2},
	{"homelab", "runbook", "ups-graceful-shutdown-chain", "On UPS low battery: NUT triggers vault VM save, then storage VM, then nodes -- verify order after any rename.", "", 0},

	// ---------------- homelab: constraints (2) ----------------
	{"homelab", "constraint", "vault-vm-never-snapshots-live", "The vault VM is never live-snapshotted (memory captures secrets); backups are stop-mode only.", "", 3},
	{"homelab", "constraint", "iot-vlan-no-egress", "IoT VLAN has no WAN egress except NTP + the two allowlisted update hosts. New devices get nothing by default.", "", 2},

	// ---------------- homelab: references (3) ----------------
	{"homelab", "reference", "pbs-retention-docs", "Proxmox Backup Server retention math -- keep-daily/weekly/monthly interplay.", "", 2},
	{"homelab", "reference", "nut-ups-config", "NUT config for the rack UPS: driver quirks, the shutdown chain, and the calibration numbers.", "", 0},
	{"homelab", "reference", "zfs-tuning-spinning-pool", "ZFS knobs for the spinning pool: recordsize per dataset, scrub throttle, ARC cap on the 32GB node.", "", 0},

	// ---------------- homelab: protocols (2) ----------------
	{"homelab", "protocol", "change-window-sunday-morning", "Lab changes land Sunday mornings: household traffic is lowest and a broken DNS gets noticed least.", "", 2},
	{"homelab", "protocol", "document-before-close", "No lab change closes without its note: config diffs paste into the note BEFORE the terminal closes.", "", 2},

	// ---------------- homelab: stage (1) ----------------
	{"homelab", "stage", "backup-restore-drill-q2-done", "Q2 restore drill complete: 22m end-to-end, one gap found (outbox backfill step) and folded into the runbook.", "", 3},

	// ---------------- dotfiles: (7) ----------------
	{"dotfiles", "gotcha", "zsh-compinit-cache-stale", "compinit rebuilds the completion dump on every shell when the fpath mtime changes; cache it or pay 300ms.", "", 2},
	{"dotfiles", "gotcha", "brew-autoupdate-path-race", "brew autoupdate under launchd runs before PATH setup; pin the brew binary path in the plist.", "", 0},
	{"dotfiles", "decision", "mise-over-asdf", "mise over asdf for runtimes: faster shims, same .tool-versions format, no bash bottleneck.", "", 2},
	{"dotfiles", "reference", "one-repo-stow-layout", "The dotfiles layout: one repo, stow packages per tool -- adopted after per-tool repos made drift invisible.", "", 0},
	{"dotfiles", "constraint", "no-secrets-in-dotfiles", "Nothing secret enters the dotfiles repo: tokens live in the keychain, referenced by name only.", "", 2},
	{"dotfiles", "reference", "zsh-startup-profiling", "zprof + the hyperfine harness used to get shell startup 480ms -> 90ms; the flamegraph recipe.", "", 0},
	{"dotfiles", "stage", "nvim-lsp-config-consolidated", "Neovim LSP config consolidated to lazy.nvim + one servers table; per-language files retired.", "", 0},

	// ---------------- _global: (15) ----------------
	{"", "gotcha", "gh-actions-cache-key-branch-scope", "GH Actions cache is branch-scoped: main's cache never warms PR builds unless the key omits the ref.", "", 3},
	{"", "gotcha", "macos-sleep-kills-long-ssh", "macOS sleep drops long-lived SSH despite caffeinate -i on the child; use -s or the session dies with the lid.", "", 2},
	{"", "decision", "uv-for-python-everywhere", "uv replaces pip/venv/pipx across all repos: lockfile committed, scripts run via uv run.", "", 4},
	{"", "decision", "conventional-commits-fleetwide", "All repos use conventional commits; release notes are generated, a freeform subject breaks the changelog.", "", 4},
	{"", "decision", "pnpm-over-npm", "pnpm is the default Node package manager fleet-wide; npm only where a lockfile pins it.", "", 3},
	{"", "runbook", "new-repo-checklist", "New repo: branch protection, CI template, CODEOWNERS, conventional-commit hook, seamless map-repo.", "", 3},
	{"", "runbook", "rotate-github-pat", "PAT rotation: mint fine-grained token, update keychain entry, run the verify script, revoke old.", "", 2},
	{"", "constraint", "friday-no-deploys", "No production deploys after Friday 14:00 -- pager rotation is thin on weekends.", "", 5},
	{"", "constraint", "agents-verify-in-worktree", "Agents verify builds in a throwaway worktree, never the shared checkout -- parallel edits break foreign builds.", "", 4},
	{"", "constraint", "findings-are-for-the-next-agent", "Session findings are written for the NEXT agent: state what changed, what's open, and where the bodies are buried.", "", 4},
	{"", "protocol", "session-naming-cc-hex", "Agent sessions are cc/<8 hex>; anything else breaks the provenance joins in the console.", "", 3},
	{"", "protocol", "plan-before-multi-file-change", "Changes touching >3 files start in plan mode; the captured plan is the review artifact.", "", 3},
	{"", "protocol", "escalate-after-two-failed-fixes", "Two failed fix attempts on the same symptom = stop and write up a trial; the third attempt needs a hypothesis.", "", 2},
	{"", "reference", "fleet-model-pricing", "Current model pricing + context windows for the fleet's providers; refresh monthly.", "", 2},
	{"", "stage", "seam-onboarding-all-repos-mapped", "Every active repo is now mapped to a project; ambient sessions bind without explicit project flags.", "", 3},
}

// supersededSpec seeds an invalidated memory so supersession chains and the
// lifecycle story show up in the console.
type supersededSpec struct {
	project    string
	kind       string
	name       string
	desc       string
	successor  string // name of the active memory that replaced it
	daysAgoSup int    // when it was superseded; 0 = minutes ago (the live demo beat)
}

var supersededSpecs = []supersededSpec{
	{"orbital", "gotcha", "edge-cache-gotcha", "Edge cache misses on static assets; suspected CDN config. (v1 -- superseded by the Vary: Cookie root cause.)", "edge-cache-vary-cookie", 0},
	{"orbital", "gotcha", "webhook-dedupe-idempotency-key", "Dedupe webhooks on the idempotency key. (Wrong: the key rotates on retry; see the replay-window memory.)", "stripe-webhook-replay-window", 7},
	{"homelab", "runbook", "caddy-manual-certs", "Manually issue certs for .local services from the internal CA. (Superseded by the mDNS/CA-profile approach.)", "caddy-internal-tls-mdns", 15},
}

// noteSpec is one note to seed.
type noteSpec struct {
	project string
	slug    string
	title   string
	desc    string
	tags    []string
	daysAgo int
	body    string
	iter    int // plan_iteration for cc-plan captures; 0 = none
}

var planNarrativeNotes = []noteSpec{
	{"orbital", "plan-billing-webhooks-v2", "PLAN: billing-webhooks-v2 -- replay-safe webhook ingest", "Why v2: the 07-08 storm showed ingest trusts delivery uniqueness. Sequence: schema, outbox dispatch, signature rotation, replay dedupe, burst load test.", []string{"plan:billing-webhooks-v2", "created-by:agent"}, 10, "The 07-08 replay storm delivered every event an average of 6.2 times. v1 ingest assumed at-most-once delivery per event id, which is not what providers promise.\n\nSequence: provider-event schema, outbox writer + dispatcher, signature verification + rotation, replay protection + dedupe store, then a 50x burst load test. Each step lands independently; the dedupe store is the gate for calling ingest replay-safe.", 0},
	{"orbital", "plan-edge-cache-rollout", "PLAN: edge-cache-rollout -- static split + SWR", "Get the edge hit rate off the floor: cache-key audit, cookieless static split, stale-while-revalidate for API GETs, then dashboards.", []string{"plan:edge-cache-rollout", "created-by:agent"}, 18, "", 0},
	{"orbital", "plan-postgres-16-migration", "PLAN: postgres-16-migration -- logical replica cutover", "PG14 -> 16 via logical replication: replica build, extension sweep, app compat gates, cutover rehearsal, promote.", []string{"plan:postgres-16-migration", "created-by:agent"}, 34, "", 0},
	{"orbital-web", "plan-design-tokens-refresh", "PLAN: design-tokens-refresh -- tokens.json as single source", "Kill hand-written CSS vars: inventory the 214 existing vars, generate vars + TS consts from tokens.json, migrate components.", []string{"plan:design-tokens-refresh", "created-by:agent"}, 12, "", 0},
	{"homelab", "plan-backup-restore-drill", "PLAN: backup-restore-drill -- prove the backups restore", "Quarterly drill: restore the vault VM from PBS to the spare node, boot isolated, verify secrets round-trip end to end.", []string{"plan:backup-restore-drill", "created-by:agent"}, 25, "", 0},
}

var ccPlanCaptureNotes = []noteSpec{
	{"orbital", "cc-plan-billing-webhooks-v2", "Replay-safe webhook ingest (billing-webhooks-v2)", "Captured Claude Code plan billing-webhooks-v2.md (iteration 3, approved)", []string{"cc-plan", "plan:billing-webhooks-v2", "plan-status:approved", "created-by:agent"}, 10, "Captured plan body: goals, non-goals, the five steps with acceptance criteria, and the burst-test exit bar (p99 ack < 900ms at 50x for 10 min, zero duplicate postings).", 3},
	{"orbital", "cc-plan-api-rate-limiting", "API rate limiting for public endpoints", "Captured Claude Code plan api-rate-limiting.md (iteration 2, presented)", []string{"cc-plan", "plan:api-rate-limiting", "plan-status:presented", "created-by:agent"}, 0, "Captured plan body: GCRA limiter keyed per token, per-node SQLite counters with bounded drift, Retry-After semantics, and a dark-launch phase counting would-be rejections before enforcement.", 2},
}

var agentCacheNotes = []noteSpec{
	{"orbital", "cc-agent-7f3a91d2", "Planning subagent: rate-limit algorithm survey", "Cached planning subagent output: GCRA vs sliding window vs token bucket, with per-node drift math.", []string{"agent-cache", "plan:api-rate-limiting", "created-by:agent"}, 0, "", 0},
	{"orbital", "cc-agent-c48b0e17", "Planning subagent: provider rate-limit precedents", "Cached planning subagent output: how Stripe/GitHub/Shopify shape 429s, headers, and docs.", []string{"agent-cache", "plan:api-rate-limiting", "created-by:agent"}, 0, "", 0},
	{"orbital", "cc-agent-2d9e55ab", "Planning subagent: dedupe store sizing", "Cached planning subagent output: row width, TTL partitioning, and burst insert throughput for the dedupe table.", []string{"agent-cache", "plan:billing-webhooks-v2", "created-by:agent"}, 10, "", 0},
}

var miscNotes = []noteSpec{
	// orbital (20)
	{"orbital", "webhook-storm-postmortem-draft", "Webhook storm postmortem draft -- 2026-07-08", "Draft postmortem: timeline, the lowercased header root cause, replay math, and the five action items.", []string{"postmortem", "created-by:agent"}, 6, "Timeline: 09:14 first duplicate-charge support ticket; 09:41 dedupe alert; 10:05 deploy freeze; 11:30 root cause (LB lowercases X-Signature, first deliveries fail verification, providers replay). Impact: 312 duplicate webhook processings, 0 duplicate charges (ledger idempotency held). Action items -> plan:billing-webhooks-v2.", 0},
	{"orbital", "outbox-dispatcher-design", "Outbox dispatcher design: at-least-once + dedupe", "Design note for dispatcher v2: batched NOTIFY wakeups, poll fallback, per-consumer cursors, backpressure.", []string{"design", "created-by:agent"}, 16, "", 0},
	{"orbital", "provider-event-id-census", "Provider event id census across Stripe/Adyen payloads", "Every event family's id field, stability guarantees, and length bounds -- the dedupe key contract.", []string{"research", "created-by:agent"}, 9, "", 0},
	{"orbital", "invoice-burst-load-profile", "Load profile: webhook bursts at invoice finalization", "Measured burst shape at cycle boundary: 40-60x baseline for ~4 min, long tail of retries for 3 days.", []string{"research", "created-by:agent"}, 13, "", 0},
	{"orbital", "rate-limiting-algorithms-compared", "Research: rate limiting algorithms compared", "GCRA vs sliding window vs token bucket for our shape: GCRA wins on memory and burst semantics.", []string{"research", "created-by:agent"}, 2, "", 0},
	{"orbital", "decision-record-api-versioning", "Decision record: Accept-header API versioning", "Why versions ride the Accept header: proxy route stability, per-endpoint client migration, sunset headers.", []string{"decision-record"}, 30, "", 0},
	{"orbital", "pgx-v5-migration-notes", "pgx v5 migration notes and pitfalls", "What broke moving to pgx v5: pool config renames, the LISTEN checkout trap, scany version pin.", []string{"research", "created-by:agent"}, 27, "", 0},
	{"orbital", "replay-window-measurements", "Replay window measurements: provider retry schedules", "Empirical retry schedules per provider from the storm logs; Stripe tops out at 3 days, Adyen at 36h.", []string{"research", "created-by:agent"}, 6, "", 0},
	{"orbital", "incident-timeline-replay-spike", "Incident timeline: 07-08 replay spike", "Raw timeline notes kept during the incident; source material for the postmortem.", []string{"incident"}, 7, "", 0},
	{"orbital", "signature-verification-findings", "Signature verification: LB header case findings", "The repro matrix that isolated header lowercasing to the LB path; direct traffic verifies clean.", []string{"research", "created-by:agent"}, 7, "", 0},
	{"orbital", "billing-reconciliation-drift", "Billing reconciliation job: drift sources", "Three real drift sources found: replica lag reads, timezone bucketing, and orphaned outbox rows.", []string{"research", "created-by:agent"}, 21, "", 0},
	{"orbital", "payments-idempotency-audit", "Payments idempotency audit checklist", "Route-by-route audit of idempotency behavior; two mutations were retry-unsafe before the outbox landed.", []string{"audit", "created-by:agent"}, 24, "", 0},
	{"orbital", "queue-depth-telemetry", "Queue depth telemetry: what we alert on", "Outbox depth, dispatch lag, and dedupe hit-rate: thresholds, and why absolute depth alone is a bad alert.", []string{"ops"}, 19, "", 0},
	{"orbital", "session-digest-2026-06-orbital", "Session digest -- 2026-06 (orbital)", "Monthly roll-up of session findings: outbox v2 design settled, PG16 cutover rehearsed, storm precursors missed.", []string{"digest", "created-by:agent"}, 14, "", 0},
	{"orbital", "api-error-taxonomy", "API error taxonomy: retryable vs terminal", "The error code table: which 4xx/5xx are retryable, what Retry-After promises, and idempotency interplay.", []string{"design"}, 29, "", 0},
	{"orbital", "canary-rollout-outbox-v2", "Canary rollout notes: outbox dispatcher v2", "Canary soak observations: NOTIFY batching cut wakeups 20x; one poll-fallback trigger during a conn reset.", []string{"ops", "created-by:agent"}, 4, "", 0},
	{"orbital", "pg16-extension-compat-sweep", "Postgres 16 upgrade: extension compatibility sweep", "Every installed extension vs PG16: two needed upgrades, pg_partman config change documented.", []string{"research", "created-by:agent"}, 33, "", 0},
	{"orbital", "latency-budget-webhook-ingest", "Latency budget: webhook ingest path", "The 5s provider timeout decomposed: LB 50ms, verify 5ms, enqueue 20ms -- the budget lives in the queue write.", []string{"design"}, 22, "", 0},
	{"orbital", "support-tickets-duplicate-charges", "Interview notes: support tickets about duplicate charges", "What users actually saw during the storm: duplicate emails, not duplicate charges. Messaging matters.", []string{"research"}, 5, "", 0},
	{"orbital", "q3-payments-roadmap-summary", "Meeting summary: Q3 payments roadmap", "Q3 scope: rate limiting, dispute webhooks, and the ledger export API; explicit non-goals listed.", []string{"meeting"}, 8, "", 0},
	{"orbital", "delivery-semantics-reading", "Reading: delivery semantics and the outbox pattern", "Notes from the reading pass that settled the outbox decision; exactly-once is a coordination cost, not a flag.", []string{"research"}, 35, "", 0},
	// orbital-web (12)
	{"orbital-web", "design-tokens-inventory", "Design tokens inventory: 214 hand-written vars", "The census: 214 CSS vars, 61 unused, 40 duplicates-with-drift; the mapping table into tokens.json.", []string{"research", "created-by:agent"}, 11, "", 0},
	{"orbital-web", "playwright-migration-retro", "Playwright migration retrospective", "e2e 14m -> 4m with parallel workers; two real races surfaced by faster runs, both fixed.", []string{"retro", "created-by:agent"}, 20, "", 0},
	{"orbital-web", "bundle-analysis-dashboard-route", "Bundle analysis: dashboard route regression", "The +38KB regression bisected to a locale data import; fix is a dynamic import per locale.", []string{"research", "created-by:agent"}, 5, "", 0},
	{"orbital-web", "next-image-loader-research", "Research: next/image loaders vs our CDN", "Loader contract vs CDN rewrite rules; the /static/opt/ exclusion and why width buckets beat exact widths.", []string{"research", "created-by:agent"}, 26, "", 0},
	{"orbital-web", "storybook-visual-diff-workflow", "Storybook visual-diff workflow", "How baselines refresh, what the diff gallery gates, and the flake quarantine path.", []string{"ops"}, 17, "", 0},
	{"orbital-web", "theme-flash-investigation", "Theme flash investigation: first-paint token timing", "Why the effect-based theme set flashes; the inline-script fix and its CSP consequence.", []string{"research", "created-by:agent"}, 15, "", 0},
	{"orbital-web", "a11y-sweep-findings", "A11y sweep findings: focus traps in modals", "Sweep results: three modal focus traps, one skip-link regression; portal + suspense interplay documented.", []string{"audit", "created-by:agent"}, 10, "", 0},
	{"orbital-web", "session-digest-2026-06-web", "Session digest -- 2026-06 (orbital-web)", "Monthly roll-up: Playwright migration finished, token census started, bundle budget held.", []string{"digest", "created-by:agent"}, 14, "", 0},
	{"orbital-web", "safari-date-input-matrix", "Safari date input quirks: test matrix", "datetime-local behavior across Safari versions; the epoch-ms boundary convention that sidesteps it.", []string{"research"}, 23, "", 0},
	{"orbital-web", "component-token-census", "Component census: who consumes which tokens", "Generated map of component -> token usage; the migration order falls out of the dependency counts.", []string{"research", "created-by:agent"}, 3, "", 0},
	{"orbital-web", "decision-record-rsc-boundaries", "Decision record: RSC boundaries for the dashboard", "Boundary at route segments: fewer client islands, request-scoped data stays server-side.", []string{"decision-record"}, 25, "", 0},
	{"orbital-web", "web-vitals-before-after-cache", "Web vitals snapshot before/after cache fix", "LCP p75 2.9s -> 1.6s after the static split; INP unchanged; the field-data query saved for reuse.", []string{"research", "created-by:agent"}, 4, "", 0},
	// homelab (9)
	{"homelab", "pbs-restore-drill-log-q2", "PBS restore drill log -- 2026-Q2", "Full drill log: 22m end-to-end, secrets round-trip verified, the one gap (backfill step) noted.", []string{"drill", "created-by:agent"}, 25, "", 0},
	{"homelab", "proxmox-82-upgrade-notes", "Proxmox 8.2 upgrade notes", "Node-by-node upgrade log; quorum held, one NIC rename caught by the checklist.", []string{"ops"}, 31, "", 0},
	{"homelab", "caddy-config-refactor", "Caddy config refactor: one site block per service", "The refactor diff and the review checklist; bad blocks now fail alone instead of taking the file down.", []string{"ops", "created-by:agent"}, 28, "", 0},
	{"homelab", "vlan-layout-firewall-rules", "VLAN layout and firewall rules", "The three-tier layout, inter-VLAN rules table, and the IoT egress allowlist.", []string{"reference"}, 36, "", 0},
	{"homelab", "ups-runtime-test-results", "UPS runtime test results", "Measured runtime at current load: 18 min to low-battery; shutdown chain needs 6 -- comfortable margin.", []string{"drill"}, 32, "", 0},
	{"homelab", "wireguard-peer-inventory", "WireGuard peer inventory", "Every peer, its allowed-ips, and which config generation it carries; two stale peers revoked.", []string{"audit", "created-by:agent"}, 18, "", 0},
	{"homelab", "backup-coverage-audit", "Backup coverage audit: what is NOT backed up", "The honest list: scratch datasets, the media pool, and two config dirs that should be -- now ticketed.", []string{"audit", "created-by:agent"}, 24, "", 0},
	{"homelab", "session-digest-2026-06-homelab", "Session digest -- 2026-06 (homelab)", "Monthly roll-up: restore drill done, cert rotation runbook written, scrub IO fix applied.", []string{"digest", "created-by:agent"}, 14, "", 0},
	{"homelab", "vault-cert-rotation-notes", "Vault VM cert rotation notes", "The rotation dry-run: issue, hot-reload, agent re-auth verified; revocation timing gotcha noted.", []string{"ops", "created-by:agent"}, 9, "", 0},
	// dotfiles (3)
	{"dotfiles", "zsh-startup-profiling-notes", "zsh startup profiling: 480ms -> 90ms", "zprof findings: compinit cache and nvm lazy-load were 80% of the win; hyperfine harness saved.", []string{"research", "created-by:agent"}, 21, "", 0},
	{"dotfiles", "nvim-lsp-cleanup-notes", "Neovim LSP config cleanup notes", "The consolidation to one servers table; which per-language quirks survived as overrides.", []string{"research"}, 16, "", 0},
	{"dotfiles", "brewfile-audit", "Brewfile audit: orphaned casks", "14 orphaned casks removed; the quarterly audit one-liner saved for next time.", []string{"audit", "created-by:agent"}, 11, "", 0},
	// _global (2)
	{"", "weekly-digest-2026-w28", "Weekly digest -- 2026-W28", "Cross-project week: storm postmortem drafted, dedupe store landed, token generator shipped, drill gap closed.", []string{"digest", "created-by:agent"}, 3, "", 0},
	{"", "agent-fleet-conventions", "Agent fleet conventions: session naming + findings style", "The conventions every agent follows: cc/<hex> names, findings written for the next agent, plan-first for big changes.", []string{"reference"}, 38, "", 0},
}

// planSpec drives plan-step task creation.
type stepSpec struct {
	title   string
	status  string // open | in_progress | done
	daysAgo int    // when created; for done steps also ~when closed (+1d)
	claimed bool   // in_progress step claimed by a live session
	depPrev bool   // depends on the previous step
}

type planSpec struct {
	slug    string
	project string
	daysAgo int
	steps   []stepSpec
}

var planSpecs = []planSpec{
	{"billing-webhooks-v2", "orbital", 10, []stepSpec{
		{"Schema for provider events", "done", 10, false, false},
		{"Outbox writer + dispatcher", "done", 9, false, true},
		{"Signature verification + rotation", "done", 8, false, true},
		{"Replay protection + dedupe store", "in_progress", 7, true, true},
		{"Load test at 50x burst", "open", 7, false, true},
	}},
	{"edge-cache-rollout", "orbital", 18, []stepSpec{
		{"Cache-key audit", "done", 18, false, false},
		{"Static asset split + Vary fix", "done", 16, false, true},
		{"Hit-rate dashboard + alerts", "done", 12, false, false},
		{"Stale-while-revalidate for API GETs", "open", 12, false, false},
	}},
	{"postgres-16-migration", "orbital", 34, []stepSpec{
		{"Build logical replica on PG16", "done", 34, false, false},
		{"Extension compatibility sweep", "done", 33, false, false},
		{"App compat gates in CI", "done", 31, false, false},
		{"Cutover rehearsal on staging", "done", 28, false, true},
		{"Promote + retire old primary", "done", 26, false, true},
	}},
	{"design-tokens-refresh", "orbital-web", 12, []stepSpec{
		{"Token inventory + usage census", "done", 12, false, false},
		{"Generate CSS vars + TS consts from tokens.json", "done", 6, false, true},
		{"Migrate components off hand-written vars", "in_progress", 6, true, true},
	}},
	{"backup-restore-drill", "homelab", 25, []stepSpec{
		{"Restore vault VM snapshot to spare node", "done", 25, false, false},
		{"Boot isolated + verify secrets round-trip", "done", 25, false, true},
		{"Fold findings into the restore runbook", "done", 24, false, true},
	}},
}

// taskSpec is a standalone (non-plan) task.
type taskSpec struct {
	project  string
	title    string
	body     string
	status   string
	daysAgo  int
	claimed  bool // claimed by a live session (in_progress + future lease)
	dependsI int  // index into this slice the task depends on; -1 = none
}

var standaloneTasks = []taskSpec{
	{"orbital", "Rotate Stripe webhook signing key", "Post-storm rotation per the rotate-webhook-signing-keys runbook. Old key stays in the ring 72h.", "in_progress", 2, true, -1},
	{"orbital", "Cut release 0.4.0", "Ship dispatcher v2 + dedupe store. Blocked on the signing key rotation completing its 72h overlap.", "open", 2, false, 0},
	{"orbital", "Fix flaky TestOutboxDispatch_Retry", "Fails ~1/30 under -race; suspected timer leak in the poll fallback path.", "open", 5, false, -1},
	{"orbital", "Upgrade pgx to v5.6", "Pool health-check fix we want; check the LISTEN checkout behavior did not regress.", "open", 9, false, -1},
	{"orbital", "Write postmortem: 07-08 webhook storm", "Finish the draft note into the published postmortem; action items already tracked in plan:billing-webhooks-v2.", "open", 6, false, -1},
	{"orbital-web", "Audit bundle size regression on /dashboard", "+38KB on the route bundle; bisect and fix. RESOLVED: locale data import, now dynamic.", "done", 5, false, -1},
	{"orbital-web", "Storybook visual-diff baseline refresh", "Refresh after the token generator landed; review the diff gallery before committing.", "done", 8, false, -1},
	{"homelab", "Replace vault VM cert before 08-01", "Rotate per the runbook; verify agent auto-auth after reload.", "done", 3, false, -1},
	{"homelab", "PBS retention: move to keep-daily 14", "Datastore has headroom after the prune fix; extend daily retention.", "done", 12, false, -1},
}

// trialSpec is one research-lab trial.
type trialSpec struct {
	title    string
	changes  string
	expected string
	actual   string
	outcome  string
	daysAgo  int
}

const trialLab = "webhook-retry-storm"

var trialSpecs = []trialSpec{
	{"Reproduce replay burst locally", "Replay 07-08 event log at 50x against a dev stack", "Duplicate processings appear once rate exceeds dedupe window", "Reproduced: 6.2x average duplicate processing, matches prod", "pass", 7},
	{"Dedupe on idempotency key", "Key dedupe store on the provider idempotency key", "Replays collapse to one processing", "FAILED: key rotates on provider retry; replays pass straight through", "fail", 7},
	{"Dedupe on provider event id", "Key dedupe store on provider event id", "Replays collapse to one processing", "All replays collapsed; 0 duplicates across the full 3-day window", "pass", 6},
	{"Backoff cap at 15m", "Cap outbound retry backoff at 15 minutes", "Consumer recovers within one cap interval after outage", "Recovered in 14m; no thundering herd on the half-hour", "pass", 6},
	{"LB header case sensitivity", "Compare X-Signature casing on direct vs LB path", "Header casing identical on both paths", "LB lowercases the header; direct path preserves case -- ROOT CAUSE", "pass", 6},
	{"Drop sig-mismatch silently", "Silently drop signature mismatches instead of 400", "Provider stops replaying dropped events", "Providers keep replaying regardless; silent drop only hides the signal -- alert instead", "fail", 5},
	{"Unique index under burst insert", "UNIQUE(provider_event_id) with 50x concurrent inserts", "No deadlocks; losers get clean conflict errors", "2 deadlocks per 10k until app id left the index; clean after", "partial", 5},
	{"Dedupe TTL at 4 days", "TTL partition drop at 4 days under replay tail", "No replay escapes dedupe before TTL expiry", "3-day replay tail fully inside window; day-4 partition drop clean", "pass", 5},
}

// findingsPool feeds session_end findings for historical sessions.
var findingsPool = []string{
	"Replay dedupe must key on provider event id; idempotency keys rotate on retry. Landed the dedupe store schema.",
	"Vary: Cookie was the entire cache-miss story; hit rate 4% -> 71% after the static split.",
	"Playwright migration done; e2e 14m -> 4m; two real races surfaced and fixed.",
	"PITR drill clean: 22m end-to-end. One gap: outbox backfill needs a runbook step -- added.",
	"Token census finished: 214 vars, 61 unused. Generator design agreed; consts are the only import surface.",
	"Dispatcher v2 canary is quiet: NOTIFY batching cut wakeups 20x. Fleet rollout waits for the billing cycle.",
	"Root cause on the storm: LB lowercases X-Signature. Verification is now case-insensitive; postmortem drafted.",
	"PG16 cutover rehearsal passed; promote scheduled Sunday. Extension sweep found two upgrades needed.",
	"Bundle regression bisected to locale data import; dynamic import per locale lands tomorrow.",
	"Vault cert rotation dry-run verified; revocation must wait for agent re-auth -- noted in the runbook.",
	"Scrub IO starvation fixed with a speed cap; VM p99 back to baseline during scrub windows.",
	"Rate-limit survey done: GCRA per token, per-node SQLite counters. Plan drafted for review.",
	"Focus-trap fixes landed for all three modals; portal + suspense mount order documented.",
	"Backfill runbook validated against the outage replay; idempotent upsert confirmed safe to re-run.",
	"Queue depth alert rewritten around dispatch lag; absolute depth alone was pure noise.",
	"Two stale WireGuard peers revoked; peer inventory note is now the source of truth.",
	"zsh startup at 90ms after compinit caching; profiling harness saved to the dotfiles notes.",
	"Session digest for June written; the storm precursors we missed are called out for the retro.",
}

// promptsPool feeds hook.prompt events (prompts with no recall match).
var promptsPool = []string{
	"why is the outbox dispatcher waking up 40 times a second",
	"add a retry budget to the webhook push worker",
	"what did we decide about api versioning",
	"the dashboard bundle grew again, find out why",
	"write the postmortem summary section",
	"can the dedupe table partitions drop without locking ingest",
	"migrate the button component to generated tokens",
	"why do iOS devices complain about the lab certs",
	"draft the q3 roadmap summary from the meeting notes",
	"is the canary error budget still green",
}

// recallQueries feed recall tool calls (they DO match memories).
var recallQueries = []string{
	"stripe webhook retry dedupe",
	"edge cache vary cookie hit rate",
	"signing key rotation runbook",
	"pgx pool listen connection",
	"rate limit counters sqlite drift",
	"outbox transaction guarantees",
	"playwright flaky parallel workers",
	"design tokens generator consts",
	"bundle budget dashboard route",
	"pbs retention prune gc order",
	"vault vm backup snapshot rules",
	"wireguard mtu pppoe tunnel stall",
	"deploy canary soak checklist",
	"jwt clock skew login failures",
	"invoice finalization burst shape",
	"conventional commits changelog",
}

// briefingPreviews give retrieval.injected SessionStart events a plausible
// content preview (what the console shows when a row expands).
var briefingPreviews = []string{
	"<seam-briefing>\nSeam project: orbital -- 8 constraints, 53 memories, 3 recent findings.\nCONSTRAINT: payments-idempotent-by-event-id: Every payment mutation must be idempotent on provider event id...\nPLAN: billing-webhooks-v2 -- 3/5 done, 1 claimable, 1 in flight\n...",
	"<seam-briefing>\nSeam project: orbital-web -- 6 constraints, 30 memories, 3 recent findings.\nCONSTRAINT: bundle-budget-180kb: First-load JS budget 180KB gz per route...\nPLAN: design-tokens-refresh -- 2/3 done, 0 claimable, 1 in flight\n...",
	"<seam-briefing>\nSeam project: homelab -- 2 constraints, 23 memories, 2 recent findings.\nCONSTRAINT: vault-vm-never-snapshots-live: The vault VM is never live-snapshotted...\n...",
	"<seam-briefing>\nSeam project: dotfiles -- 1 constraint, 7 memories, 1 recent finding.\n...",
}
