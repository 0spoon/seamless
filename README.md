# Seamless

[![Go Reference](https://pkg.go.dev/badge/github.com/0spoon/seamless.svg)](https://pkg.go.dev/github.com/0spoon/seamless)
[![Latest release](https://img.shields.io/github/v/release/0spoon/seamless)](https://github.com/0spoon/seamless/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/MCP-compatible-6f42c1)](https://thereisnospoon.org/docs/reference/mcp/)

An AI coding agent rediscovers the same constraint every session, because
nothing it learns survives the context window. Run two agents against the same
backlog and they pick the same step and build it twice. And the products that
promise to fix this keep your project's memory in someone else's database.

Seamless is a local-first memory and coordination substrate for AI coding
agents. Works with Claude Code, Codex CLI, and any MCP client.

It gives a fleet of agents a shared, durable memory and a way to divide work
without colliding: memories with a supersession lifecycle, hybrid recall, a
dependency-aware task queue with lease-based claiming, captured plans, and
research trials. Durable knowledge is stored as markdown files on disk; a
single Go binary indexes it, serves it over MCP, and renders a web console for
inspection.

**Full documentation: [thereisnospoon.org/docs/](https://thereisnospoon.org/docs/)**
&nbsp;·&nbsp; Website: [thereisnospoon.org](https://thereisnospoon.org) (source in
[`docs/`](docs/))

## Design principles

- **Built for a fleet, not a lone agent.** Real coordination primitives: a
  dependency-aware ready-queue, atomic lease-based task claiming, and plans
  composed of notes and steps, so agents divide labor instead of colliding.
- **Files are the source of truth.** Every memory and note is a markdown file
  with YAML frontmatter under `~/.seamless` -- git-diffable, greppable,
  hand-editable. SQLite is a rebuildable index; delete it and lose nothing.
- **Curation proposes, humans dispose.** Every gardener pass -- from
  deduplicating and archiving to flagging dead weight and knowledge gaps --
  only *proposes*; applying is an explicit action. Supersession preserves
  provenance, so nothing is silently rewritten.
- **One binary, no ceremony.** Static Go binary, no CGO, pure-Go SQLite, no
  Node, no separate vector engine, no cloud account.

## How it compares

The agent-memory space splits into a few recognizable categories. By category,
because categories do not go stale:

| | Seamless | Cloud memory APIs | Built-in agent memory | Knowledge-graph servers |
|---|---|---|---|---|
| Storage format | Markdown files on your disk; SQLite as a rebuildable index | Their database, reached by API key | Vendor-managed store inside one product | A graph database, often a separate server |
| Runs where | Your machine, localhost only | Their cloud | The vendor's product | Your machine or theirs |
| Account required | No | Yes | The vendor's | Usually no |
| Multi-agent coordination | Task queue, lease-based claiming, shared plans | None | None -- one agent, one store | Shared reads at best |
| Forgetting policy | Supersession with provenance; a gardener proposes, a human disposes | Automatic summarization you do not control | Vendor-defined | Manual |
| Runtime | One static Go binary | HTTP SDK against their service | None (built in) | Node or Python, plus the database |

For the version with product names and receipts, see
[the full comparison](https://thereisnospoon.org/compare/).

## Real transcripts

Four pairs of real, unedited Claude Code sessions -- identical prompt,
identical repo, with and without Seamless:

- **[Cold start](https://thereisnospoon.org/scenarios/cold-start/)** -- one
  session continues yesterday's plan from an injected briefing; the other
  re-derives the work from a `TODO` and re-ships a bug the project had already
  fixed once.
- **[Constraint violation](https://thereisnospoon.org/scenarios/constraint-violation/)** --
  a security scanner demands `SameSite=Strict`, which the team already learned
  breaks external-link logins. One session ships the regression anyway; one
  refuses and cites the recorded constraint.
- **[Token safety](https://thereisnospoon.org/scenarios/token-safety/)** --
  told to persist refresh tokens, one agent mirrors the in-memory map into a
  raw-token SQL column; the other reads a recorded rule first and stores only
  SHA-256 hashes.
- **[Task collision](https://thereisnospoon.org/scenarios/task-collision/)** --
  two live agents race for the same plan step. One claim wins, the other
  bounces with the holder's name and pivots to the next ready step.

## What Seamless is not

Not a hosted team knowledge base, not a RAG framework, not a benchmark winner:
it is memory and coordination for one owner's fleet of agents, on that owner's
machine.

## Quick start

```bash
curl -fsSL https://thereisnospoon.org/install | sh
```

On Windows, the same install in PowerShell:

```powershell
irm https://thereisnospoon.org/install.ps1 | iex
```

That is the whole install. It needs `curl` and `tar` and nothing else -- no Go,
no CGO toolchain, no database, no Node. It fetches the checksum-verified release
archive for your platform (macOS, Linux, and Windows; amd64 and arm64), installs
`seamlessd` and `seam` into `~/.local/bin`, generates the bearer key, installs
hooks, MCP, and skills for the detected Claude Code/Codex local hosts, and runs
the daemon as a per-user service -- launchd on macOS, systemd `--user` on Linux, an
at-logon Scheduled Task on Windows. Upgrade any time with `seamlessd update`
(re-runs the installer for you; `--check` reports installed vs latest): your
config and `~/.seamless` are never touched.

(Why `seam`? The CLI keeps the short name of Seam v1, the decommissioned
private predecessor Seamless was rebuilt from the ground up to replace.)

Then just start the selected client in a git repo. There is no project to create
and no repo to register: the session-start hook resolves your cwd to its git
root, derives a project from the repo's directory name, and records the mapping
on the spot, so agents inherit project scope without passing it on every call.
Reach for `seamlessd map-repo --path ~/code/myrepo --project myrepo` only to
override the derived slug.

It is [one shell script](docs/install) and piping a stranger's script into a
shell deserves a read first. Prefer a toolchain, or want the pieces one at a
time?

```bash
go install github.com/0spoon/seamless/cmd/...@latest   # Go 1.25+; seamlessd + seam
seamlessd serve                   # 127.0.0.1:8081; first run generates the API key
seamlessd install-hooks           # selected client's hooks, MCP, and skills
```

`install-hooks` detects Claude Code, Codex, or both. Claude Code gets a
user-scoped Streamable HTTP registration; the shared Codex app/CLI/IDE host gets
five hooks and the `seam mcp-proxy` stdio bridge. Current Codex supports direct
Streamable HTTP too - the proxy is Seamless's default so the bearer key stays in
the 0600 Seamless config, not a transport limitation. Other MCP clients register
`http://127.0.0.1:8081/api/mcp` with `Authorization: Bearer <mcp.api_key>`. From
a clone, `make build && make run` is the same daemon out of `./bin/`, and `make
install` sets it up as a service.

The installer delivers portable `seam-onboard` and `seam-research` skills for
the selected client: `~/.claude/skills/` for Claude Code and
`${CODEX_HOME:-$HOME/.codex}/skills/` for Codex. Run `/seam-onboard` in Claude Code
or `$seam-onboard` in Codex once to add a Seamless-awareness block to global or
project instructions (`CLAUDE.md` or `AGENTS.md`). From a clone, use
`make install-onboard-skill CLIENT=claude|codex|all|detect` (default: detect).

Then: [Quickstart](https://thereisnospoon.org/docs/quickstart/) ·
[Claude Code setup](https://thereisnospoon.org/docs/claude-code/) ·
[Codex local setup](https://thereisnospoon.org/docs/codex-cli/) ·
[Install & deploy](https://thereisnospoon.org/docs/install/)

## Documentation

The full docs are at
**[thereisnospoon.org/docs/](https://thereisnospoon.org/docs/)** (sources in
[`docs-src/`](docs-src/), generated by [`cmd/docsgen`](cmd/docsgen/)).

| | |
|---|---|
| [Concepts](https://thereisnospoon.org/docs/concepts/) | Memory & notes, sessions & briefings, recall, tasks & plans, projects & scope, the gardener |
| [Guides](https://thereisnospoon.org/docs/guides/) | Integrating an agent, writing memories that get recalled, coordinating a fleet, troubleshooting |
| [Reference](https://thereisnospoon.org/docs/reference/) | Every MCP tool, both CLIs, every config key, the hooks, and the file formats |
| [Internals](https://thereisnospoon.org/docs/internals/) | Architecture, contributing, domain invariants |

This README is deliberately short. Anything that can drift from the code -- tool
counts, config keys, CLI flags -- lives in the docs site, where the reference
pages are generated from the code itself and `make check` fails if they go stale.

## Development

```
make build      # ./bin/seamlessd + ./bin/seam
make test       # unit tests
make test-race  # unit tests under the race detector
make bench      # hot-path benchmarks (recall, briefing, matcher, event fan-out)
make lint       # golangci-lint
make check      # the full gate: build + vet + fmt-check + docs-check +
                # installer-check + site-check + lint + vulncheck + test-race
make doctor     # config + database self-checks
make run        # serve on 127.0.0.1:8081

make docs       # regenerate the docs site (docs-src/ -> docs/docs/, committed)
make docs-serve # regenerate + serve the site at 127.0.0.1:8899/docs/
```

Tests are table-driven with `testify/require` against fresh or in-memory SQLite.
Use `make fmt` rather than `gofmt -w .`: the Make target scopes formatting to
git-tracked files, while a bare `gofmt` walk also rewrites dot-directories that
Go's `./...` pattern excludes.

The docs site's output under `docs/docs/` is committed, and `make check` runs
`docs-check`, so a change to `docs-src/` -- or to the tool surface or config keys
the reference generates from -- must be followed by `make docs` in the same
change. See [`SITE.md`](SITE.md).

Conventions live in [`AGENTS.md`](AGENTS.md); read it before writing code.
