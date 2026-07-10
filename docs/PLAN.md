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

- [ ] **P0 — Skeleton (target: 1-2 days).** New repo, module, Makefile (build/test/lint/run), config (yaml + env, static key, budgets), `internal/store` with migration runner (pattern from v1) and migration 001 (all tables above), event log write path, `/healthz`, doctor skeleton. Port `validate`. Acceptance: `make test` green; server starts; doctor reports config + DB ok.
- [ ] **P1 — Files + import (2-3 days).** `internal/files`: memory/note frontmatter round-trip, atomic writes, watcher + reconciliation (port), FTS + embeddings jobs (brute-force cosine search working with Ollama/OpenAI embedder). `seamlessd import --from ~/.seam`: old memory notes (parse `"Knowledge: {cat} - {name}"` titles + `domain:/project:/session:` tags into real frontmatter), plain notes (normalize frontmatter), sessions + agent_tool_calls (as historical sessions/events), old trial notes (parse sections into `trials` rows), SKIP the briefings project. Acceptance: import on a COPY of prod data; counts reported and spot-checked (76 memories, ~28 notes, 42 sessions); FTS and cosine search return sane results; editing a memory file on disk round-trips through the watcher.
- [ ] **P2 — Core loop + dogfood (3-4 days).** MCP server (static key) with the minimal loop: `session_start/update/end` (with binding + write-scope inheritance), `memory_write/append/read/delete` (arbitration hint from day one — port dedupHint logic onto cosine search), `recall` (semantic + FTS, simple fusion), `notes_create/read/update/append/delete`, `project_list/create`. Hooks: session-start briefing (constraints + memory index + findings, token-budgeted) and user-prompt-submit (ported matcher). CLI: `prime, remember, recall, status`. Dogfood switch (owner-confirmed): add a SECOND user-scope MCP server named `seamless` pointing at :8081 in `~/.claude.json` (leave the v1 `seam` entry untouched), and install the v2 hooks into THIS repo's project-scoped `.claude/settings.json` only — v1's global hooks will also fire here with their small unmapped-repo fallback briefing, which is acceptable during dogfood. Acceptance: a real Claude Code session in the seamless repo gets a v2 briefing, writes/recalls memories; old Seam untouched for all other repos. DOGFOOD STARTS HERE — every subsequent phase is built with v2 as this repo's memory system.
- [ ] **P3 — Lifecycle + tasks + ambient (3-4 days).** Bi-temporal supersession (`supersedes` param, validity filters everywhere, read warnings), provenance stamping, SessionEnd hook + ambient sessions + harvest (PLAN.md 3.1 contract), tasks v2 ready-queue + 4 tools + briefing line (build to the "Ready-queue semantics" spec above), sibling-family briefing section, `trial_record/trial_query/lab_open` on the trials table with native metrics filtering, `stage` kind pinned in briefings. Acceptance: PLAN.md Phase 2/3 verification scenarios, run against v2.
- [ ] **P4 — Gardener + retrieval quality (2-3 days).** Gardener ticker (propose-only: dedup >=0.88, staleness 90d via retrieval_stats, monthly session digests via LLM job; reference-aware protection; apply/dismiss via 2 MCP tools + console later), RRF recall (semantic + FTS + link expansion, k=60, validity- and budget-aware), retrieval_stats from events, `capture_url` + `usage_summary` tools. Tool count now 26 — assert in doctor. Acceptance: seeded-fixture gardener run produces all three proposal kinds; recall degrades to lexical with the embedder down.
- [ ] **P5 — Console + CLI observability (3-4 days).** All console pages + SSE; CLI `sessions/usage/ready/task/capture/doctor`; `install-hooks` for v2; doctor complete (key, hooks x3, tool count 26, gardener ticker, embedder reachability). Acceptance: owner walkthrough of every console page against live dogfood data; screenshots recorded.
- [ ] **P6 — Cutover (1-2 days + a parallel-run week).** Final `import` delta run (re-import anything old Seam accrued during the rewrite; imports are idempotent by id/name). Switch the global hooks + MCP registration to Seamless for ALL repos (installer handles it; remove the project-scoped dogfood hooks and rename/remove the old `seam` MCP entry), keep old seamd running read-only for one week as fallback, then stop and disable the old service. Archive the v1 repo (history + PLAN.md + review notes are the design record). Rename/move: Seamless takes over port 8080, data dir stays `~/.seamless`, `make install-service` for Seamless, update `~/.claude/CLAUDE.md` Seam section and replace the `/seam-onboard` skill from the Seamless docs. Acceptance: `make doctor` green on v2 as the sole system; one full day of normal multi-repo agent work with zero fallbacks to v1.

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
