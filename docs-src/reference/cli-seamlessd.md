---
title: seamlessd CLI
description: The daemon and operator CLI - serve, doctor, import, install-hooks, map-repo, family, console-open, and version.
---

`seamlessd` is both the server and the operator CLI. `serve` runs the daemon;
every other subcommand is a one-shot that opens the same config and database
directly, without going through a running server. That means most of them work
whether or not the daemon is up - and that `map-repo` and `family` write state
the running daemon reads.

Each subcommand parses its own flags. None of them take positional arguments
except `family`, which takes only positionals.

For the keys every command below resolves, see
[Configuration](/reference/configuration/).

## seamlessd serve {#seamlessd_serve}

```bash
seamlessd serve [--addr HOST:PORT]
```

Starts the HTTP server and blocks until SIGINT or SIGTERM, then shuts down
gracefully. `--addr` overrides the configured bind address (default
`127.0.0.1:8081`).

On a true first run - no config file anywhere in the search order and no
`SEAMLESS_MCP_API_KEY` in the environment - it generates the bearer key and
writes it to `~/.config/seamless/seamless.yaml` before starting. An existing
config file is never edited, even when its key is empty.

It wires up:

- `/healthz` - liveness plus a database ping. Reports `degraded` with a 503 when
  the ping fails.
- `/api/mcp` - the MCP tool endpoint, bearer-authenticated.
- `/api/hooks/...` - the session and plan-capture hooks.
- `/console/...` - the observability console. The bare root `/` redirects here.

Startup is deliberately tolerant of a half-configured install, and the log is
where you find out:

- **No embedder** - recall degrades to FTS-only for the life of the process. A
  missing credential logs a warning; a *malformed* setting (a bad `base_url`)
  logs an error, because that one is a typo rather than a choice.
- **No chat client** - gardener digest passes no-op.
- **Empty `mcp.api_key`** (a config file exists but leaves it blank) - logs a
  warning, and every MCP and hook request is then rejected.
- **Gardener disabled** - logged, and no maintenance passes run.

The startup line carries the version, commit, and data directory, which is how
you spot a daemon running older code than your working tree.

## seamlessd doctor {#seamlessd_doctor}

```bash
seamlessd doctor
```

Server-side self-checks. Each line reports `ok`, `warn`, or `fail`; **only a
`fail` exits non-zero** - warnings are informational. Checks stop early if
config or the database cannot be loaded at all.

| Check | What it reports |
|---|---|
| `binary` | The version that ran. |
| `config` | Which file it loaded, or that it fell back to defaults + env. |
| `data_dir` | The resolved data directory. |
| `mcp.api_key` | Set, or a warning that `/api/mcp` will reject everything. |
| `llm` | The provider, or a warning that its credential is missing. |
| `embedder` | Probes the embedder with a real embed call. Unreachable, unconfigured, or provider `anthropic` (no embeddings API) is a warning: recall degrades to FTS. |
| `database` | Path, schema version, and table count. Opens and migrates if needed. |
| `mcp_tools` | Fails if the number of registered tools disagrees with the expected count - catches a tool written but never wired in. |
| `hooks` | How many hook events are installed, and where. Partial or absent is a warning. |
| `gardener` | The ticker configuration, or a warning that it is disabled. |

The hooks check looks at `~/.claude/settings.json` and then
`./.claude/settings.json`, matching by hook URL or command rather than by the
managed marker - Claude Code strips that marker when it rewrites the file, and a
hook that still fires must still count as installed.

Reach for it after changing config, after an upgrade, or as the first step when
recall has quietly gone lexical.

## seamlessd import {#seamlessd_import}

```bash
seamlessd import [--from DIR] [--skip LIST] [--embed=false]
```

Imports a Seam v1 data directory into this instance. Memory and note files are
written and indexed under the v2 data directory; trials, sessions, and tool-call
events are inserted into the database.

| Flag | Default | Meaning |
|---|---|---|
| `--from` | `~/.seam` | v1 data directory to import from. A leading `~` expands. |
| `--skip` | `briefings` | Comma-separated storage projects to skip. |
| `--embed` | `true` | Embed imported items for cosine search, using the configured provider. |

**It is idempotent by id**, so re-running imports only what is new - which makes
a delta re-import safe after the first pass. It honours SIGINT/SIGTERM, and
prints a report even when the import ends in an error. With `--embed` on and no
usable embedder, it warns and imports without vectors rather than failing.

## seamlessd install-hooks {#seamlessd_install_hooks}

```bash
seamlessd install-hooks [--settings PATH] [--url BASE] [--seam PATH] [--mcp=false]
```

