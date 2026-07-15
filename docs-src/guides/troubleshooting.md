---
title: Troubleshooting
description: Symptom-first fixes for a system whose hooks fail open - where silence, not an error, is what a broken install looks like.
---

Seamless has one property that makes troubleshooting unlike most software:
**hooks fail open**. A stopped daemon, a wrong key, a `seam` binary that moved -
none of these produce an error you or your agent will see. The handler returns
200 with empty context, `seam hook` reports to stderr and exits 0, and work
proceeds as if Seamless were not installed.

So the failure mode is *silence*. Nothing is broken-looking; you just quietly stop
getting briefings. That is a deliberate trade - a memory system must never block
an agent - but it means you cannot wait for an error. You have to go ask.

## Start here, always

```bash
seamlessd doctor   # server side: config, database, credentials, embedder, hooks
seam doctor        # client side: reachable, key accepted, tool count matches
```

They answer different questions and neither subsumes the other.

| | `seamlessd doctor` | `seam doctor` |
|---|---|---|
| Runs | Against config and the database directly - no daemon needed | Against a running daemon over HTTP and MCP |
| Checks | `binary`, `config`, `data_dir`, `mcp.api_key`, `llm`, `embedder`, `database`, `mcp_tools`, **`hooks`**, `gardener` | `server` (`/healthz`), `mcp_tools` (`tools/list`), `projects` |
| Fails on | Only a `fail` - warnings are informational | Any failed check |

**The `hooks` check exists only in `seamlessd doctor`.** If your symptom is "no
briefing", `seam doctor` cannot tell you why - it will happily report a healthy
server that no hook is calling. Run both.

`seamlessd doctor`'s `embedder` check makes a **real embed call**, which is the
one way to learn that recall has quietly gone keyword-only.

---

## No `<seam-briefing>` appears at session start

**What is happening.** Something in the chain - hook installed, `seam` on disk,
daemon up, key valid - is broken, and every link in it fails silently by design.

**Fix.** Walk the chain in order:

1. `seamlessd doctor` → the `hooks` line. It reports `N/N installed in <path>`,
   looking at `~/.claude/settings.json` and then `./.claude/settings.json`. Not
   installed, or partial, is a warning - which does **not** fail the run, so read
   it rather than trusting the exit code. Fix with `seamlessd install-hooks`.
2. `seam doctor` → is the daemon actually up and is the key accepted? An empty
   `mcp.api_key` makes the daemon reject every MCP and hook request while looking
   perfectly healthy on `/healthz`.
3. Did the `seam` binary move? Command hooks bake in an **absolute path**. A
   rebuild is fine; moving or cleaning the repo is not. Re-run
   `install-hooks`, or install the release layout so the hooks point at stable
   copies rather than your working tree.
4. Check the hook's **type** if you hand-edited settings.json. Claude Code
   silently ignores an `http` hook for `SessionStart` - it must be a `command`
   hook. This is why `install-hooks` writes it that way.

Detection matches by hook URL or command, **not** by the `seamless_managed`
marker, because Claude Code strips unknown keys when it rewrites settings.json.
So a hook that lost its marker still counts as installed, and a re-install adopts
it in place rather than appending a duplicate.

## A briefing appears, but it is nearly empty or names the wrong project

**What is happening.** The hook fired and the daemon answered - this is not a
plumbing problem. The cwd resolved to a project you did not expect, or to none.

**Fix.** The [precedence chain](/concepts/projects/) resolves scope from the
working directory via the repo map. Three outcomes:

- **Wrong project** - the cwd matched a mapping you forgot about. Check
  `/console/projects`, and re-map with `seamlessd map-repo --path <dir> --project
  <slug>`.
- **A project named after your directory** - an unmapped *git* repo
  auto-registers a project named after the repository root. It is real, it is
  just new and therefore empty.
- **No project at all** - the cwd is not inside a git repo, so nothing resolved
  and the session is global.

An empty briefing for a project that genuinely has no constraints and no memories
is correct behavior, not a fault. The header counts what exists; if it says zero,
the store is telling the truth.

## A write is rejected as ambiguous scope

**What is happening.** This is the [fail-closed rule](/concepts/projects/) doing
its job. A durable write with no resolvable scope is rejected rather than landing
in the global scope, because a global memory is seen by every agent in every repo
forever - and that must never be what happens when the system is *unsure*.

**Fix.** The message tells you which of two cases you are in:

| Message says | Cause | Fix |
|---|---|---|
| *no bound or ambient session to infer the project from* | No `session_start`, and no ambient session for this cwd | Call `session_start` with your `cwd`, or pass `project=<slug>` |
| *active ambient sessions span multiple projects* | You are unbound and other agents are live in several repos, so inheriting would bleed your write into someone else's project | Pass `project=<slug>` explicitly |

If a call that worked all morning starts failing this way, suspect a **lost
binding** - see the daemon-restart section below. `project: global` is always
accepted; it is a token you pass on purpose.

## Recall returns junk, or misses what you just wrote

**What is happening.** Three unrelated causes wear the same symptom.

**Recall has degraded to keyword-only.** If the embedding provider is unreachable,
rate-limited, or rejecting the key, recall **degrades rather than errors** - you
get worse ranking, not a failure, because a partial answer beats no answer during
a network incident. That is correct in the moment and terrible for three weeks.
Run `seamlessd doctor` and read the `embedder` line: it probes with a real call.
Note that provider `anthropic` has no embeddings API at all, so selecting it means
permanently lexical recall.

**The memory is not written to be findable.** The `description` is the retrieval
surface - the only text indexes show, and the *only* text the prompt-injection
matcher scores (name and description; never the body). A description like "notes
about the console" cannot be retrieved by anything. See [Write memories that get
recalled](/guides/write-good-memories/).

