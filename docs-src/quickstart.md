---
title: Quickstart
description: Install Seamless with one command, start Claude Code in a repo, and confirm the first briefing lands.
---

This is the one happy path: install, start a session, and watch it open with a
briefing. It is one command and a check - everything between them is automatic.
Every fork in the road is a link, not a branch in these steps.

No CGO toolchain, no external database, no Node. The installer needs `curl` and
`tar`; building from source needs **Go 1.25+**.

## Install

```bash
curl -fsSL https://thereisnospoon.org/install | sh
```

One command does the lot: it fetches the checksum-verified release archive for
your platform (macOS and Linux, amd64 and arm64), installs `seamlessd` and
`seam` into `~/.local/bin`, generates the bearer key, wires Claude Code, and
starts the daemon as a per-user service on `127.0.0.1:8081` with data in
`~/.seamless`. Re-run it to upgrade. [Install & deploy](/install/) has the
overrides, the service details, and how to remove it.

Piping a stranger's script into a shell deserves a read first - it is
[one file](https://thereisnospoon.org/install), and every other route to the
same place is on that page: prebuilt archives from
[GitHub releases](https://github.com/0spoon/seamless/releases),
`go install github.com/0spoon/seamless/cmd/...@latest`, or `make install` from
a clone.

On a true first run - no config file anywhere - the bearer key is generated and
written to `~/.config/seamless/seamless.yaml`. Nothing to copy, nothing to
paste.

No LLM key is required either: without one, recall degrades to plain full-text
search. Add OpenAI or Ollama in the [configuration](/reference/configuration/)
when you want semantic recall.

`seamlessd doctor` is the checkpoint: it validates the config, opens the
database, applies migrations, and asserts the tool count. If it is green, the
daemon will start.

## Wire your agents

The installer already ran `install-hooks` for you, so there is no second step:
start Claude Code in any git repo.

```bash
cd ~/code/myrepo && claude
```

`install-hooks` does both halves of the Claude Code wiring: it installs the
hooks (briefing on SessionStart, recall on UserPromptSubmit, harvest on
SessionEnd) and registers the MCP server via `claude mcp add --scope user`. If
the `claude` CLI is not on your PATH it prints the command to run yourself -
see [Claude Code setup](/claude-code/) for the by-hand version and why
`--scope user` matters. Installed by hand? Run `seamlessd install-hooks`
yourself first.

The hooks are what make sessions *ambient*: an agent gets a briefing without
calling a tool. They also register the repo. On the first session inside a git
repo, the SessionStart hook resolves your cwd to its git root, derives a project
slug from that directory's name, and records the mapping - so agents inherit
project scope without passing `project` on every call, and without you creating
a project first. `seamlessd map-repo --path ~/code/myrepo --project myrepo` is
the override when you want a slug that is not the directory name; see
[Projects & scope](/concepts/projects/) for the full precedence chain.

Restart Claude Code once after installing so it loads the hooks and the MCP
server.

## Confirm it works

That session's context now opens with an injected briefing:

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
