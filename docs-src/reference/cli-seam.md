---
title: seam CLI
description: Every seam subcommand — agent loop, tasks, plans, observability, hooks — plus the flags-before-positionals rule that governs all of them.
---

`seam` is the headless CLI. It does no work itself: it loads the same
configuration the daemon does, then talks to a running `seamlessd` — over MCP at
`/api/mcp` for most commands, and over the console's JSON endpoints for the
owner-only actions that do not exist as MCP tools (force-releasing a task lock,
approving a plan). Both paths authenticate with the static bearer key from
[Configuration](/reference/configuration/), so if the key is wrong or the daemon
is down, every subcommand below fails at the connection.

The target address is derived from the configured bind address, with a bind-all
host (`0.0.0.0`, `::`, or empty) mapped to loopback.

## Flags go before positionals

Go's `flag` package stops parsing at the first positional argument. Everything
after it is left as an argument, not a flag. `seam` inherits this, so:

```bash
seam task release --force 01K7ABCD    # --force applies
seam task release 01K7ABCD --force    # --force is ignored, silently
```

There is no error and no warning — the second form releases the task through the
normal holder-checked path and the override never happens.

`seam help` prints the working order and says so up front, so the help is a
reliable guide. It was not always: the usage text used to print the
trailing-flag form for exactly the commands where it does not work, which is
worth knowing if you are reading an older build's `--help`.

The commands where this bites, written the way that actually works:

```bash
seam capture --project myproj https://example.com/page
seam task claim --lease 1800 01K7ABCD
seam task heartbeat --lease 1800 01K7ABCD
seam task release --force 01K7ABCD
seam plan check --cwd ~/repos/myproj my-plan-slug
```

Two exceptions:

- **`seam recall` parses its own arguments**, so flags may appear before or
  after the query words. `seam recall chroma boot race --project myproj` works.
- **`seam task done|start|drop|reopen`** parse no flags at all; the id is taken
  as the first argument directly.

Commands that take only flags and no positionals (`prime`, `remember`, `ready`,
`task list`, `task add`, `plan list`, `status`, `usage`, `doctor`) are unaffected.

## Agent loop

The four commands an agent — or you, standing in for one — uses to start a
session, write knowledge, and search it back.

### seam prime {#seam_prime}

```bash
seam prime [--cwd DIR] [--name NAME]
```

Calls `session_start` with `source=explicit` and prints the briefing to stdout;
the session id and resolved project go to stderr, so the briefing pipes cleanly.
`--cwd` defaults to the current directory and is what resolves the project via
the repo mapping. Reusing a `--name` resumes that session rather than opening a
new one. When there is no briefing content yet, it says so on stderr.

This is the explicit form of what the SessionStart hook does automatically.

### seam remember {#seam_remember}

```bash
seam remember --name N --kind K --description D [--body TEXT] [--project P]
```

Calls `memory_write`. `--name`, `--kind`, and `--description` are required.
`--kind` is one of `constraint`, `runbook`, `protocol`, `gotcha`, `decision`,
`refuted`, `reference`, or `stage`. `--description` is the one-line summary
(<=150 chars) that indexes show.

The body comes from `--body`, or from **stdin** when `--body` is omitted — an
empty body after either route is an error, not an empty memory. `--project`
defaults to the server's own binding or global. Output reports whether the write
created or updated the memory, and names a similar existing memory when the
server flags one.

### seam recall {#seam_recall}

```bash
seam recall QUERY [--scope all|memories|notes] [--project P] [--limit N]
```

Calls `recall`, the single RRF-fused search entry point. Scope defaults to `all`
and limit to `10`. Query words are joined with spaces, and — unlike every other
subcommand — flags may sit anywhere in the line. Each hit prints its kind, name,
age, source, score, and description; no matches prints `no results`.

### seam capture {#seam_capture}

```bash
seam capture [--project P] URL
```

Calls `capture_url` to fetch a page through the SSRF-safe fetcher and store it
as a note. An empty `--project` files the note under `inbox`. Mind the flag
order: `seam capture URL --project p` parses `--project` as a positional and
drops it, filing the note to `inbox` without saying so.

## Tasks

The queue side of the CLI. See [Tasks](/reference/mcp/tasks/) for what ready,
blocked, and claimed mean.

### seam ready {#seam_ready}

```bash
seam ready [--project P] [--blocked] [--plan S]
```

Calls `tasks_ready` and lists the actionable queue by short id and title.
`--blocked` additionally lists blocked tasks, each followed by its blockers and
their statuses. `--plan S` shows that plan's step tasks instead of the default
queue — plan steps are excluded from the default view. Prints
`ready: (nothing actionable)` when the queue is empty.

### seam task list {#seam_task_list}

```bash
seam task list [--id ID] [--project P] [--status S] [--plan S]
```

Calls `tasks_list`. `--status` filters to `open`, `in_progress`, `done`, or
`dropped`. `--id` loads a single task by its globally-unique id and ignores the
project, status, and plan filters. `--plan` lists a plan's steps instead of the
default non-plan tasks. Bare `seam task` with no subcommand is the same as
`seam task list`.

### seam task add {#seam_task_add}

```bash
seam task add --title T [--body B] [--project P] [--depends id,id] [--plan S]
```

Calls `tasks_add`. `--title` is required. `--depends` takes a comma-separated
list of blocker task ids. `--plan S` composes the task as a step of the plan
`plan:<slug>`, which keeps it out of the default queue.

### seam task done|start|drop|reopen {#seam_task_transition}

```bash
seam task done <id>
seam task start <id>
seam task drop <id>
seam task reopen <id>
```