Merges the Seamless hook entries into a Claude Code `settings.json`, then
registers the MCP server with the `claude` CLI.

| Flag | Default | Meaning |
|---|---|---|
| `--settings` | `~/.claude/settings.json` | Target settings file, created if absent. Point it at a project-scoped `.claude/settings.json` to scope the hooks to one repo. |
| `--url` | derived from the config addr | Base URL of the daemon. |
| `--seam` | sibling of this binary, else `seam` on PATH | Path to the `seam` CLI baked into the command hooks. |
| `--mcp` | `true` | Register the MCP server via `claude mcp add --scope user`. |

It generates `mcp.api_key` on a true first run under the same rule as `serve`,
and refuses to run when an existing config leaves the key empty, since the key
is what the hooks authenticate with. The loaded config path is made absolute
and baked into the command hooks as `SEAMLESS_CONFIG`, so they resolve config
from any working directory. A `--seam` binary that cannot be found is a printed
warning, not an error - the hooks would fail at fire time, so it says so now.

The MCP registration is best-effort: the hooks land regardless, and a missing
`claude` CLI or a failed `claude mcp add` prints the exact command to run
yourself. An already-registered `seamless` server is left alone.

The merge preserves unknown keys, replaces existing Seamless-managed entries in
place, adopts unmarked entries that point at the same hook URL or run the same
`seam hook <event>` command (this is what stops re-installs from duplicating
hooks), and backs the file up once before the first change. It is idempotent:
an already-current file is reported as up to date and left untouched. Each hook
is reported as added, updated, or unchanged.

Six events are installed together: `SessionStart`, `UserPromptSubmit`,
`SessionEnd`, `PostToolUse`, `SubagentStop`, and `PermissionRequest`. All are
command hooks that shell out to `seam hook <event>` except `UserPromptSubmit`,
which is an http hook - Claude Code will not run an http hook for SessionStart
at all, and at SessionEnd a fire-and-forget request races process teardown, so
the findings harvest would often be lost.

## seamlessd map-repo {#seamlessd_map_repo}

```bash
seamlessd map-repo --project SLUG [--path DIR]
```

Adds an entry to the `repo_project_map` setting, so an agent whose working
directory is under that path resolves to that project - in the hooks and in
`session_start`. This is what makes a briefing arrive scoped to the right
project without the agent passing `project` anywhere.

Mostly you will not need it: a git repo maps itself on its first session, taking
the slug from the repo root's directory name. Run `map-repo` to override that
derived slug, or to map a directory that is not a git repo.

`--project` is required. `--path` defaults to the current directory and is made
absolute. The command also ensures the project exists, so mapping a new slug
registers it. Writes straight to the database; no running daemon needed.

## seamlessd family {#seamlessd_family}

```bash
seamlessd family list
seamlessd family add <name> <slug> [<slug>...]
seamlessd family remove <name> [<slug>...]
```

Manages the `project_families` setting: named groupings whose members surface
each other's recent findings in briefings. Use it when two projects are really
one body of work and an agent in either should see what happened in the other.

Members are **project slugs, not repo paths** - resolve a repo to its slug with
`map-repo` first. `remove` with no slugs removes the whole family; with slugs it
removes just those members. `rm` is accepted as an alias for `remove`.

Adding a slug that is not yet a registered project prints a warning but
succeeds: the membership starts taking effect once an agent opens that repo and
registers it.

## seamlessd console-open {#seamlessd_console_open}

```bash
seamlessd console-open [--browser APP]
```

Opens the console in a browser, already authenticated. It renders a one-shot
self-submitting login page to a `0600` temp file and opens it; the page POSTs
the static key to the console's login endpoint, which sets the session cookie
and redirects into the console - so you land on an authenticated page without
pasting a key.

`--browser` targets a specific browser application (for example
`"Google Chrome"`, so an agent driving Chrome gets the auth cookie even when
another browser is the default). It is **macOS only** and is rejected with an
error on other platforms rather than silently opening the default browser.

It refuses to run when `mcp.api_key` is empty, or when the server does not
answer `/healthz` within two seconds - the page has nowhere to POST otherwise.
Any HTTP response counts as reachable, including a degraded 503.

## seamlessd version {#seamlessd_version}

```bash
seamlessd version
```

Prints the version, commit, and build date. `-v` and `--version` are aliases.

Commit and build date are link-time metadata set by the Makefile; a plain
`go build` leaves them `unknown`. The same version string appears in `/healthz`,
the MCP handshake, and the startup log - compare them when you suspect the
daemon is running older code than what you just built.
