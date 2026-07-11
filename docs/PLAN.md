# Seam v2 — greenfield rewrite plan

Status: proposed 2026-07-10 (owner asked for a rewrite direction after approving the agent-first refactor).
Relationship to `docs/PLAN.md`: the two are alternative paths to the same end-state. PLAN.md refactors in place; this plan builds a new system beside the old one and cuts over at parity. If the rewrite is chosen, PLAN.md remains valuable as the requirements record (its tool table, lifecycle semantics, and observability spec are reused verbatim here), and its Phase 0 paper cuts (title validation, session_end cap) should STILL be applied to old Seam immediately, since it keeps serving daily traffic during the rewrite.

## Why a rewrite is justified here (decision record)

Factors that normally kill rewrites do not apply:
- No external users, one owner, no compatibility contract to honor except the owner's own agents.
- The durable data is portable by design: markdown files on disk plus a rebuildable index. 274 notes today; an importer is an afternoon, not a project.
- The in-place refactor already deletes roughly half the tree (TUI, assistant, chat, webhooks, review, templates, voice, most web pages) and heavily rewires what remains. The "preserve working code" argument mostly evaporates when this much is scheduled for demolition anyway.
- The conceptual center changes: v1 is a human notes app with agent access bolted on (memories are notes with title-string encoding `"Knowledge: {cat} - {name}"` and tag conventions). v2's center is an agent memory and coordination substrate where memory, session, task, and trial are first-class. Data models are the thing refactors are worst at changing.
- Production stability: old Seam keeps running untouched (hooks + MCP serve every Claude Code session daily) while v2 grows beside it. The in-place path performs surgery on the live system across ~40 steps.