Calls `tasks_update` with the mapped status: `done` → `done`, `start` →
`in_progress`, `drop` → `dropped`, `reopen` → `open`. These parse no flags.

### seam task claim {#seam_task_claim}

```bash
seam task claim [--lease SECS] <id>
```

Calls `tasks_claim` to atomically take a ready task, printing the new status and
the lease expiry. `--lease` overrides the server's default lease (900 seconds);
it is only sent when greater than zero.

### seam task heartbeat {#seam_task_heartbeat}

```bash
seam task heartbeat [--lease SECS] <id>
```

The same `tasks_claim` call as `claim`. Re-claiming a task you already hold
refreshes its lease, so `heartbeat` is that path under a name that says what it
is for during long work. There is no separate heartbeat tool.

### seam task release {#seam_task_release}

```bash
seam task release [--force] <id>
```

Without `--force`, calls `tasks_release`, which only releases a claim you hold.

`--force` is the owner override and takes a different route entirely: it POSTs
to the console's release endpoint, which is bearer-authenticated and force-releases
any holder's claim. Agents on the MCP surface cannot reach that path.

## Captured plans

Owner surface over the plans Claude Code plan mode captures. `list`, `show`, and
`approve` are backed by the console's JSON endpoints; `check` also runs `git`
locally. Bare `seam plan` is the same as `seam plan list`.

### seam plan list {#seam_plan_list}

```bash
seam plan list [--project P] [--window W]
```

Lists captured plans with slug, status, title, project, iteration, agent count,
task progress, and age. `--window` is `24h`, `7d`, `30d`, or `all` (default
`all`) and is applied by the server; `--project` filters the returned rows
client-side. Prints `(no captured plans)` when nothing matches.

### seam plan show {#seam_plan_show}

```bash
seam plan show <slug>
```

Prints one plan: its status, project, title, source file and iteration, the
notes attached to the composition (each marked `note` or `agent`), its tasks,
and then the plan body.

### seam plan check {#seam_plan_check}

```bash
seam plan check [--cwd DIR] <slug>
```

Staleness check against a repo's git history. `--cwd` defaults to the current
directory and must be a git repo. For the plan body and each attached note, it
reads the capture stamp (the `> captured from ... | git <head> | ...` line) and
compares it to the repo's current HEAD:

- **FRESH** — stamped at the current HEAD, or nothing changed since the stamped
  commit, or files changed but none the note mentions.
- **STALE** — files the note mentions by path changed between the stamped commit
  and HEAD. The changed paths are listed (truncated after five).
- **UNKNOWN** — no git stamp, captured outside a git repo, the stamped commit no
  longer resolves (rebased away), or the note could not be read.

Mentioned paths are extracted from the prose by pattern, and match whether the
note wrote them absolute or repo-relative. **The command exits non-zero when any
note is stale**, so it works as a gate in a script.

### seam plan approve {#seam_plan_approve}

```bash
seam plan approve <slug>
```

Escape hatch for when Claude Code skips the approval hook: flips the plan to
approved and creates its tracking task, reporting the new task or noting that
one already exists.

## Observability

### seam status {#seam_status}

```bash
seam status
```

Server health from the unauthenticated `/healthz` endpoint (status and version),
the configured data directory, then the project count and slugs via
`project_list` — which doubles as proof the static key works. If MCP is
unavailable it still prints health and says so on the projects line rather than
failing.

### seam sessions {#seam_sessions}

```bash
seam sessions [--status active|completed]
seam sessions <id>
```

With no positional, lists sessions with name (or short id), project, status, age,
and an ambient marker, under a total/active count. With an id, prints that
session's detail: status, project, tool calls, memory writes and reads, the
read-after-inject ratio, and findings when present. Both read the console's JSON
endpoint.

### seam usage {#seam_usage}

```bash
seam usage
```

Activity roll-up from `usage_summary`: active memories broken down by kind, note
count, session and task counts by status, retrieval injections and reads with a
read-after-inject percentage, the most-injected items, and pending gardener
proposals.

### seam doctor {#seam_doctor}

```bash
seam doctor
```

Client-side checks, each reported `ok` or `FAIL`, exiting non-zero if any failed:

1. **server** — `/healthz` reachable and reporting `ok`.
2. **mcp_tools** — `tools/list` returns the tool count this CLI was built to
   expect. A mismatch means the running daemon is a different build.
3. **projects** — `project_list` answers.

This is the client-side view. `seamlessd doctor` checks config, database, and
credentials on the server side; they are different commands answering different
questions.

## Hooks

```bash
seam hook session-start|user-prompt-submit|session-end
seam hook post-tool-use|subagent-stop|permission-request
```

Invoked by Claude Code, not by hand. Each reads the hook payload from stdin,
forwards it to the matching `/api/hooks/...` endpoint with the bearer key, and
copies the JSON response to stdout — so a `command` hook drives the same server
logic an `http` hook would. This exists because Claude Code only runs
command-type hooks for SessionStart, which is how the briefing and the ambient
session get injected at all.

Two behaviours matter if you are debugging a hook:

- **Failures do not block the session.** A missing config, an unreachable
  daemon, or an unreadable stdin is reported on stderr and exits 0. Only an
  unknown event name — an install bug, not a runtime hiccup — is a hard error.
- **`post-tool-use` pre-filters locally.** It fires machine-wide on every
  `Write`/`Edit`, so the CLI drops everything that is not an `ExitPlanMode`
  approval or a write to a file directly under `~/.claude/plans` before loading
  config or touching the network. The daemon re-validates the path anyway.

Install these with `seamlessd install-hooks`.
