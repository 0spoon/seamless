---
title: Quickstart
description: Build Seamless, point Claude Code at it, and confirm the first briefing lands — one happy path, about ten commands.
---

This is the one happy path: build from source, run the daemon, register it with
Claude Code, and watch an agent get its first briefing. Every fork in the road is
a link, not a branch in these steps.

Requires **Go 1.25+**. No CGO toolchain, no external database, no Node.

## Build and run

```bash
git clone https://github.com/0spoon/seamless
cd seamless
cp seamless.yaml.example seamless.yaml
openssl rand -hex 32              # put the result in mcp.api_key
make build                        # ./bin/seamlessd + ./bin/seam
make doctor                       # config, database, and tool-count self-checks
make run                          # serve on 127.0.0.1:8081
```

`make doctor` is the checkpoint: it validates the config, opens the database,
applies migrations, and asserts the tool count. If it is green, the daemon will
start.

## Register the MCP endpoint

```bash
claude mcp add --scope user --transport http seamless http://127.0.0.1:8081/api/mcp \
  --header "Authorization: Bearer $KEY"
```

`--scope user` is required. Without it `claude mcp add` defaults to `local`,
which ties the registration to whichever directory you happened to run it from —
and the tools then vanish in every other repo.

## Install the hooks and map a repo

```bash
./bin/seamlessd install-hooks                        # SessionStart/UserPromptSubmit/SessionEnd
./bin/seamlessd map-repo --path ~/code/myrepo --project myrepo
```

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
make console                                         # opens pre-authenticated
```

## Next steps

- `make install-onboard-skill` installs a `/seam-onboard` Claude Code skill that
  walks an agent through this setup and verifies each step.
- Look up any tool, key, or command in the [Reference](/reference/).

