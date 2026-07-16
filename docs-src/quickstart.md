---
title: Quickstart
description: Install Seamless, point Claude Code at it, and confirm the first briefing lands - three commands and a check.
---

This is the one happy path: install, serve, wire your agents, and watch a
session open with a briefing. Every fork in the road is a link, not a branch in
these steps.

No CGO toolchain, no external database, no Node. Building from source needs
**Go 1.25+**; the prebuilt binaries need nothing.

## Install and run

```bash
go install github.com/0spoon/seamless/cmd/...@latest   # seamlessd + seam
seamlessd serve                                        # 127.0.0.1:8081, data in ~/.seamless
```

No Go toolchain? Every tagged release ships prebuilt archives for macOS and
Linux (amd64 and arm64) with both binaries inside - grab one from
[GitHub releases](https://github.com/0spoon/seamless/releases) and put
`seamlessd` and `seam` on your PATH.

On a true first run - no config file anywhere - `serve` generates the bearer
key and writes it to `~/.config/seamless/seamless.yaml`. Nothing to copy,
nothing to paste.

Working from a clone instead? `make build && make run` does the same with
`./bin/` binaries, and `make install` sets the daemon up as a service - see
[Install & deploy](/install/).

`seamlessd doctor` is the checkpoint: it validates the config, opens the
database, applies migrations, and asserts the tool count. If it is green, the
daemon will start.

## Wire your agents

```bash
seamlessd install-hooks
seamlessd map-repo --path ~/code/myrepo --project myrepo
```

`install-hooks` does both halves of the Claude Code wiring: it installs the
hooks (briefing on SessionStart, recall on UserPromptSubmit, harvest on
SessionEnd) and registers the MCP server via `claude mcp add --scope user`. If
the `claude` CLI is not on your PATH it prints the command to run yourself -
see [Claude Code setup](/claude-code/) for the by-hand version and why
`--scope user` matters.

The hooks are what make sessions *ambient*: an agent gets a briefing without
calling a tool. `map-repo` binds a working directory to a project, so agents
working in that repo inherit project scope without passing `project` on every
call.

## Confirm it works

Start a Claude Code session in the mapped repo. Its context now opens with an
injected briefing:

```text
<seam-briefing>
Seam project: seamless -- 4 constraints, 58 memories, 3 recent findings.
CONSTRAINT: errcheck-check-blank-two-category-rule: errcheck runs with check-blank ...
...
</seam-briefing>
```

If you see that block, the loop is closed: the hook resolved your cwd to a
project, the daemon assembled a briefing within its token budget, and the agent
started with your knowledge already in context.

Open the console to watch it happen:

```bash
seamlessd console-open                               # opens pre-authenticated
```

## Next steps

- [Install & deploy](/install/) makes the daemon a service that survives
  reboots instead of a foreground process.
- From a clone, `make install-onboard-skill` installs a `/seam-onboard` Claude
  Code skill that walks an agent through this setup and verifies each step.
- Look up any tool, key, or command in the [Reference](/reference/).
