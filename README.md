# Seamless

Seamless is a local-first agent memory and coordination substrate: persistent
memory, sessions, tasks, and research trials for the AI agents you run (Claude
Code and friends), with a read-mostly observability console for the human.

It is a ground-up rebuild of Seam (v1). The design and phase-by-phase execution
plan live in [`docs/PLAN.md`](docs/PLAN.md).

## Status

Cutover complete (Phase 6, 2026-07-10): Seamless is the sole agent-memory system,
serving on port `8081` with data dir `~/.seamless` (env prefix `SEAMLESS_*`). v1
Seam is decommissioned -- its launchd services are disabled and port `8080` is
freed, with `~/.seam` preserved read-only as a fallback archive. See
[`docs/PLAN.md`](docs/PLAN.md) for the phase history.

## Architecture (target)

| Package | Responsibility |
|---|---|
| `cmd/seamlessd/` | server daemon (`serve`, `doctor`, `import`, `install-hooks`) |
| `cmd/seam/` | headless CLI for agents and owner observability |
| `internal/core/` | domain types (Project, Memory, Session, Task, Trial, Event) |
| `internal/store/` | SQLite: schema, FTS5, embeddings (BLOB vectors + brute-force cosine), migrations |
| `internal/files/` | markdown layer: memory/note files, frontmatter, watcher |
| `internal/llm/` | OpenAI (default), Ollama, Anthropic chat + embeddings |
| `internal/retrieve/` | briefing assembler, prompt-context matcher, recall (RRF) |
| `internal/lifecycle/` | supersession, arbitration, provenance |
| `internal/gardener/` | scheduled dedup / staleness / digest proposals |
| `internal/tasks/` | dependency-aware ready-queue |
| `internal/mcp/` | 26 MCP tools (streamable HTTP, static bearer key) |
| `internal/hooks/` | SessionStart / UserPromptSubmit / SessionEnd endpoints |
| `internal/console/` | server-rendered observability UI (html/template + SSE) |
| `internal/events/` | append-only event log; SSE fan-out; retrieval stats |
| `internal/capture/` | SSRF-safe URL fetch |
| `internal/validate/` | path / title guards |
| `internal/config/` | single YAML + env config |

## Development

```
make build     # build ./bin/seamlessd
make test      # unit tests
make lint      # golangci-lint
make doctor    # config + DB self-checks
make run       # start the server on 127.0.0.1:8081
```

Go 1.25+, no CGO, pure-Go SQLite (`modernc.org/sqlite`). Configuration lives in a
gitignored `seamless.yaml` (see `seamless.yaml.example`); every key also has a
`SEAMLESS_*` environment override.
