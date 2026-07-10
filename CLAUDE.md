# CLAUDE.md

Quick-reference for AI assistants working in the Seamless repository. For coding
conventions see `AGENTS.md`; for the execution plan see `docs/PLAN.md`.

## What is Seamless?

Seamless is a local-first agent memory and coordination substrate. Its clients are
AI agents (Claude Code and friends); the human is an observer/editor. It provides
persistent memory with a lifecycle (supersession, provenance, arbitration, a
gardener), ambient sessions via Claude Code hooks, a dependency-aware task
ready-queue, research trials, hybrid recall, and a read-mostly observability
console. Go backend, no CGO, single binary + CLI.

It is a ground-up rebuild of Seam v1 (`~/repos/seam`, READ-ONLY reference). v1
keeps running on port 8080 with data dir `~/.seam` throughout the rewrite;
Seamless develops on port 8081 with data dir `~/.seamless` and env prefix
`SEAMLESS_*`, so the two never cross-configure during the parallel run.

## Project structure

```
cmd/seamlessd/     server daemon: serve, doctor, import, install-hooks
cmd/seam/          headless CLI (agents + owner observability)  [P2/P5]
internal/core/     domain types: Project, Memory, Session, Task, Trial, Event
internal/config/   single YAML + env config (static key, budgets, llm)
internal/store/    SQLite: schema, FTS5, embeddings (BLOB + cosine), migrations
internal/events/   append-only event log; SSE fan-out; retrieval stats
internal/validate/ path/title/name guards
internal/files/    markdown layer: frontmatter, atomic writes, watcher     [P1]
internal/llm/      OpenAI (default), Ollama, Anthropic chat + embeddings   [P1]
internal/retrieve/ briefing assembler, prompt-context matcher, recall (RRF) [P2/P4]
internal/lifecycle/ supersession, arbitration, provenance                  [P3]
internal/tasks/    dependency-aware ready-queue                            [P3]
internal/gardener/ dedup / staleness / digest proposals                   [P4]
internal/mcp/      26 MCP tools (streamable HTTP, static bearer key)       [P2+]
internal/hooks/    SessionStart / UserPromptSubmit / SessionEnd endpoints  [P2/P3]
internal/console/  server-rendered observability UI (html/template + SSE)  [P5]
internal/capture/  SSRF-safe URL fetch                                     [P4]
```

Bracketed tags mark the phase that introduces the package; unbuilt ones do not
exist yet.

## Common commands

```bash
make build      # build ./bin/seamlessd
make test       # unit tests
make test-race  # unit tests with the race detector
make lint       # golangci-lint
make vet        # go vet
make fmt        # gofmt -w .
make run        # build + serve on 127.0.0.1:8081
make doctor     # build + config/DB self-checks
make clean      # remove bin/ and coverage files

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

## Storage layout

```
~/.seamless/
  seam.db                          SQLite: indexes, sessions, tasks, trials, events, embeddings, ...
  memory/{project|_global}/{name}.md   one memory per file (source of truth)
  notes/{project|inbox}/{slug}.md      work artifacts (source of truth)
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

## MCP surface (target: 26 tools)

sessions (`session_start/update/end`), memory (`memory_write/append/read/delete`,
write carries `supersedes`), discovery (`recall` -- the only search tool, RRF-fused),
notes (`notes_create/read/update/append/delete`), projects (`project_list/create`),
tasks (`tasks_add/update/ready/list`), research lab (`lab_open`, `trial_record`,
`trial_query`), gardener (`gardener_proposals`, `gardener_apply`), utility
(`capture_url`, `usage_summary`). Project and session are inherited from the
session binding; agents in mapped repos rarely pass `project` explicitly.

## Working here

- `docs/PLAN.md` is the source of truth for what to build and in what order.
  Work one phase at a time; a phase boundary is a hard stop for owner review.
- Conventions live in `AGENTS.md`. Read it before writing code.
- v1 (`~/repos/seam`) is read-only reference. Port packages marked `[PORT]` in the
  plan with attribution; do not modify, run, or commit against v1.