**You wrote it seconds ago.** The prompt matcher's corpus is rebuilt on a 30-second
interval, and an expired lookup serves the *stale* corpus while the rebuild runs
behind the hook - so the hook never pays for a cold rebuild, and a brand-new
memory can miss the next prompt's injection by a little more than the interval
suggests. An explicit `recall` call does not use that corpus and sees the write
immediately.

Before searching at all: **read what was already injected.** The briefing is in
context. Re-recalling it spends tokens to learn what the agent was already told.

## A task is stuck `in_progress` and nobody is working it

**What is happening.** Its holder died, and you are between two clocks. An expired
lease makes a task **stealable by id** - but it does *not* re-queue it. The task
is still `in_progress`, and `tasks_ready` returns only `open` tasks, so nothing
surfaces it. Lease expiry is enforced lazily inside `tasks_claim`; there is no
sweeper watching leases.

What *does* re-queue it is the session reaper: it expires sessions idle past
`gardener.session_idle_minutes` (default 45) and releases their claims back to
`open`.

**Fix.**

1. Confirm the diagnosis: `seam task list --status in_progress`, or
   `/console/tasks` - a task's detail panel shows its holder and whether the lease
   is **live** or **expired**.
2. **Check `gardener.enabled`.** The reaper runs inside the gardener's pass. With
   the gardener off, nothing reaps idle sessions and nothing ever returns a dead
   agent's claims - they stay stuck forever. `seamlessd doctor` warns when the
   gardener is disabled.
3. Do not wait if you do not want to: `seam task release --force <id>`, or the
   **release lock** button in the console. Both force-release any holder
   regardless of the lease. Neither is on the MCP surface - agents get the
   cooperative protocol, you get the override.

## `tasks_update` says the task is already claimed - by my own session

**What is happening.** The connection binding was lost. The task is genuinely held
by your session id; your *connection* no longer knows that, so the holder check
sees a stranger.

**Fix.** Re-run `session_start` with the **same name** to rebind. It resumes the
session rather than opening a second one.

The binding is keyed by the transport's `Mcp-Session-Id`, held in the daemon's
memory. Any daemon restart drops it - see below.

## A `seam` flag did nothing

**What is happening.** Go's `flag` package stops parsing at the first positional
argument, and everything after it is left as an argument rather than a flag.
`seam` inherits this, and **its own usage text prints the broken order**.

```bash
seam task release --force 01K7ABCD    # --force applies
seam task release 01K7ABCD --force    # --force is silently ignored
```

There is no error and no warning: the second form takes the normal
holder-checked path and the override never happens.

**Fix.** Flags before positionals, always. The [seam CLI
reference](/reference/cli-seam/) lists the commands where this bites, written the
way that works. Two exceptions: `seam recall` parses its own arguments so flags
can go anywhere, and `seam task done|start|drop|reopen` take no flags at all.

## A briefing setting in the YAML is ignored

**What is happening.** The `briefing:` block has a fourth precedence layer above
file and environment: a **runtime override stored in the database**, written by the
console's Settings → Briefing injection form. It wins over both, applies from the
next session start without a restart, and stays until reset.

**Fix.** Check the console before you check the YAML. This is the one place the
config file is not the last word, and it exists so you can tune what agents get
injected while they are running. See
[Configuration](/reference/configuration/).

## Two daemons, the wrong port, or code changes that never land

**What is happening.** Seamless is **one instance per machine**: one port (8081),
one data directory (`~/.seamless`). The dev and release install layouts drive that
same instance, so installing one replaces the other. Most confusion here is
someone accidentally interacting with a second copy, or with the same copy running
older code.

**Fix.**

- **Compare versions.** The same version string appears in `/healthz`, the MCP
  handshake, and the startup log line. `seam status` prints health and version.
  If it disagrees with what you just built, the daemon is running older code.
- **A rebuild does not restart anything.** `make build` and `make check` rewrite
  `bin/seamlessd`, but a running process keeps its in-memory image. The new binary
  takes effect on the next restart - which also means an *accidental* restart
  silently upgrades the running daemon to whatever is in your working tree.
- **Never `pkill -f "seamlessd serve"`.** Running a throwaway daemon on a spare
  port is the right way to test in isolation, but that pattern matches the real
  service's command line too and kills it. Under launchd it comes back in about a
  second with a new pid and no data loss - but **every agent's MCP connection
  drops and their session bindings are lost mid-task**, which resurfaces as the
  ambiguous-scope and self-claimed-task symptoms above. Kill the throwaway by its
  own pid, or by its port.
- **Tools missing in some repos?** That is not a daemon problem. `claude mcp add`
  defaults to `local` scope, tying the registration to the directory you ran it
  from. Register with `--scope user`.

## `memory_write` says the name is held by a superseded memory

**What is happening.** A superseded or archived memory leaves every index but
stays on disk as provenance - and it still owns its filename. The name is taken by
something you cannot see in any index.

**Fix.** Pick a different name, or free the old one with `memory_delete`. Prefer a
different name: the old file is the record of what you used to believe, and
deleting it destroys the history that explains the current state. See [Memory &
notes](/concepts/memory/).

## A `supersedes` failed but the memory was written

**What is happening.** This is a deliberate partial failure, reported rather than
swallowed. The new memory's content is valid knowledge, so it is **written and
kept** - but the supersession did not happen, which means **the old memory is
still active**, live in briefings and recall alongside its replacement. That is
exactly the contradictory-store state the lifecycle exists to prevent, so it comes
back as a tool error rather than an error field inside a success payload.

**Fix.** Correct the `supersedes` target and re-run the write. Re-writing the same
name is a lossless in-place update, so retrying is safe.
