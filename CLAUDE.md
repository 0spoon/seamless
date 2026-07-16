# CLAUDE.md

Quick-reference for AI assistants working in the Seamless repository. For coding
conventions see `AGENTS.md`.

## What is Seamless?

Seamless is a local-first agent memory and coordination substrate. Its clients are
AI agents (Claude Code and friends); the human is an observer/editor. It provides
persistent memory with a lifecycle (supersession, provenance, arbitration, a
gardener), ambient sessions via Claude Code hooks, a dependency-aware task
ready-queue, research trials, hybrid recall, and a read-mostly observability
console. Go backend, no CGO, single binary + CLI.

It is a ground-up rebuild of Seam v1 (a private predecessor, read-only
reference). As of the P6 cutover (2026-07-10) Seamless is the sole agent-memory system, serving on
port 8081 with data dir `~/.seamless` and env prefix `SEAMLESS_*`. v1 is
decommissioned (launchd services disabled, port 8080 freed) with `~/.seam`
preserved read-only as a fallback archive.

## Project structure

```
cmd/seamlessd/     server daemon: serve, doctor, import, install-hooks, map-repo,
                   family, console-open
cmd/seam/          headless CLI (agents + owner observability)  [P2/P5]
cmd/docsgen/       docs site generator: docs-src/ -> docs/docs/ (see docs/README-site.md)
docs-src/          docs site markdown + nav.yaml (source; docs/docs/ is output)
internal/core/     domain types: Project, Memory, Session, Task, Trial, Event
internal/config/   single YAML + env config (static key, budgets, briefing tunables, llm)
internal/store/    SQLite: schema, FTS5, embeddings (BLOB + cosine), migrations
internal/events/   append-only event log; SSE fan-out; retrieval stats
internal/validate/ path/title/name guards
internal/files/    markdown layer: frontmatter, atomic writes, watcher     [P1]
internal/markdown/ agent markdown -> sanitized console HTML (goldmark + bluemonday)
internal/llm/      OpenAI (default), Ollama, Anthropic chat + embeddings   [P1]
internal/retrieve/ briefing assembler, prompt-context matcher, recall (RRF),
                   console search                                          [P2/P4]
internal/lifecycle/ supersession, arbitration, provenance                  [P3]
# (the dependency-aware ready-queue + lease-based claiming live in
#  internal/store/tasks*.go -- there is no internal/tasks package)
internal/gardener/ dedup / staleness / digest / stale-plan passes, plus the
                   request- and split-driven proposals                     [P4]
internal/plans/    captured CC plan vocabulary: tags, statuses, tracking task
internal/mcp/      MCP tools (streamable HTTP, static bearer key); see ToolCount [P2+]
internal/hooks/    session hooks + CC plan-mode capture (PostToolUse etc.) [P2/P3]
internal/console/  server-rendered observability UI (html/template + SSE)  [P5]
internal/capture/  SSRF-safe URL fetch                                     [P4]
internal/importer/ one-shot import of the v1 (~/.seam) snapshot
```

Bracketed tags mark the phase that introduces the package; unbuilt ones do not
exist yet.

## Common commands

```bash
make build      # ./bin/seamlessd + ./bin/seam (touches nothing live)
make install    # build + snapshot binaries/config to ~/.local/bin +
                # ~/.config/seamless, repoint the service and hooks, restart
make test       # unit tests
make test-race  # unit tests with the race detector
make check      # the full gate: build + vet + fmt-check + docs-check + lint + test-race
make check-fast # the pre-commit subset (no test-race); .githooks/pre-commit runs it
make lint       # golangci-lint
make vet        # go vet
make fmt        # gofmt tracked files -- NOT `gofmt -w .`, which rewrites other
                # agents' worktrees under dot-dirs that go's ./... skips
make run        # build + serve on 127.0.0.1:8081
make doctor     # build + config/DB self-checks
make console    # open the console in a browser, pre-authenticated
make clean      # remove bin/ and coverage files

make docs       # regenerate the docs site (docs-src/ -> docs/docs/, committed)
make docs-check # fail if the committed docs site is stale (runs inside `check`)
make docs-serve # regenerate + serve the site at 127.0.0.1:8899/docs/

# single test
go test ./internal/validate -run TestTitle -v
```

## Tech stack

| Layer | Choice |
|---|---|
| Language | Go 1.25+ (no CGO) |
| Database | SQLite (`modernc.org/sqlite`, WAL, FTS5) |
| Vectors | float32 BLOBs in SQLite + brute-force cosine (no ChromaDB) |
| LLM/embeddings | OpenAI (default, first-class), Ollama, Anthropic |
| IDs | ULID (`oklog/ulid/v2`), never UUID |
| Auth | single static bearer key, localhost bind (no JWT) |
| Console | `html/template` + vanilla JS + SSE (no node/React) |
| Tests | testify/require, table-driven, fresh/in-memory SQLite |

