---
title: Claude Code hooks
description: The six hooks Seamless installs, their transports and timeouts, the fail-open contract, and what install-hooks writes into settings.json.
---

Seamless installs six Claude Code hooks. They are what makes sessions ambient:
an agent gets a briefing, its prompts get matched against stored memories, its
findings get harvested, and its plan-mode activity gets captured - without the
agent calling a single MCP tool.

## The installed hooks

Taken from the `seamlessHooks` definition in `internal/hooks/install.go`, in
install order:

| Event | Matcher | Transport | Timeout | Endpoint | Effect |
|---|---|---|---|---|---|
| `SessionStart` | `startup\|resume\|clear\|compact` | command (`seam hook session-start`) | 10s | `/api/hooks/session-start` | Registers the agent's cwd in the repo→project map, assembles the `<seam-briefing>`, and creates or resumes the ambient `cc/{prefix}` session. |
| `UserPromptSubmit` | none | http | 5s | `/api/hooks/user-prompt-submit` | Heartbeats the ambient session, matches the prompt against stored memories, and injects a recall block. A miss is logged as a `hook.prompt` event. |
| `SessionEnd` | none | command (`seam hook session-end`) | 10s | `/api/hooks/session-end` | Harvests findings and completes the agent's sessions. Bare ack - Claude Code's schema has no `hookSpecificOutput` for `SessionEnd`. |
| `PostToolUse` | `Write\|Edit\|MultiEdit\|ExitPlanMode` | command (`seam hook post-tool-use`) | 10s | `/api/hooks/post-tool-use` | Heartbeats the ambient session. Captures plan-file iterations (`Write`/`Edit`/`MultiEdit` under the plans dir) and plan approvals (`ExitPlanMode`). |
| `SubagentStop` | none | command (`seam hook subagent-stop`) | 10s | `/api/hooks/subagent-stop` | Caches a planning subagent's prompt and final report as a `cc-agent-<agent_id>` note in the plan composition. |
| `PermissionRequest` | `ExitPlanMode` | command (`seam hook permission-request`) | 10s | `/api/hooks/permission-request` | Marks the session's draft plan as presented when the user is prompted to review an `ExitPlanMode` call. |

Timeouts are in seconds, Claude Code's unit. Server-side the briefing and recall
paths are additionally bounded at 2s and the capture paths at 8s, so a slow store
cannot spend the whole hook budget.

`SessionStart` is the only hook with a matcher on session sources. Subagents
(`agent_type` set) share the parent's session and get no ambient session of their
own.

## The fail-open contract

**A hook must never block an agent.** Every handler in `internal/hooks` honors
this: an internal error yields a 200 with empty `additionalContext` rather than a
failure. Only a bad bearer key returns non-2xx (401).

The same contract runs client-side. `seam hook <event>` reports a failure to
stderr and exits 0 - a server that is down, a config that will not load, an
unreadable stdin, all produce exit 0. Only a misconfigured event name (an install
bug, not a runtime condition) is a hard error.

Plan and subagent capture are best-effort under the same rule: a capture problem
is logged and the hook still acks 200.

The consequence is that **hook failure is silent**. A stopped daemon, a bad key,
a `seam` binary that moved - none of these produce an error an agent or the user
will see. Work simply proceeds without a briefing, and nothing announces it.

So troubleshooting starts with the doctor checks, not with looking for an error:

```bash
seamlessd doctor   # includes the hooks check: N/N installed in <settings path>
seam doctor        # server reachable, key accepted, tools/list count
```

`seamlessd doctor`'s hooks check looks in `~/.claude/settings.json` and then
`./.claude/settings.json`, reporting the first location that has all of them, and
warning when they are partial or absent. `seam doctor` covers the other half: it
hits `/healthz` and calls `tools/list`, which is what tells you whether the
endpoint the hooks post to is actually answering.

## Why two transports

Five hooks are `command`, one is `http`. The split is not stylistic:

- **`SessionStart` must be a command hook.** Claude Code only runs
  `command`/`mcp_tool` hooks for `SessionStart`. An `http` hook there is silently
  skipped, and the briefing and ambient session never fire.
- **`SessionEnd` does support `http`, but it races.** At process exit the
  fire-and-forget request races Claude Code's teardown, so the ambient-session
  harvest often never lands and sessions pile up as active. A command hook is
  one Claude Code waits on, which makes the harvest reliable.