Honest costs and risks, with mitigations:
- Second-system effect. Mitigation: the 26-tool contract from PLAN.md is the scope fence; anything not in it is out of v2.0.
- Parity valley (new system useless until it isn't). Mitigation: dogfood-first build order — by Phase 2 v2 is the daily driver for ONE repo while old Seam serves the rest; expand per-repo as confidence grows.
- Regressions in battle-tested corners (file watching, frontmatter edge cases, SSRF guards). Mitigation: those packages are PORTED, not rewritten (explicit port list below).
- Cost: rewrite to cutover is roughly +50% over the refactor. Accepted in exchange for the cleaner foundation.

## What we now know (design inputs)

From 4 months of telemetry, the verified landscape scan, and the two review notes:
1. Agents are the only real client; the human is an observer/editor. UI exists solely for observability.
2. Files-as-truth is right for durable knowledge (memories, notes) and wrong for high-churn state (sessions, tasks, trials, telemetry) — DB-of-record for those.
3. Memory needs a lifecycle: bi-temporal supersession, provenance, write-time arbitration, scheduled gardening. Flat memories + explicit links beat entity-extraction graphs (Mem0's own ~2% delta).
4. Retrieval: hooks-injected briefing under a hard token budget + precision-floor prompt injection + one pull tool (recall) with hybrid RRF ranking.
5. The corpus is small (hundreds, maybe thousands of items) — ChromaDB is unnecessary operational weight; brute-force cosine over SQLite-stored vectors is milliseconds at this scale and removes an entire failure class (the chroma boot-race and outage incidents are documented in memories).
6. Multi-user was never real: JWT/bcrypt/refresh tokens/registration guard a single localhost owner. One static bearer key suffices.
7. A node/Vite/React toolchain is heavy machinery for an observability console. Server-rendered Go templates + SSE need no build step and let agents modify the console without npm.

## v2 architecture

Names (decided 2026-07-10): product **Seamless**, repo `seamless`, daemon binary `seamlessd`, CLI binary `seam` (short, existing muscle memory, no unix-tool collisions; replaces the v1 TUI binary of the same name at install time). Data dir `~/.seamless`, config file `seamless.yaml`, env prefix `SEAMLESS_*` (deliberately distinct from v1's `SEAM_*` so the two systems cannot cross-configure during the parallel run). Go 1.25+, no CGO, modernc SQLite + FTS5, single binary + CLI.

```
cmd/seamlessd/    server binary (serve, doctor, install-hooks, import)
cmd/seam/         headless CLI (agents + owner observability)
internal/core/    domain types: Project, Memory, Session, Task, Trial, Event
internal/store/   SQLite: schema, FTS5, embeddings (BLOB vectors + brute-force cosine), migrations
internal/files/   markdown layer: memory/note files, frontmatter, watcher + reconciliation  [PORT]
internal/llm/     ollama/openai/anthropic chat + embeddings clients                          [PORT, trimmed]
internal/retrieve/ briefing assembler, prompt-context matcher, recall (RRF), token budgets   [PORT phase-1-7 logic, upgraded]
internal/lifecycle/ supersession, arbitration, provenance
internal/gardener/ scheduled passes: dedup, staleness, digests; proposals
internal/tasks/   ready-queue with dependency edges
internal/mcp/     26 tools; shared handler layer also backing the CLI JSON-RPC
internal/hooks/   SessionStart / UserPromptSubmit / SessionEnd endpoints + installer + doctor [PORT, extended]
internal/console/ server-rendered observability UI (html/template + vanilla JS + SSE), read-mostly
internal/events/  append-only event log; SSE fan-out; retrieval stats
internal/capture/ SSRF-safe URL fetch                                                        [PORT]
internal/validate/ path/title guards (WITH the ".." title fix)                               [PORT]
internal/config/  single yaml + env; static api key; budgets; no JWT config                  [PORT, halved]
```

Deleted concepts (never built in v2): assistant, chat, TUI-as-app, webhooks, review queue, templates, voice capture, daily notes, JWT/multi-user/registration, React SPA + node toolchain, ChromaDB, cron scheduler package (gardener runs on a plain ticker), WebSocket hub (SSE instead).

### Data model

Files (source of truth for durable knowledge):
```
{data_dir}/memory/{project|_global}/{name}.md     one memory per file
{data_dir}/notes/{project|inbox}/{slug}.md        work artifacts
```
Memory frontmatter (real fields, no title encoding):
```yaml
---
id: 01K...                # ULID
kind: gotcha              # constraint|runbook|protocol|gotcha|decision|refuted|reference|stage
name: chroma-boot-race
description: one line, <=150 chars, the ONLY text shown in indexes
project: seam             # empty = global
created: 2026-07-10T18:00:00Z
updated: ...
valid_from: 2026-07-10T18:00:00Z
invalid_at: null          # set on supersession/archive; invalid memories leave indexes
superseded_by: null       # ULID of the replacement
source_session: cc/ab12cd34
tags: [x, y]
---
body markdown
```
Watcher + startup reconciliation keep DB mirrors in sync with files exactly like v1 (ported).

SQLite (DB-of-record for churn; one `seam.db`):
- `projects(id, slug, name, description, created_at)`
- `memories_index`, `notes_index` — mirrors of frontmatter for query (id, kind, name, description, project, file_path, validity columns, content_hash, timestamps) + one unified FTS5 table over title/name/description/body
- `embeddings(item_id TEXT PK, kind TEXT, model TEXT, dims INT, vec BLOB, updated_at)` — float32 LE; brute-force cosine in Go (guardrail: do NOT add a vector DB; at 10k items x 3072 dims a scan is ~10-30 ms)
- `sessions(id, name UNIQUE, project_slug, status, findings, claude_session_id, cwd, source, ambient BOOL, metadata JSON, created_at, updated_at)`
- `tasks(id, project_slug, title, body, status open|in_progress|done|dropped, created_by, created_at, updated_at, closed_at)` + `task_deps(task_id, depends_on)`
- `trials(id, lab, title, changes, expected, actual, outcome, metrics JSON, session_id, project_slug, created_at)` — DB-first (queryable metrics natively; the v1 fenced-JSON-in-notes parsing was a workaround)
- `events(id, ts, kind, session_id, project_slug, item_id, payload JSON)` — append-only log of EVERYTHING (session lifecycle, memory write/read/supersede, injection, task transition, gardener action). Telemetry, stats, and the console feed derive from it.
- `retrieval_stats(item_id PK, inject_count, read_count, last_injected_at, last_read_at)` — maintained from events
- `gardener_proposals(id, kind merge|archive|digest, payload JSON, status, created_at, resolved_at)`
- `settings(key, value)` — repo_project_map, project_families, budgets, gardener toggles
- `jobs(id, type, payload, status, attempts, run_after)` — tiny queue for embeds + LLM digests (replaces v1 ai_tasks)

### Ready-queue semantics (tasks)

Borrowed from Beads' design (the tool itself is NOT a dependency — a single-owner ready queue is a SQL query, and tasks are DB-of-record churn state, so a git-backed external tracker would fight the architecture). Build these semantics into the tasks module from P3 so the ready queue is correct on day one:

- **Ready = actionable now.** A task is ready iff `status='open'` AND it has no *blocking* dependency that is still open/in_progress. `done` and `dropped` deps do NOT block (a dropped blocker unblocks its dependents — dropping is a valid way to clear a dependency). `in_progress` tasks are excluded from the ready queue (already claimed), but still count as blockers for their dependents.
- **`task_deps(task_id, depends_on)` is a blocks-edge.** `depends_on` must reference an existing task; reject dangling references at write time. One edge type only in v2.0 (blocks / depends-on); do not add parent-child or related edges unless a concrete need appears — decided deliberately, not by omission.
- **Cycle prevention.** Adding a dep that would create a cycle is rejected at write time (walk the existing edges before inserting). The ready-queue query assumes a DAG and must never loop.
- **Deterministic ordering.** Ready tasks are surfaced oldest-`created_at` first (stable, agent-predictable); ties broken by `id` (ULID, monotonic). The briefing ready-tasks line and `seam ready` share this ordering.
- **Blocked view.** Non-ready open tasks are "blocked"; the console Tasks page and `seam ready --blocked` show each blocked task with its still-open blockers, so the chain is legible.
- **Events.** Every status transition and dep add/remove writes an `events` row (`kind` = task transition), so the console and stats derive task history without a separate audit table.

Reference to steal LATER, not now: Beads-style compaction/collapse of long-closed tasks belongs to the **gardener** (a staleness/digest pass over `done`/`dropped` tasks), not the tasks module — keep it out of P3.

### API surface

- `/api/mcp` — MCP streamable HTTP, static bearer key. The 26 tools EXACTLY as specified in PLAN.md "Final MCP tool surface" (sessions 3, memory 4, recall 1, notes 5, projects 2, tasks 4, lab 3 as `lab_open`/`trial_record`/`trial_query`, gardener 2, capture_url, usage_summary), with v2 semantics already in (supersedes, provenance, write-scope inheritance, token budgets, metrics on trials).
- `/api/hooks/{session-start, user-prompt-submit, session-end}` — same contracts as PLAN.md Phase 3 (ambient sessions, harvest, briefing with constraints > stages > memory index > sibling findings > ready tasks, all under a ~1500-token budget).
- `/console/*` — server-rendered pages + `/console/events` SSE. Same static key via cookie set at `/console/login` (paste the key once).
- CLI `seam` — JSON-RPC to `/api/mcp`: `prime, remember, recall, ready, task, capture, status, sessions, usage, doctor`.
- Bind `127.0.0.1` by default. No JWT anywhere.

### Observability (first-class, per owner)

Console pages (html/template, no build step): Sessions (list + detail: findings, tool calls, injections with read-after-inject badges — all from `events`), Memories (browser: project/kind groups, description, age, validity badges, supersession chains, per-memory stats, archive button, edit link opening the file path), Retrieval (per-kind rates, read-after-inject trend, top/stale memories), Tasks (ready queue first, blocked with blockers), Gardener (proposal cards with previews, apply/dismiss), Settings (repo map, families, budgets). Terminal: `seam status/sessions/usage` render the same data. SSE keeps Sessions/Gardener live.

## Port list (lift from v1 with attribution, adapt imports only)

| v1 source | v2 destination | Notes |
|---|---|---|
| `internal/watcher` + reconciliation | `internal/files` | proven; adapt to two trees (memory/, notes/) |
| note frontmatter parse/write (`internal/note`) | `internal/files` | extend fields; keep atomic-write pattern |
| `internal/validate` | `internal/validate` | apply the Title ".." fix |
| `internal/capture` URL path | `internal/capture` | SSRF guards as-is |
| `internal/agent/hook_briefing.go`, `prompt_context.go`, `recall.go` | `internal/retrieve` | keep thresholds + sanitization; swap in token budgets, RRF, validity filters |
| `internal/server/hooks_handler.go` + `cmd/seamd/hooks_install.go` + doctor | `internal/hooks` | add SessionEnd; keep seam_managed idempotent merge + backups |
| `internal/ai` clients (ollama/openai/anthropic, embedder) | `internal/llm` | drop chroma client, task-queue coupling |
| `internal/usage` token accounting | `internal/events` + small usage module | fold retrieval recorder into events |
| FTS5 schema/trigger patterns from `migrations/001` | `internal/store` | unified index |
| `internal/mcp` tool/handler patterns | `internal/mcp` | same SDK unless a newer one is clearly better; keep per-session binding design |

## Phases

Guardrails: identical spirit to PLAN.md (AGENTS.md conventions carry over — write a v2 AGENTS.md in Phase 0; testify/require, table-driven, ULID, no CGO, never push, one commit per step, progress log at the bottom of this file). Old Seam keeps running untouched on :8080 throughout; Seamless develops on :8081 with `SEAMLESS_DATA_DIR=~/.seamless`.

- [x] **P0 — Skeleton (target: 1-2 days).** New repo, module, Makefile (build/test/lint/run), config (yaml + env, static key, budgets), `internal/store` with migration runner (pattern from v1) and migration 001 (all tables above), event log write path, `/healthz`, doctor skeleton. Port `validate`. Acceptance: `make test` green; server starts; doctor reports config + DB ok. **DONE 2026-07-10 (awaiting owner review before P1).**
- [x] **P1 — Files + import (2-3 days).** `internal/files`: memory/note frontmatter round-trip, atomic writes, watcher + reconciliation (port), FTS + embeddings jobs (brute-force cosine search working with Ollama/OpenAI embedder). `seamlessd import --from ~/.seam`: old memory notes (parse `"Knowledge: {cat} - {name}"` titles + `domain:/project:/session:` tags into real frontmatter), plain notes (normalize frontmatter), sessions + agent_tool_calls (as historical sessions/events), old trial notes (parse sections into `trials` rows), SKIP the briefings project. Acceptance: import on a COPY of prod data; counts reported and spot-checked (76 memories, ~28 notes, 42 sessions); FTS and cosine search return sane results; editing a memory file on disk round-trips through the watcher. **DONE 2026-07-10 (awaiting owner review before P2).**
- [x] **P2 — Core loop + dogfood (3-4 days).** MCP server (static key) with the minimal loop: `session_start/update/end` (with binding + write-scope inheritance), `memory_write/append/read/delete` (arbitration hint from day one — port dedupHint logic onto cosine search), `recall` (semantic + FTS, simple fusion), `notes_create/read/update/append/delete`, `project_list/create`. Hooks: session-start briefing (constraints + memory index + findings, token-budgeted) and user-prompt-submit (ported matcher). CLI: `prime, remember, recall, status`. Dogfood switch (owner-confirmed): add a SECOND user-scope MCP server named `seamless` pointing at :8081 in `~/.claude.json` (leave the v1 `seam` entry untouched), and install the v2 hooks into THIS repo's project-scoped `.claude/settings.json` only — v1's global hooks will also fire here with their small unmapped-repo fallback briefing, which is acceptable during dogfood. Acceptance: a real Claude Code session in the seamless repo gets a v2 briefing, writes/recalls memories; old Seam untouched for all other repos. DOGFOOD STARTS HERE — every subsequent phase is built with v2 as this repo's memory system. **DONE 2026-07-10 (owner accepted; dogfood live on :8081).**
- [x] **P3 — Lifecycle + tasks + ambient (3-4 days).** Bi-temporal supersession (`supersedes` param, validity filters everywhere, read warnings), provenance stamping, SessionEnd hook + ambient sessions + harvest (PLAN.md 3.1 contract), tasks v2 ready-queue + 4 tools + briefing line (build to the "Ready-queue semantics" spec above), sibling-family briefing section, `trial_record/trial_query/lab_open` on the trials table with native metrics filtering, `stage` kind pinned in briefings. Acceptance: PLAN.md Phase 2/3 verification scenarios, run against v2. **DONE 2026-07-10 (awaiting owner review before P4).**
- [x] **P4 — Gardener + retrieval quality (2-3 days).** Gardener ticker (propose-only: dedup >=0.88, staleness 90d via retrieval_stats, monthly session digests via LLM job; reference-aware protection; apply/dismiss via 2 MCP tools + console later), RRF recall (semantic + FTS + link expansion, k=60, validity- and budget-aware), retrieval_stats from events, `capture_url` + `usage_summary` tools. Tool count now 26 — assert in doctor. Acceptance: seeded-fixture gardener run produces all three proposal kinds; recall degrades to lexical with the embedder down. **DONE 2026-07-10 (awaiting owner review before P5).**
- [x] **P5 — Console + CLI observability (3-4 days).** All console pages + SSE; CLI `sessions/usage/ready/task/capture/doctor`; `install-hooks` for v2; doctor complete (key, hooks x3, tool count 26, gardener ticker, embedder reachability). Acceptance: owner walkthrough of every console page against live dogfood data; screenshots recorded. **DONE 2026-07-10 (awaiting owner review before P6).**
- [x] **P6 — Cutover (1-2 days + a parallel-run week).** Final `import` delta run (re-import anything old Seam accrued during the rewrite; imports are idempotent by id/name). Switch the global hooks + MCP registration to Seamless for ALL repos (installer handles it; remove the project-scoped dogfood hooks and rename/remove the old `seam` MCP entry), keep old seamd running read-only for one week as fallback, then stop and disable the old service. Archive the v1 repo (history + PLAN.md + review notes are the design record). Rename/move: Seamless takes over port 8080, data dir stays `~/.seamless`, `make install-service` for Seamless, update `~/.claude/CLAUDE.md` Seam section and replace the `/seam-onboard` skill from the Seamless docs. Acceptance: `make doctor` green on v2 as the sole system; one full day of normal multi-repo agent work with zero fallbacks to v1. **DONE 2026-07-10 -- owner elected a full same-day cutover (no parallel-run week). Deviations: Seamless stayed on :8081 (did not take :8080; v1 decommissioned frees 8080); v1 services disabled but NOT archived, all data preserved (`~/.seam`, `~/repos/seam`, plists as `.disabled`). See progress log.**

Total: roughly 3-4 weeks of agent execution at the PLAN.md level of care, with v2 earning its keep from P2 onward.

## Owner decisions needed before P0

- [v] Project/repo name: Seamless / seamless. find a nice short cli name so the user doesn't have to type seamless every time. -> Decided: CLI is `seam` (4 chars, existing muscle memory, no unix collisions, brand-coherent); daemon is `seamlessd` (distinguishable from v1's `seamd` in process lists during the parallel run); env prefix `SEAMLESS_*`.
- [v] Confirm: drop ChromaDB (embeddings in SQLite, brute-force cosine) — recommended yes.
- [v] Confirm: server-rendered console, no React/node toolchain — recommended yes.
- [v] Confirm: single static key + localhost bind, no JWT — recommended yes (revisit only if Seam ever leaves this machine; note Tailscale serve as the escape hatch).
- [v] Data dir: fresh `~/.seam2` then rename at cutover, or keep `~/.seam` with v2 subtree — recommended fresh dir.

## Progress log

(executor appends: date, step, result, divergences)

### 2026-07-10 — Bootstrap + P0 kickoff

Owner decisions (restated per REWRITE-PROMPT): product **Seamless**, repo `seamless`,
daemon `seamlessd`, CLI `seam`, data dir `~/.seamless`, config `seamless.yaml`, env prefix
`SEAMLESS_*`; ChromaDB dropped (SQLite BLOB vectors + brute-force cosine); server-rendered
console (no node); single static bearer key (no JWT); fresh data dir. New owner input this
session: **OpenAI is the first-class / default LLM + embedding provider** (Ollama +
Anthropic secondary); Ollama restarted and reachable (`qwen3-embedding:8b` present locally).

Environment: repo empty on `main`, remote `github.com:0spoon/seamless.git`, git user Eitan;
Go 1.25.8, golangci-lint 1.64.8. Module path `github.com/0spoon/seamless` (from remote).

Decisions / divergences:
- go.mod directive `go 1.25` (not `.8`) so any 1.25.x toolchain builds without a download.
- Testing: **testify/require** per plan + v1 (overrides my global "stdlib testing only"
  preference; updated global rule confirms project conventions win on conflict).
- P0 granularity: REWRITE.md P0 is one checkbox; executed as a sequence of green commits
  (skeleton -> config -> core -> store/migration -> events/doctor), one Progress entry each.
  P0 box checked at the phase boundary only.
- HTTP: P0 `/healthz` uses stdlib `net/http`; chi deferred to P2 when routing grows.
- `validate` port (v1 origin `~/repos/seam/internal/validate/validate.go`): dropped v1's
  `UserID()` (no multi-user in v2); kept Path/PathWithinDir/Title/Name; removed the `..`
  rejection from Title only (kept in Name/Path) with a regression test.

**Step 1 — repo skeleton** (`chore(p0): repo skeleton, tooling, conventions, validate port`).
Created go.mod, Makefile, .golangci.yml, .gitignore (gitignores `seamless.yaml` /
`seam-server.yaml` from commit 1), README.md, AGENTS.md, CLAUDE.md, seamless.yaml.example
(OpenAI-first llm config), docs/PLAN.md. Ported `internal/validate` (+tests). Minimal
`cmd/seamlessd` (serve `/healthz`, doctor skeleton, version). `make build` / `go vet` /
`go test ./...` / `golangci-lint run` all green.

**Step 2 — config** (`feat(p0): config loading (yaml + env, static key, budgets, llm)`).
`internal/config`: `Config` (addr, data_dir, mcp.api_key, budgets, llm{openai|ollama|
anthropic}, gardener) with `Defaults()` -> file overlay -> `SEAMLESS_*` env overlay -> `~`
expansion -> `Validate()`. OpenAI is the default provider (chat_model gpt-4o, embedding
text-embedding-3-large/3072). Config search: `$SEAMLESS_CONFIG`, `~/.config/seamless/
seamless.yaml`, `./seamless.yaml`. Wired into `serve` (bind = flag||config) and `doctor`
(config load + api_key/llm warnings). Tests cover file/env precedence, `~` expansion, bad
env ints, validate. Divergence from v1: fresh minimal config (no JWT/auth/chroma). Green.

**Step 3 — core domain types** (`feat(p0): core domain types + ULID`). `internal/core`:
`NewID()` (crypto/rand ULID, never `MustNew`); types Project, Memory (+`MemoryKind`
8-value enum, `Active()`), Session (+`SessionStatus`), Task (+`TaskStatus`, `Valid`/
`Closed`), Trial (+`TrialOutcome`), Event (+`EventKind` set). Pure data, no I/O deps.
Tests: ULID uniqueness/parseability (dropped an incorrect intra-ms ordering assertion --
random-entropy ULIDs only sort across milliseconds), enum validity, Active/Closed. Green.

**Step 4 — store + migration 001** (`feat(p0): sqlite store, migration runner, migration 001`).
`internal/store.Open(dbPath)` sets PRAGMAs via the DSN (`journal_mode(WAL)`,
`foreign_keys(1)`, `busy_timeout(5000)`) so every pooled connection inherits them,
`SetMaxOpenConns(1)`, runs migrations. Ported the migration runner from v1
(`migrations/migrate.go`; dropped the unused PreHook). Migration 001 creates all 13 domain
tables (projects, memories_index, notes_index, embeddings, sessions, tasks, task_deps,
trials, events, retrieval_stats, gardener_proposals, settings, jobs) + a unified
self-contained FTS5 `fts` (managed by the files layer, `porter unicode61`) + indexes.
Helpers `SchemaVersion`/`TableCount`. Wired into serve (healthz pings DB) and doctor
(schema v + table count -> "config + DB ok"). Tests: fresh-DB pragmas/migration, idempotent
reopen, FK enforced end-to-end, FTS insert/match/stem/delete. Divergences: modernc sqlite
v1.53.0 (vs v1 1.46.1); DSN pragmas (vs v1 `db.Exec`) for per-connection robustness. Green;
`serve` + `doctor` smoke-verified (healthz ok, graceful shutdown).

**Step 5 — event log** (`feat(p0): append-only event log write path`). `internal/events`:
`Recorder.Record(ctx, core.Event)` (stamps ULID id + UTC ts when absent, JSON payload,
rejects empty kind) + `Recent(ctx, limit)` (newest-first, `rows.Err` guarded). Added a
canonical fixed-width UTC timestamp format `core.FormatTime`/`ParseTime` (lexically
sortable TEXT timestamps; ParseTime also accepts RFC3339 for imported v1 data). Tests:
stamp/round-trip/payload, explicit id+ts preserved, empty-kind rejected, Recent
ordering+limit; core time round-trip / UTC normalization / lexical sort. Green.

**P0 acceptance (phase boundary, 2026-07-10).** `make build` + `go test ./...` +
`golangci-lint run` all green (5 packages: validate, config, core, store, events).
`seamlessd serve` starts, `/healthz` returns `{"status":"ok"}` with a DB ping, graceful
shutdown on signal verified. `seamlessd doctor` reports config + DB ok (schema v1, 15
tables) with expected empty-key warnings (mcp.api_key, openai.api_key). Migration 001
provisions all 13 domain tables + unified FTS5 for every later phase. Packages deferred to
their phases per plan (files, llm, retrieve, lifecycle, tasks, gardener, mcp, hooks,
console, capture) -- none are P0 scope. **HARD STOP: awaiting owner go-ahead before P1.**

### 2026-07-10 — P1 (Files + import)

Owner go-ahead received ("go ahead, Ollama-for-dev for P1 embeddings"). Mid-phase
the owner added a gitignored `seamless.yaml` with an OpenAI key and asked to use
**OpenAI for embeddings** instead of Ollama; the switch needs no code change (the
config default is already `provider: openai`; the importer and Manager build the
embedder via `llm.NewEmbedder(cfg.LLM)`). Executed as 6 green commits.

**Step 1 — files: frontmatter round-trip + atomic writes** (`feat(p1): files layer
— frontmatter round-trip + atomic writes`). `internal/files` ported from v1
`internal/note`, generalized to two item kinds. `frontmatter.go`: ordered YAML
marshal for memory + note; unknown keys captured into an `Extra` map and re-emitted
(lossless Obsidian-style round-trip); lifecycle fields (`invalid_at`,
`superseded_by`) always emitted (null when unset). `files.go`: Parse/Render for
each kind (RFC3339 timestamps on disk, parsed at the core boundary via
`core.ParseTime`), data-dir-relative path computation
(`memory/{project|_global}/{name}.md`, `notes/{project|inbox}/{slug}.md`), pure-FS
Store guarded by `validate`. `atomic.go`: AtomicWrite (temp+fsync+rename). core:
add `Note` type; add `Extra` passthrough on Memory + Note.

**Step 2 — files: index mirror + unified FTS** (`feat(p1): files layer — SQLite
index mirror + unified FTS`). `index.go`: Indexer upserts memory/note into
`memories_index` / `notes_index` and refreshes the item's row in the unified
self-contained `fts` (delete+insert by ULID `item_id`; no content triggers, since
one virtual table spans both kinds). Tags -> JSON column; nullable columns hold
real NULL; timestamps use the fixed-width `core.FormatTime`. DeleteByFilePath and
ContentHashByFilePath support the reconciler.

**Step 3 — files: watcher + reconciliation** (`feat(p1): files layer — watcher +
startup reconciliation`). `watcher.go`: fsnotify over the two trees (ported from
v1 `internal/watcher`): recursive dir add, debounce+generation, self-write
suppression, plus a new-directory re-scan that closes the create-race so a file in
a brand-new project dir is still indexed. `manager.go`: Manager ties Store +
Indexer + watcher; Start (mkdir+watch+reconcile+run loop); Reconcile (content-hash
skip + orphan deletion via AllFilePaths); write-through WriteMemory/WriteNote/Remove
that suppress the watcher and index synchronously. Dep: fsnotify v1.9.0.

**Step 4 — llm embedding clients** (`feat(p1): llm embedding clients (openai +
ollama), provider factory`). `internal/llm` ported+trimmed from v1 `internal/ai`
(chat/Chroma/task-queue dropped). Embedder interface (Embed -> []float32 + Model);
OpenAIEmbedder + OllamaEmbedder; sentinel errors for graceful P4 lexical fallback;
`NewEmbedder(config.LLM)` selects by provider (Anthropic rejected — no embeddings
API). httptest-only unit tests.

**Step 5 — embeddings store + cosine + embed-on-index** (`feat(p1): embeddings
store + brute-force cosine search + embed-on-index`). `internal/store/embeddings.go`:
LE float32 BLOB encode/decode, Upsert/Delete, and `CosineSearch` (full scan filtered
by model+kinds, dim-mismatch skip, deterministic top-k) — no vector DB. Manager
gains a best-effort embedder: every (re)indexed item is embedded and upserted;
failures log-and-continue so a down embedder never blocks writes (degrades to FTS).
DeleteByFilePath also drops the vector.

**Step 6 — import** (`feat(p1): seamlessd import — migrate Seam v1 data into
Seamless`). `internal/importer` reverses v1's "Knowledge: {kind} - {name}" +
`domain:/project:/session:` tag convention back into real frontmatter (kind from
title, name slugified, semantic project + provenance from tags, structural tags
stripped); normalizes plain notes; lifts `type:trial` notes into `trials` rows
(lab/outcome/Changes/Expected/Actual parsed from markdown, tolerant of v1's
run-together headers); replays `agent_sessions` -> sessions and `agent_tool_calls`
-> `tool.call` events. Idempotent by id (index check + INSERT OR IGNORE).
`seamlessd import --from ~/.seam [--skip briefings] [--embed]`. core: add
`tool.call` event kind.

**P1 acceptance (phase boundary, 2026-07-10).** `make build` + `go test ./...` +
`golangci-lint run` all green (7 tested packages; race-clean on files). Validated
on an announced read-only COPY of live `~/.seam` (live v1 untouched): imported
**72 memories, 29 notes, 6 trials, 43 sessions, 440 events**; a second run skipped
all 590 (idempotent by id). Kind distribution (reference 34 / gotcha 13 / runbook 7
/ protocol 6 / decision 6 / constraint 4 / refuted 2) and semantic-project mapping
(from the `project:` tag, not the `agent-memory` storage bucket) verified; on-disk
v2 frontmatter spot-checked clean. Unified FTS returns sane hits. Full-corpus
OpenAI embedding: **101/101 items** vectorized (3072-dim, text-embedding-3-large,
zero failures once the owner's firewall was opened). Cosine on imported data:
"how do I back up the postgres database to s3" -> `backup-strategy` #1 (0.57);
"firmware antenna matching network for the nordic chip" ->
`nrf54l15-...-antenna-matching-network` #1 (0.59), firmware memories following.
Watcher round-trip (create/edit/delete on disk -> index) verified. Divergences:
embeddings switched to OpenAI mid-phase per owner; imported sessions coerced to
`completed` (historical); tool-call args/results dropped from events (kept
tool/duration/error). **HARD STOP: awaiting owner go-ahead before P2.**

### 2026-07-10 — P2 (Core loop + dogfood)

Entry reconstructed from the commit log (the P2 executor shipped the code but did
not append a progress entry). Commits, in order:
`feat(p2): MCP server + 15 tools with per-session binding`;
`feat(p2): hook endpoints + settings.json installer`;
`feat(p2): wire MCP + hooks into serve; add install-hooks + map-repo`;
`feat(p2): seam CLI (prime, remember, recall, status)`;
`feat(p2): backfill projects registry + runtime repo->project mapping`;
`fix(p2): import top-level notes as inbox (empty project), not filename`.

Net: the minimal core loop is live -- `internal/mcp` (15 tools, static bearer key,
per-connection session binding so project scope is inherited), `internal/hooks`
(session-start briefing + user-prompt-submit matcher) + settings.json installer,
`cmd/seam` CLI (prime/remember/recall/status), and a runtime-evolving
repo->project map that registers a projects-table row the first time an agent
works in a new git repo. The last item (top-level notes imported as inbox rather
than under a filename-derived project) fixed the inbox-note importer bug that
gated P3. Dogfood is live on :8081 with `~/.seamless`; v1 untouched on :8080.
**Owner accepted; P3 kicked off.**

### 2026-07-10 — P3 (Lifecycle + tasks + ambient)

Executed as 6 green commits (build/test/vet/lint clean after each), then an
isolated end-to-end binary smoke on a throwaway config/data-dir/port (never the
live :8081/`~/.seamless`).

**Step 1 -- supersession + provenance** (`feat(p3): memory supersession +
provenance + recall validity filter`). New `internal/lifecycle`: `Supersede`
stamps the old memory `invalid_at` + `superseded_by`, appends a tombstone line to
its file body (source of truth stays honest), and rewrites it out of the active
index. `memory_write` gains `supersedes`; `memory_read` returns `source_session`
provenance and, for a superseded memory (found via new
`store.MemoryByNameIncludingInvalid`), a warning naming its replacement.
**Divergence/bug found:** recall did NOT exclude superseded memories -- their FTS
+ embedding rows persist after `invalid_at` (only the index row is stamped), so
recall now filters on `Memory.Active()` in the hydrate step.

**Step 2 -- ambient sessions + session-end** (`feat(p3): ambient sessions,
session-end hook + harvest, write-scope fallback`). session-start hook creates/
resumes `cc/{prefix}` (metadata `claude_session_id/cwd/source`, scoped to the cwd
project) and appends `Seam session: cc/xxxx (ambient)` to the briefing; subagents
get none. New `POST /api/hooks/session-end` harvests the transcript's last
assistant message (text blocks, cap 2000 runes, `(auto-harvested) ` prefix;
fallback when absent) and completes the session, idempotently. Installer now
installs the 3rd hook. MCP write-scope fallback: an unbound connection attributes
writes to the most recent active `cc/*` session within 6h
(`store.LatestActiveAmbientSession`). `hooks.Handler` gained a `*sql.DB`.

**Step 3 -- tasks v2** (`feat(p3): tasks v2 ready-queue + 4 tools + briefing
line`). `store/tasks.go` implements the ready-queue to the spec: ready = open with
no open/in_progress blocker; done AND dropped unblock; in_progress leaves ready
but still blocks; order oldest-created then id; dangling + cycle rejected at write
time; terminal transitions stamp/clear `closed_at`. `BlockedTasks` surfaces
blockers. MCP `tasks_add/update/ready/list` (ToolCount 15->19); briefing gains a
`Ready tasks: N -- ...` line. **Divergence:** task persistence lives in `store/`
(store-centric codebase), not a separate `internal/tasks`.

**Step 4 -- sibling briefings** (`feat(p3): sibling-project family briefings`).
`project_families` setting; `store.SiblingProjects` (union across families, self
excluded) + `SiblingFindings`; briefing gains a `## Sibling projects` section (<=2
findings, 150-rune snippets) between the memory index and recent findings.
Briefing sections grouped into a `briefingSections` struct.

**Step 5 -- research trials** (`feat(p3): research trials -- lab_open,
trial_record, trial_query`). `store/trials.go` on the trials table (metrics as
native JSON); lab/outcome/project filtered in SQL, exact-match metrics filter in
Go (JSON-normalized so 497 == 497.0). MCP `lab_open` (returns lab history, binds
the lab), `trial_record` (inherits bound lab), `trial_query` (metrics_filter).
ToolCount 19->22.

**Step 6 -- stage pinning** (`feat(p3): pin non-done stage memories in the
briefing`). `ParseStageHeader` reads `Status:`/`Gate:` from a stage body; briefing
pins non-done stages right after constraints and keeps stage-kind out of the
index. `retrieve.Service` gained an optional `MemoryBodyReader` (set to
`files.Store` in serve; index rows carry no body); degrades away when unset.

**P3 acceptance (phase boundary, 2026-07-10).** `make build` + `go test ./...` +
`go vet` + `golangci-lint run` all green (10 tested packages incl. new lifecycle).
No migration needed -- migration 001 already provisioned every P3 table. New unit
tests cover the PLAN.md Phase 2/3 scenarios against v2: supersede A with B ->
A absent from a fresh briefing and recall but readable with a warning; ambient
session create/resume/harvest lifecycle + explicit-overrides-ambient no-op;
`tasks_add` A, B depends_on A -> ready = {A}, complete A -> ready = {B}, cycle
rejected; sibling findings shown only for family members; trial metrics filter
round-trip; stage status pinned. Isolated binary smoke: `doctor` clean,
`install-hooks` writes all 3 hooks, `mcp_tools=22`, session-start created ambient
`cc/smoke123` (project auto-registered) with the ambient briefing line,
session-end harvested `(auto-harvested) ...` and completed the session.
**HARD STOP: awaiting owner review before P4.**

### 2026-07-10 — P4 (Gardener + retrieval quality)

Owner go-ahead received ("start P4 gardener"). Executed as 8 green commits
(build/test/vet/lint clean after each), then an isolated binary smoke on a
throwaway config/data-dir/port (never the live :8081/`~/.seamless`). No
migration -- migration 001 already provisioned retrieval_stats,
gardener_proposals, and jobs.

**Step 1 -- retrieval_stats from events** (`feat(p4): retrieval_stats derived
from the event log`). `store/retrieval_stats.go`: `RebuildRetrievalStats`
materializes the table from the append-only log (`retrieval.injected` ->
inject_count/last_injected_at per item id, read from BOTH the item_id column and
the payload `item_ids` array; `memory.read` -> read_count/last_read_at),
`GetRetrievalStat`, and `StaleMemories(cutoff)` (active memories with no
update/injection/read since a cutoff, via LEFT JOIN). Chosen design: a
rebuildable projection (the gardener rebuilds it at the top of each pass), not
scattered incremental bumps -- the event log is the single source of truth.
**Gap found:** briefing/prompt-hook injections record NO item_ids (only
`{hook, claude_session_id}`), so only recall + memory_read feed per-item stats;
staleness therefore measures "pulled via recall/read", which is the intended
liveness signal.

**Step 2 -- llm chat client** (`feat(p4): llm chat client (openai, ollama,
anthropic) for digests`). `llm/chat.go`: minimal `Chat` (Complete(system, user)
-> text) + `NewChatClient` factory. Unlike embeddings, all three providers do
chat (OpenAI /chat/completions, Ollama /api/chat, Anthropic /v1/messages).
httptest-only tests.

**Step 3 -- gardener passes** (`feat(p4): gardener propose-only passes (dedup,
staleness, digest)`). New `internal/gardener` + `store/gardener.go`
(gardener_proposals CRUD + `ActiveMemoryVectors`/`AllActiveMemories`/
`CompletedSessionsSince`). RunOnce rebuilds stats, then: dedup (pairwise cosine
over stored vectors, >=0.88 -> merge proposal, keep newer / drop older;
skipped without an embedder), staleness (no activity in StalenessDays -> archive
proposal; constraints + stages never archived; a memory named by a [[link]] in
another memory's body is protected), digest (completed sessions in the trailing
DigestDays grouped by project, summarized by the chat client into one digest
proposal per project per calendar month; skipped without a chat client). Every
payload carries a stable `key`; `AllProposalKeys` (across every status) dedups so
an applied/dismissed suggestion never returns. config: gardener
interval/threshold/staleness/digest knobs + env. **Divergence:** digest computed
inline in the pass (synchronous chat call), not via the `jobs` queue -- simpler,
testable, and the gardener is already a ticker. Acceptance test seeds a fixture
and asserts RunOnce produces all three proposal kinds and is idempotent on a
second pass.

**Step 4 -- ticker + serve** (`feat(p4): gardener ticker + wire into serve`).
`Service.Start`: one pass ~20s after boot, then every Interval, stopping on ctx
cancel; each pass under a 5-minute timeout, best-effort. serve builds a
best-effort chat client and starts the gardener when enabled.

**Step 5 -- gardener MCP tools** (`feat(p4): gardener_proposals + gardener_apply
MCP tools`). `lifecycle.Archive` (retire a memory: invalid_at, no successor,
archive tombstone) + gardener `Apply`/`Dismiss` (archive -> retire; merge ->
supersede drop by keep; digest -> write the summary as a note; a failed effect
leaves the proposal pending). Two tools; ToolCount 22->24. Server now tracks
registered tool names (`NumTools`) for the doctor assertion.

**Step 6 -- capture_url** (`feat(p4): capture_url tool + SSRF-safe URL fetch`).
Ported `internal/capture` from v1 (URL path only; voice dropped): DNS-rebinding-
safe dialer rejecting private/loopback addresses, redirect-scheme validation,
2MB cap, readable-content extraction. `capture_url` saves the page as a note.
Added golang.org/x/net + golang.org/x/text. ToolCount ->25.

**Step 7 -- recall link expansion** (`feat(p4): recall link expansion + shared
core.WikiLinks`). `core.WikiLinks` (parse/normalize/dedup [[name]] refs), routed
the gardener's reference scan through it too. Recall scans the top fused memory
hits' bodies for links and pulls each linked active in-scope memory into the
candidate set as a third RRF signal (source `link`); no-op without a body reader.

**Step 8 -- usage_summary + doctor** (`feat(p4): usage_summary tool + doctor
asserts 26 tools`). `store.GetUsageSummary` (memory/note/session/task counts,
retrieval totals + most-injected, pending proposals, events by kind).
`usage_summary` rebuilds stats then returns it. doctor builds a throwaway MCP
server and asserts NumTools == ToolCount (26). ToolCount ->26.

**P4 acceptance (phase boundary, 2026-07-10).** `make build` + `go test ./...` +
`go vet` + `golangci-lint run` all green (13 tested packages incl. new gardener +
capture). Acceptance scenarios: seeded-fixture RunOnce produces merge + archive +
digest and re-proposes nothing on a second pass (reference-aware + kind-aware
protections verified: a linked old memory and an old constraint are NOT
archived); recall returns results with the embedder nil (degrades to FTS), and
link expansion is a no-op without a body reader. Isolated binary smoke: `doctor`
clean with `mcp_tools: 26 tools registered`; serve boots with `gardener enabled`
(interval 1h, dedup 0.88, staleness 90d), healthz ok, embeddings + digests
degrade cleanly when unconfigured, graceful shutdown. **HARD STOP: awaiting owner
review before P5.**

### 2026-07-10 — P5 (Console + CLI observability)

Owner go-ahead received ("merged P4 to main, now start P5"). P4 fast-forwarded
to main (HEAD 3a71869); P5 executed on branch `p5-console` as 11 green commits
(build/test/vet/lint clean after each), then a live browser walkthrough of every
page against a COPY of the dogfood `~/.seamless` (the live :8081 instance was
never touched). No migration -- migration 001 already provisioned every table
the console reads.

New `internal/console`: server-rendered `html/template` + one embedded CSS file
via `go:embed`, vanilla-JS SSE client, no node/build step. Auth accepts EITHER a
session cookie (browser, set at `/console/login` -- the cookie stores a SHA-256
of the key, never the key) OR the static bearer key (so the `seam` CLI reaches
the same routes). Every page also serves `?format=json`, which is how the CLI's
`sessions` command reads data without adding a 27th MCP tool (ToolCount stays 26).

Commits, in order:
- **scaffold** (`feat(p5): console scaffold`): auth, layout, overview page from
  GetUsageSummary + the event log, wired into serve. `store.GetNavCounts`.
- **Sessions** (`feat(p5): console Sessions page`): list (all/active/completed
  filter) + detail (metadata, findings, event timeline, per-session
  tool-call/read/write counts + a read-after-inject metric). `store.ListSessions`,
  `events.BySession` (scanEvents factored out of Recent).
- **Memories** (`feat(p5): console Memories browser + archive`): project (global
  first) -> kind groups, per-memory inject/read counts, edit link
  (`vscode://file` absolute path via `template.URL`), archive button
  (lifecycle.Archive -- the console's one memory write), and a collapsible
  archived/superseded section with resolved supersession chains.
  `store.AllMemoriesIncludingInvalid` + `AllRetrievalStats` (one bulk query).
- **Retrieval** (`feat(p5): console Retrieval page`): read-after-inject headline,
  per-kind rate meters, 14-day injection trend, most-injected + stale (90d)
  lists. `store.RetrievalByKind/TopInjectedMemories/InjectionsByDay`.
- **Tasks** (`feat(p5): console Tasks page`): ready-first, then in-progress,
  blocked (with blocker badges), and a collapsible recently-closed list, across
  all projects. `store.AllReadyTasks/AllBlockedTasks/AllTasksByStatus`.
- **Gardener** (`feat(p5): console Gardener page`): typed proposal cards
  (archive/merge/digest) with previews; Apply/Dismiss POST to
  gardener.Apply/Dismiss; a failed apply leaves the proposal pending and flashes
  the error.
- **Settings** (`feat(p5): console Settings page`): read-only view of data dir,
  budgets, gardener knobs, projects, the learned repo->project map, and families.
- **SSE** (`feat(p5): console SSE live feed`): `events.Recorder` gains a
  best-effort pub/sub (a slow subscriber drops rather than blocks the write
  path); `/console/events` streams JSON frames; every page's layout runs a small
  EventSource client that pulses the brand dot and shows a "new activity" pill.
  Race-clean.
- **CLI** (`feat(p5): seam CLI observability`): `usage` (usage_summary), `ready
  [--blocked]` (tasks_ready), `task list|add|done|start|drop|reopen`, `capture`
  (capture_url), `sessions [<id>]` (console JSON, bearer), `doctor` (healthz +
  MCP tools/list count == 26 + project_list). Verified end-to-end live.
- **doctor** (`feat(p5): complete seamlessd doctor`): adds hooks x3
  (`hooks.InstalledStatus`, checked in `~/.claude` and `./.claude`), gardener
  ticker config, and an embedder reachability probe (skipped when the provider
  credential is missing). All three degrade to warnings, never failures.
- **fix** (`fix(p5): cap read-after-inject rate at 100%`): reads can exceed
  injections (hook injections record no per-item ids), so the raw ratio is
  clamped to a sensible coverage percentage.

`install-hooks` already installed all three hooks (P2/P3); doctor now reports
their status. **Divergences:** console-support store queries live in
`store/console.go` + existing per-domain files (store-centric codebase), not a
new package; the CLI `sessions` command uses the console's content-negotiated
JSON rather than a new MCP tool (keeps ToolCount at 26); the memory edit link
assumes a VS Code-family editor (`vscode://file`), with the absolute path shown
as the title so it is informative regardless.

**P5 acceptance (phase boundary, 2026-07-10).** `make build` + `go test ./...` +
`go vet` + `golangci-lint run` all green (console + events race-clean). Live
walkthrough on a copy of dogfood data (73 memories, 28 notes, 49 sessions):
every page rendered correctly -- Overview roll-up, Sessions list+detail (real
findings + timeline), Memories grouped by project/kind with archive + edit,
Retrieval (per-kind meters, trend, stale), Tasks (ready/blocked with a real
dependency), Gardener (all three seeded card types), Settings (14 projects, 15
repo mappings). SSE verified: emitting an event surfaced the live "new activity"
pill. `seamlessd doctor` reports 26 tools, hooks 3/3 after install, gardener
config, and the embedder credential state. `seam doctor/usage/ready/task/
sessions/capture` all verified against a live throwaway server. **HARD STOP:
awaiting owner review before P6 (cutover).**

### 2026-07-10 — P6 cutover (full same-day, no parallel-run week)

Owner merged P5 to `main`, then elected a **complete same-day cutover** rather
than the planned one-week parallel run. The live env had already diverged from
the plan text (owner had manually pointed global hooks + MCP at Seamless during
dogfood), so P6 was smaller than written.

**Owner decisions (this session):** (1) keep Seamless on **:8081** -- do NOT take
:8080; v1 is decommissioned and 8080 simply freed. (2) Convert everything to
Seamless including the MW75 hardware repo, then disable v1 and free the port,
**preserving all v1 data/DB/repo** (disable only, no deletion).

**Repo deliverables (committed):**
- `chore(p6)`: `make install-service` + `deploy/launchd` plist template
  (`__BINARY__`/`__CONFIG__`/`__LOG__`) formalizing the hand-made
  `org.thereisnospoon.seamless` LaunchAgent; idempotent bootout+bootstrap reload;
  symmetric `uninstall-service`. (plist render validated via `plutil`; the
  bootstrap action itself is gated behind explicit owner approval.)
- `feat(p6)`: ported the `/seam-onboard` skill from v1 to Seamless -- discovery
  reads `SEAMLESS_MCP_API_KEY` / plist `SEAMLESS_CONFIG` / `seamless.yaml`
  (`mcp.api_key`+`addr`), registers the `seamless` MCP server at user scope on
  :8081, writes a `mcp__seamless__` CLAUDE.md block (recall as the single search
  tool; no `notes_search`/`context_gather`/`decision_record`). Same skill name +
  markers so it overwrites v1 in place. `make install-onboard-skill`.

**Live cutover (all reversible, backups in session scratch):**
- Delta `import --from ~/.seam`: idempotent-by-id; parity confirmed (v1's 107
  note-tree files == 73 memories + 29 notes + 6 trials in v2, + 50 sessions;
  final re-run 0 new / 590 skipped). Firmware knowledge verified recallable via
  `recall` scoped to `mw75-neuro-firmware`.
- `install-hooks` on `~/.claude/settings.json` added the missing global
  **SessionEnd** hook. **Gotcha:** the installer keys idempotency on a
  `seamless_managed: true` marker and does NOT adopt pre-existing *unmarked*
  seamless-URL hooks -- it duplicated SessionStart/UserPromptSubmit. Fixed by
  dropping the stale unmarked duplicates; installer now reports 3/3 unchanged.
  (Follow-up candidate: teach `hooks.Install` to adopt/dedupe unmarked entries.)
- Removed the last project-scoped `seam` (:8080) MCP entry
  (`hegemon/firmware/mw75neuro`) from `~/.claude.json`; global MCP is now
  `seamless`-only. Repo was already mapped (`.../hegemon/firmware ->
  mw75-neuro-firmware`), so nothing writes to v1 anymore.
- Installed the Seamless `/seam-onboard` skill (replacing v1's) and rewrote the
  `~/.claude/CLAUDE.md` block canonically (byte-identical to the committed
  SKILL.md).
- **v1 decommissioned:** `launchctl bootout` of `com.seam.seamd` +
  `com.seam.chroma`; both plists renamed to `*.plist.disabled` (no login
  reload). Port **8080 freed**, processes gone. `~/.seam`, `~/repos/seam`,
  `seam.db`, chroma data all preserved -- restore = rename plists back +
  bootstrap.

**Acceptance:** `make doctor` green as the sole system -- 26 tools, 3/3 hooks,
embedder reachable, DB ok, gardener enabled. Seamless healthy on :8081; 8080
free. The "one full day zero fallbacks" soak is now ongoing normal use rather
than a gated week.