## Configuration

Single YAML file (`$SEAMLESS_CONFIG`, `~/.config/seamless/seamless.yaml`, or
`./seamless.yaml`; see `seamless.yaml.example` for every key) with `SEAMLESS_*`
env overrides -- env wins over file, file over defaults. The `briefing:` block
tunes what the SessionStart `<seam-briefing>` auto-injects (section counts,
recency filters, family cross-over, hard-cap multiplier); those knobs are also
editable live in the console (Settings -> Briefing injection), which stores a
runtime override in the DB that wins over file/env until reset and applies from
the next session start without a daemon restart. `gardener.session_idle_minutes`
is the single live/idle threshold shared by the session reaper and the console.

## Storage layout

```
~/.seamless/
  seam.db                          SQLite: indexes, sessions, tasks, trials, events, embeddings, ...
  memory/{project|_global}/{name}.md   one memory per file (source of truth)
  notes/{project|_global}/{slug}.md    work artifacts (source of truth)
```

Files are the source of truth for durable knowledge; the DB is the record for
high-churn state (sessions, tasks, trials, events, telemetry) and a rebuildable
index for the files.

## Memory frontmatter

```yaml
---
id: 01K...                 # ULID
kind: gotcha               # constraint|runbook|protocol|gotcha|decision|refuted|reference|stage
name: chroma-boot-race
description: one line, <=150 chars -- the ONLY text shown in indexes
project: seam              # empty = global
created: 2026-07-10T18:00:00Z
updated: 2026-07-10T18:00:00Z
valid_from: 2026-07-10T18:00:00Z
invalid_at: null           # set on supersession/archive; invalid memories leave indexes
superseded_by: null        # ULID of the replacement
source_session: cc/ab12cd34
tags: [x, y]
---
body markdown
```

## MCP surface

The authoritative count is `internal/mcp.ToolCount` (asserted by doctor and by
`catalog_test`); `mcp.Catalog()` is the same list as data, and the docs site
renders its reference from it. Do not transcribe a number here -- three different
stale counts is exactly how this section drifted before.

sessions (`session_start/update/end`), memory (`memory_write/append/read/delete`,
write carries `supersedes`), discovery (`recall` -- the only search tool, RRF-fused),
notes (`notes_create/read/update/append/delete`), projects (`project_list/create`),
tasks (`tasks_add/update/ready/list` -- `tasks_list id=<id>` loads a single task
by its globally-unique id -- plus `tasks_claim`/`tasks_release` for
atomic lease-based claiming), research lab (`lab_open`, `trial_record`,
`trial_query`), gardener (`gardener_proposals`, `gardener_apply`, plus the
natural-language `gardener_request` and the project-split planner
`gardener_split` -- both LLM-backed, and both only ever propose), utility
(`capture_url`, `usage_summary`). Project and session are inherited from the
session binding; agents in mapped repos rarely pass `project` explicitly.

## Plans as composition

A "plan" is not a primitive -- it is a composition keyed by `plan:<slug>`:

- **Narrative + supporting knowledge**: one primary note, plus any further notes
  tagged `plan:<slug>`, so the next agent inherits the design and its supporting
  context. `notes_create` already accepts `tags`.
- **Steps**: tasks created with `plan:<slug>` (`tasks_add plan=...`). Plan-step
  tasks are excluded from the default `tasks_ready`/`tasks_list` (no queue
  pollution) and surfaced with `plan=<slug>`.
- **Claiming**: `tasks_claim` atomically moves a ready task to `in_progress` with
  a lease (default 900s); re-claiming refreshes the lease (heartbeat); an expired
  lease is reclaimable. `tasks_release` (or closing the task, or `session_end`)
  frees it. The briefing surfaces each active plan as a `PLAN: <slug> -- ...` line.

Claude Code plan mode feeds this automatically (`internal/hooks` capture +
`internal/plans` vocabulary): plan-file saves upsert a `cc-plan-<basename>` note
(`plan-status:draft|presented|approved|abandoned` tag), planning subagents cache
as `cc-agent-<id>` notes in the composition, approval creates the tracking task,
and the briefing lists unapproved captures as `PLAN (awaiting approval)` lines.
Owner surfaces: `/console/plans` and `seam plan list|show|check|approve` (check =
git-stamp staleness; approve = escape hatch when CC skips the approval hook).

## Working here

- Conventions live in `AGENTS.md`. Read it before writing code.