- **The plan-capture hooks are commands for cost.** `PostToolUse` fires
  machine-wide on every `Write`/`Edit`. The `seam` CLI pre-filters the payload
  locally and drops non-plan events before any config load or network round-trip,
  so the hot path never touches the network.
- **`UserPromptSubmit` stays `http`.** It fires mid-turn, where http is reliable.

Command hooks work by having Claude Code pipe the event JSON to the command's
stdin; `seam hook <arg>` forwards that to the matching endpoint and echoes the
response back on stdout. Same server logic either way - only the transport
differs.

One practical consequence: the bearer key is only written into settings.json for
the `http` hook. Command hooks read it from the config file at hook time.

## What install-hooks writes

```bash
seamlessd install-hooks                        # default: ~/.claude/settings.json
seamlessd install-hooks --settings ./.claude/settings.json
seamlessd install-hooks --url http://127.0.0.1:8081
seamlessd install-hooks --seam /path/to/seam
```

The base URL defaults to one derived from the config's bind address (a bind-all
host maps to loopback). `--seam` defaults to the `seam` binary sitting next to
the running `seamlessd`, falling back to a bare `seam` resolved from `PATH` at
hook time. The command fails if `mcp.api_key` is empty.

An http entry looks like this:

```text
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "seamless_managed": true,
        "hooks": [
          {
            "type": "http",
            "url": "http://127.0.0.1:8081/api/hooks/user-prompt-submit",
            "timeout": 5,
            "headers": { "Authorization": "Bearer <mcp.api_key>" }
          }
        ]
      }
    ]
  }
}
```

A command entry carries no key and bakes in the absolute config path:

```text
{
  "seamless_managed": true,
  "matcher": "startup|resume|clear|compact",
  "hooks": [
    {
      "type": "command",
      "command": "SEAMLESS_CONFIG=/abs/path/seamless.yaml /abs/path/seam hook session-start",
      "timeout": 10
    }
  ]
}
```

`SEAMLESS_CONFIG` is baked in because the hook fires from any cwd, where the
CLI's cwd-relative search for `seamless.yaml` would miss and leave it unable to
authenticate. It is omitted when the config came from defaults and env with no
file. The `matcher` key is omitted entirely for hooks that have none.

Install behavior:

- **Unknown keys are preserved.** The file is decoded into a generic map, so
  everything Seamless does not own survives.
- **Backed up once.** The first time Seamless changes the file it copies it to
  `settings.json.seamless-bak-<timestamp>`. Later installs skip the backup, so
  the true original is never overwritten with a modified copy.
- **Idempotent.** An already-current file is left untouched - no rewrite, no
  backup. Per-hook actions are reported as `added`, `updated`, `adopted`,
  `deduped`, or `unchanged`.
- **Written atomically**, sorted-key indented, preserving the file mode (0600 for
  a file Seamless creates, since it may hold a bearer key).

### Why detection does not use the marker

Entries are tagged `seamless_managed: true`, but **that key cannot be trusted for
detection**. Claude Code re-serializes settings.json through its own schema
whenever the owner edits config or permissions, which drops the unknown
`seamless_managed` key while keeping the functional hook entries. Those hooks are
still firing, so they must still count as installed.

Both `Install` and `InstalledStatus` therefore treat an entry as Seamless-owned
if any of these hold:

1. It carries the `seamless_managed` marker; or
2. it is an http entry whose `url` matches the hook's URL under the base URL
   (trailing slash ignored); or
3. it is a command entry whose `command` ends in ` hook <event>` - whatever
   binary path or env prefix it carries.

Rules 2 and 3 are what make re-installs adopt an existing entry in place rather
than appending a duplicate beside it. When several owned entries exist for one
event, the first is replaced with the canonical form and the rest are dropped
(`deduped`).

The marker value is deliberately `seamless_managed`, distinct from Seam v1's
`seam_managed`, so the two never match and clobber each other. A v1 entry
pointing at a different URL is not matched by any of the three rules and is left
untouched.

## Related

- [Configuration](/reference/configuration/) - `mcp.api_key`, the bind address
  the hook URLs derive from, and the `briefing:` block that tunes what
  `SessionStart` injects.
- [MCP API overview](/reference/mcp/) - the tool surface the same daemon serves.
- [Quickstart](/quickstart/) - install order for a working setup.
