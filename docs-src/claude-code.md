---
title: Claude Code setup
description: Register the MCP endpoint, install the hooks that make sessions ambient, map your repos, and verify each step.
---

Claude Code is the client Seamless is built around. Wiring it up is two
independent halves - **the MCP endpoint** (what an agent can call) and **the
hooks** (what happens without an agent calling anything) - and you want both.
One command installs both.

## Install the hooks and register MCP

```bash
seamlessd install-hooks
```

This merges six hook entries into your Claude Code `settings.json`, preserving
everything already there and backing the file up once before the first change.
It is idempotent: an already-current file is left untouched.

It then registers the MCP server with the `claude` CLI (`claude mcp add
--scope user`). Pass `--mcp=false` to skip that half; if the CLI is missing or
the registration fails, the hooks still land and the command prints the
`claude mcp add` invocation to run yourself.

The hooks are what make sessions **ambient** - an agent gets a briefing at
startup, its prompts get matched against your memories, its findings get
harvested at the end, and its plan-mode activity gets captured, all without the
agent choosing to call anything. See [Sessions & briefings](/concepts/sessions/)
for what that delivers and [the hooks reference](/reference/hooks/) for the
exact table.

### Why some hooks are commands and others are http

This looks inconsistent and is not:

- **SessionStart** must be a command hook. Claude Code only runs `command` and
  `mcp_tool` hooks for SessionStart - an `http` one is *silently skipped*, and
  the briefing and ambient session simply never fire.
- **SessionEnd** does support http, but at process exit a fire-and-forget request
  races the teardown, so the harvest often never lands and sessions pile up as
  active. As a command hook, Claude Code waits for it.
- **UserPromptSubmit** fires mid-turn where http is reliable, so it stays http
  (and carries the bearer key in `settings.json`).
- **The plan-capture hooks** are commands because `seam` pre-filters locally: the
  machine-wide `Write`/`Edit` hot path should never touch the network for files
  that have nothing to do with plans.

## Registering the MCP endpoint by hand

For a different MCP client, or when `install-hooks` could not find the
`claude` CLI:

```bash
claude mcp add --scope user --transport http seamless http://127.0.0.1:8081/api/mcp \
  --header "Authorization: Bearer $KEY"
```

**`--scope user` is required.** Without it, `claude mcp add` defaults to `local`,
which ties the registration to whichever directory you happened to run it from.
The tools then work in that one directory and silently vanish everywhere else -
which reads exactly like "the MCP server is broken" rather than "the
registration is scoped wrong".

The key is `mcp.api_key` from your config. Find it:

```bash
grep api_key ~/.config/seamless/seamless.yaml   # release layout
grep api_key ./seamless.yaml                    # dev layout
```

## Map your repos

Usually you do not have to. The first session inside a git repo maps itself: the
SessionStart hook resolves the agent's cwd to its git root, derives a project
slug from that directory's name, and records the mapping. Agents then inherit
project scope from their cwd - no `project` argument on any call.

Map by hand to override the derived slug - an `ios` directory that should be the
`arctop-ios` project:

```bash
seamlessd map-repo --path ~/code/ios --project arctop-ios
```

See [Projects & scope](/concepts/projects/) for the full precedence chain and why
unmapped writes are rejected rather than defaulted to global.

## Tell your agents it exists

Installing the tools does not mean an agent will use them well. Two things help.

First, run `/seam-onboard`. The installer drops the portable Claude Code copy
into `~/.claude/skills/`; from a clone, `make install-onboard-skill`
(re)installs it for the default `CLIENT=claude` profile:

```bash
make install-onboard-skill    # (re)install the /seam-onboard skill from a clone
```

`/seam-onboard` walks an agent through the setup above, verifies each step, and
writes a Seamless-awareness block into your `CLAUDE.md` before removing itself.

The installer drops a second skill alongside it: `/seam-research`, the
research-lab workflow for systematic debugging (see [Run research
trials](/guides/research-trials/)). Unlike `/seam-onboard` it is a recurring
workflow, not a one-shot - it never self-removes, upgrades refresh it in place,
and Claude can activate it on its own when an investigation calls for
structured trials. From a clone, `make install-research-skill` (re)installs it.

Second - or if you skip the skill - add that block to your `CLAUDE.md` by hand:
describe when to reach for Seamless (memory that should outlive the
conversation, work that crosses agents) and when not to (trivial edits, things
the codebase already records).
The briefing tells an agent *what you know*; your `CLAUDE.md` tells it *when to
write more down*.

## Verify

```bash
./bin/seamlessd doctor    # config, database, tool count, and the hooks check
```

Then start a Claude Code session in a mapped repo and look for the injected
block:

```text
<seam-briefing>
Seam project: myrepo -- 4 constraints, 58 memories, 3 recent findings.
...
</seam-briefing>
```

If it is there, the whole loop is closed: the hook resolved your cwd to a
project, the daemon assembled a briefing within its token budget, and the agent
started already knowing what you know.

If it is **not** there, note that hooks fail open by design - a broken hook never
blocks your agent, which means **silence is the failure mode**. Start with
`seamlessd doctor`; the [Troubleshooting](/guides/troubleshooting/) guide is
organized by symptom.
