# Seamless

Seamless is a local-first agent memory and coordination substrate: persistent
memory, sessions, tasks, and research trials for the AI agents you run (Claude
Code and friends), with a read-mostly observability console for the human.

It is a ground-up rebuild of Seam (v1).

## Status

Cutover complete (Phase 6, 2026-07-10): Seamless is the sole agent-memory system,
serving on port `8081` with data dir `~/.seamless` (env prefix `SEAMLESS_*`). v1
Seam is decommissioned -- its launchd services are disabled and port `8080` is
freed, with `~/.seam` preserved read-only as a fallback archive.

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

## Deployment

There are two ways to run the daemon + Claude Code hooks. Both drive the same
single instance (port `8081`, data dir `~/.seamless`), so only one is active at a
time -- installing prod replaces dev, and vice versa.

**Dev (default)** runs the service and hooks straight from this working tree, so
a rebuild takes effect on the next restart. Fast to iterate -- but `make build`,
a branch switch, or moving the repo changes what the live service and the global
SessionStart hook execute:

```
make install-service   # launchd service -> ./bin/seamlessd + ./seamless.yaml
make install-hooks      # SessionStart/UserPromptSubmit/SessionEnd -> ./bin/seam
```

**Release/prod** snapshots the binaries and config to stable, working-tree-
independent locations, then points launchd and the hooks at the copies. Survives
rebuilds, branch switches, and a moved or cleaned repo:

```
make install-prod                    # -> ~/.local/bin + ~/.config/seamless/seamless.yaml
make install-prod PREFIX=/opt/seam   # custom prefix (binaries land in $PREFIX/bin)
make uninstall-prod                  # remove prod service + binaries (config kept)
```

`install-prod` copies `seamless.yaml` only when the destination is absent, so it
never clobbers an edited prod config; delete the copy to re-seed. It lands in
`~/.config/seamless/`, one of the paths `seam` already searches, so the hooks
resolve config from any directory. To return to dev, run `make install-service &&
make install-hooks`.

Note on the session lifecycle hooks: SessionStart and SessionEnd are installed as
`command` hooks that shell out to `seam hook <event>`. Claude Code only runs
`command`/`mcp_tool` hooks for SessionStart (an `http` one is silently ignored),
and although SessionEnd does support `http`, at process exit the fire-and-forget
request races the teardown and the ambient-session harvest often never lands, so
it too runs as a `command` hook Claude Code waits on. UserPromptSubmit fires
mid-turn where `http` is reliable and stays an `http` hook.
