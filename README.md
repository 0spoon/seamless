# Seamless

Local-first memory and coordination substrate for AI coding agents.

Seamless gives a fleet of agents (Claude Code and any MCP-compatible client) a
shared, durable memory and a way to divide work without colliding: memories with
a supersession lifecycle, hybrid recall, a dependency-aware task queue with
lease-based claiming, captured plans, and research trials. Durable knowledge is
stored as markdown files on disk; a single Go binary indexes it, serves it over
MCP, and renders a web console for inspection.

Website: [thereisnospoon.org](https://thereisnospoon.org) (source in [`docs/`](docs/))

## Design principles

- **Built for a fleet, not a lone agent.** Real coordination primitives: a
  dependency-aware ready-queue, atomic lease-based task claiming, and plans
  composed of notes and steps, so agents divide labor instead of colliding.
- **Files are the source of truth.** Every memory and note is a markdown file
  with YAML frontmatter under `~/.seamless` -- git-diffable, greppable,
  hand-editable. SQLite is a rebuildable index; delete it and lose nothing.
- **Curation proposes, humans dispose.** The gardener's dedup, staleness, merge,
  and stale-plan passes only *propose*; applying is an explicit action.
  Supersession preserves provenance, so nothing is silently rewritten.
- **One binary, no ceremony.** Static Go binary, no CGO, pure-Go SQLite, no
  Node, no separate vector engine, no cloud account.

## Quick start

Requires Go 1.25+. No CGO toolchain, no external database, no Node.

```bash
cp seamless.yaml.example seamless.yaml
openssl rand -hex 32              # put the result in mcp.api_key
make build                        # ./bin/seamlessd + ./bin/seam
make doctor                       # config, database, and tool-count self-checks
make run                          # serve on 127.0.0.1:8081
```

Register the MCP endpoint with a client and install the Claude Code hooks:

```bash
claude mcp add --scope user --transport http seamless http://127.0.0.1:8081/api/mcp \
  --header "Authorization: Bearer $KEY"

./bin/seamlessd install-hooks                       # SessionStart/UserPromptSubmit/SessionEnd
./bin/seamlessd map-repo --path ~/code/myrepo --project myrepo
make console                                        # open the console, pre-authenticated
```

`--scope user` is required: `claude mcp add` otherwise defaults to `local` and
ties the registration to whichever directory you ran it from. `map-repo` binds a
working directory to a project, so agents in that repo inherit project scope
without passing it on every call.

`make install-onboard-skill` installs a `/seam-onboard` Claude Code skill that
walks an agent through this setup and verifies each step.

## Concepts

**Memory.** A markdown file with frontmatter: ULID `id`, a `kind`
(`constraint`, `runbook`, `protocol`, `gotcha`, `decision`, `refuted`,
`reference`, `stage`), a one-line `description` that is the only text shown in
indexes, and a validity window. Writing a memory with `supersedes` sets
`invalid_at` and `superseded_by` on the old one, which then leaves the indexes
while remaining on disk as provenance.

**Session.** Claude Code hooks open an ambient session per agent, inject a
budgeted `<seam-briefing>` at SessionStart, match stored memories against each
prompt at UserPromptSubmit, and harvest findings at SessionEnd. Sessions
heartbeat; an idle reaper marks abandoned ones `expired`.

**Recall.** One search entry point fusing FTS5 keyword search and brute-force
cosine over float32 embedding BLOBs via reciprocal rank fusion, packed to a token
budget.

**Task.** A dependency-aware ready-queue. `tasks_claim` atomically moves a ready
task to `in_progress` under a lease (default 900s); re-claiming heartbeats it, an
expired lease is reclaimable, and `tasks_release`, closing the task, or ending
the session frees it.

**Plan.** A composition, not a primitive, keyed by `plan:<slug>`: a narrative
note, supporting notes sharing the tag, and tasks created with the same slug.
Plan-step tasks are excluded from the default ready-queue so they do not pollute
it.

## MCP surface

30 tools over streamable HTTP at `/api/mcp`, authenticated with a static bearer
key. Project and session are inherited from the session binding, so agents in a
mapped repo rarely pass `project` explicitly.

| Group | Tools |
|---|---|
| Sessions | `session_start`, `session_update`, `session_end` |
| Memory | `memory_write` (carries `supersedes`), `memory_append`, `memory_read`, `memory_delete` |
| Discovery | `recall` |
| Notes | `notes_create`, `notes_read`, `notes_update`, `notes_append`, `notes_delete` |
| Projects | `project_list`, `project_create` |
| Tasks | `tasks_add`, `tasks_update`, `tasks_ready`, `tasks_list`, `tasks_claim`, `tasks_release` |
| Research lab | `lab_open`, `trial_record`, `trial_query` |
| Gardener | `gardener_proposals`, `gardener_request`, `gardener_split`, `gardener_apply` |
| Utility | `capture_url`, `usage_summary` |

`seamlessd doctor` asserts the registered tool count against the declared one, so
a tool written but never wired in fails the check.

## CLI

`seam` is a headless client for the same server (`seam help` for the full list):

```
seam prime [--cwd DIR]            start/resume a session, print the briefing
seam remember --name N --kind K --description D [--body TEXT]
seam recall QUERY [--scope all|memories|notes] [--limit N]
seam ready [--project P] [--blocked]      actionable queue
seam task add|list|claim|release|done <id>
seam plan list|show|check|approve <slug>
seam status | sessions | usage | doctor
```

Flags must precede positional arguments (Go's `flag` package stops at the first
non-flag): `seam task release --force <id>`.

## Console

A server-rendered observability UI (`html/template`, vanilla JS, SSE -- no build
step) at `/console`, covering Overview, Memories, Notes, Tasks, Plans, Sessions,
Events, Interactions, Projects, Gardener, Retrieval, Relations, and Settings.
It is read-mostly: browsing, drill-in, gardener approval, and live-tunable
briefing knobs that store a database override winning over file and env until
reset.

## Storage layout

```
~/.seamless/
  seam.db                              SQLite: indexes, sessions, tasks, trials, events, embeddings
  memory/{project|_global}/{name}.md   one memory per file (source of truth)
  notes/{project|inbox}/{slug}.md      work artifacts (source of truth)
```

Files own durable knowledge. The database owns high-churn state (sessions, tasks,
trials, events, telemetry) and is otherwise a rebuildable index over the files.

## Architecture

| Package | Responsibility |
|---|---|
| `cmd/seamlessd/` | server daemon (`serve`, `doctor`, `import`, `install-hooks`, `map-repo`, `family`, `console-open`) |
| `cmd/seam/` | headless CLI for agents and owner observability |
| `internal/core/` | domain types (Project, Memory, Session, Task, Trial, Event) |
| `internal/store/` | SQLite: schema, FTS5, embeddings (BLOB vectors + brute-force cosine), migrations |
| `internal/files/` | markdown layer: memory/note files, frontmatter, watcher |
| `internal/markdown/` | sanitized server-side markdown rendering for the console |
| `internal/llm/` | OpenAI (default), Ollama, Anthropic chat + embeddings |
| `internal/retrieve/` | briefing assembler, prompt-context matcher, recall (RRF) |
| `internal/lifecycle/` | supersession, arbitration, provenance |
| `internal/gardener/` | scheduled dedup / staleness / digest / stale-plan proposals |
| `internal/tasks/` | dependency-aware ready-queue |
| `internal/plans/` | captured Claude Code plan vocabulary (tags, statuses, tracking task) |
| `internal/mcp/` | 30 MCP tools (streamable HTTP, static bearer key) |
| `internal/hooks/` | SessionStart / UserPromptSubmit / SessionEnd + plan-mode capture (PostToolUse / SubagentStop / PermissionRequest) |
| `internal/console/` | server-rendered observability UI (html/template + SSE) |
| `internal/events/` | append-only event log; SSE fan-out; retrieval stats |
| `internal/capture/` | SSRF-safe URL fetch |
| `internal/importer/` | bulk import of an existing markdown corpus |
| `internal/validate/` | path / title / name guards |
| `internal/config/` | single YAML + env config |

## Configuration

One YAML file, resolved in order from `$SEAMLESS_CONFIG`,
`~/.config/seamless/seamless.yaml`, then `./seamless.yaml`. Every key has a
`SEAMLESS_*` environment override; env wins over file, file over defaults. See
[`seamless.yaml.example`](seamless.yaml.example) for the annotated key set: bind
address, data directory, bearer key, token budgets, briefing tunables, gardener
thresholds, and LLM provider.

`seamless.yaml` is gitignored because it holds the bearer key and provider
credentials.

## Development

```
make build      # ./bin/seamlessd + ./bin/seam
make test       # unit tests
make test-race  # unit tests under the race detector
make bench      # hot-path benchmarks (recall, briefing, matcher, event fan-out)
make lint       # golangci-lint
make check      # the full gate: build + vet + fmt-check + lint + test-race
make doctor     # config + database self-checks
make run        # serve on 127.0.0.1:8081
```

Tests are table-driven with `testify/require` against fresh or in-memory SQLite.
Use `make fmt` rather than `gofmt -w .`: the Make target scopes formatting to
git-tracked files, while a bare `gofmt` walk also rewrites dot-directories that
Go's `./...` pattern excludes.

Conventions live in [`AGENTS.md`](AGENTS.md); read it before writing code.

## Deployment

Two install layouts drive the same single instance (port `8081`, data dir
`~/.seamless`), so only one is active at a time -- installing prod replaces dev,
and vice versa.

**Dev** runs the service and hooks straight from the working tree, so a rebuild
takes effect on the next restart. Fast to iterate, but `make build`, a branch
switch, or moving the repo changes what the live service and the global
SessionStart hook execute:

```
make install-service   # launchd service -> ./bin/seamlessd + ./seamless.yaml
make install-hooks     # SessionStart/UserPromptSubmit/SessionEnd -> ./bin/seam
make dev               # rebuild + restart in place
```

**Release** snapshots the binaries and config to stable, working-tree-independent
locations, then points launchd and the hooks at the copies. Survives rebuilds,
branch switches, and a moved or cleaned repo:

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
`command` and `mcp_tool` hooks for SessionStart (an `http` one is silently
ignored), and although SessionEnd does support `http`, at process exit the
fire-and-forget request races teardown and the ambient-session harvest often
never lands -- so it too runs as a `command` hook Claude Code waits on.
UserPromptSubmit fires mid-turn where `http` is reliable and stays an `http`
hook.

## Claude Code plan-mode capture

Once hooks are installed, plan-mode work is captured automatically: every save of
a `~/.claude/plans/*.md` file upserts a `cc-plan-<basename>` note (tagged
`plan:<slug>`, lifecycle `plan-status:draft|presented|approved|abandoned`),
planning subagents are cached as `cc-agent-<id>` notes in the same composition,
and approval via ExitPlanMode flips the note and creates an "Implement plan"
task. The session's first captured iteration returns related prior knowledge as
`additionalContext`.

Surfaces: `/console/plans`, briefing `PLAN (awaiting approval)` lines, and the
CLI:

```
seam plan list                 # captured plans with status/iteration
seam plan show <slug>          # body + attached notes + tasks
seam plan check <slug>         # FRESH/STALE per note vs git history
seam plan approve <slug>       # escape hatch when the approval hook was skipped
```

The gardener proposes abandoning plans still unapproved after
`gardener.stale_plan_days` (default 14; 0 disables).
