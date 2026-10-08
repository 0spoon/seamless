---
title: Claude app chat setup
description: Register the seam mcp-proxy bridge in claude_desktop_config.json, restart the app, and run the session loop explicitly - a chat has no hooks and no cwd.
---

The Claude desktop app hosts two different Seamless surfaces, and they are wired
differently:

- **Code sessions** inside the app are real Claude Code - they share
  `~/.claude`, so the hooks, MCP registration, and skills from your [Claude Code
  setup](/claude-code/) apply unchanged. See [the code surface inside the Claude
  app](/claude-code/#the-claude-apps-code-surface).
- **Chat conversations** are this page: a plain MCP client with **no hooks**,
  wired through `claude_desktop_config.json`. Seamless treats it as its own
  install target, named `claude-desktop`.

## No hooks means no ambient layer

Everything the hooks deliver on the code surface is absent in a chat. There is
no `<seam-briefing>` at conversation start, no per-prompt `<seam-recall>`, no
findings harvest when the conversation ends, no plan capture - and no
`CLAUDE.md`, so none of your standing agent guidance is in context either. The
chat surface installs no skills.

What a chat gets instead is the full MCP tool surface, which means the model has
to **run the loop itself** - the same explicit loop as any
[hand-integrated agent](/guides/integrate-your-agent/): `session_start` to bind
and fetch the briefing, `recall` before guessing, durable writes for what should
outlive the conversation, `session_end` to leave findings. The MCP handshake's
server instructions give the model that baseline workflow, but nothing forces
it - if a conversation never calls `session_start`, Seamless never hears about
it. Nor is a chat bound to a session the way a Claude Code agent is: that
[automatic binding](/concepts/sessions/#process-binding) joins an agent's tool
calls to the session its SessionStart hook opened, and a chat has no such hook.

## Register the bridge

```bash
seamlessd install-hooks --client claude-desktop
```

This is the same registration the interactive installer offers as menu entry
`[2] Claude app (chat)` - answers are comma lists, so `1,2` wires Claude Code
and the chat surface together, and `all` includes the chat surface on the
platforms that can host it (the Claude app ships for macOS and Windows; on
Linux `all` deliberately excludes it).

There is no management CLI for the app, so registration is a direct,
merge-preserving edit of the app's config file:

::: when os=macos

The file is `~/Library/Application Support/Claude/claude_desktop_config.json`.

:::

::: when os=windows

The file is `%APPDATA%\Claude\claude_desktop_config.json`.

:::

::: when os=linux

The Claude app does not ship for Linux, so there is no chat surface to
register on this machine - the code-session and CLI setup on
[Claude Code setup](/claude-code/) is the whole story there.

:::

`--desktop-config PATH` overrides the location.

The edit adds one stdio entry under `mcpServers` - the reserved name
`seamless`, launching `seam mcp-proxy --config <absolute seamless.yaml>` - and
touches nothing else. Foreign entries round-trip byte-for-byte (other servers'
entries may hold credentials in `env`), the original file is backed up once
before the first change, and the write is verified by re-reading the file. **No
secret lands in the app's config**: the bridge reads the bearer key from
Seamless's `0600` config at connect time, the same policy as every other
registration (see [why MCP goes through a bridge](/codex-cli/#why-mcp-goes-through-a-bridge)).

Then **restart the app**. It reads the config only at startup; the installer
prints the same notice.

Two sharp edges, stated rather than discovered:

- `--client claude-desktop --mcp=false` is an error. The chat surface has no
  hooks and no skills, so skipping MCP leaves nothing to install.
- A foreign entry already holding the reserved `seamless` name is never
  overwritten - the installer refuses and names the manual fix.

## Registering by hand

In the app: **Settings > Developer > Edit Config**, then add under
`mcpServers`:

```json
"seamless": {
  "command": "/abs/path/seam",
  "args": ["mcp-proxy", "--config", "/abs/path/seamless.yaml"]
}
```

Use the absolute installed `seam` and config paths, save, and restart the app.
This is also the repair path whenever the automatic edit refuses to run.

## Scope discipline in a chat

A chat conversation has no working directory, and cwd is how every other
surface resolves scope. Three things worth knowing before the first durable
write - the two failure modes are quiet, and neither is reliably a rejection:

- **Bind with `project`.** When the conversation is about a project, have
  Claude call `session_start project=<slug>` - or pass the repo's absolute path
  as `cwd`, which resolves through the repo map. Either buys the full project
  briefing and correctly scoped unscoped calls on that connection. An unknown
  slug creates the project, and the result's `warning` says so. The binding
  lives in the daemon's memory, so after a reconnect (a daemon or app restart)
  call `session_start` again.
- **A `session_start` with neither `project` nor `cwd` binds the global
  scope.** The response's scope line warns, but every later unscoped durable
  write then lands global silently - nothing at write time flags it.
- **Skipping `session_start` is not a reliable way to fail closed.** An
  unscoped durable write with no bound session falls back to the sole active
  ambient session. The chat's `seam mcp-proxy` names the process that launched
  it (the app), so that fallback skips sessions owned by other agents'
  processes - a Claude Code or Codex session on a current `seam` - and the
  write is refused as ambiguous. A session started by an older `seam` carries
  no process stamp and is still a candidate, though: then the chat's write
  lands in **that session's project**, stamped with its provenance.

The discipline that avoids both: have the chat pass a `project` (or a real
`cwd`) at `session_start`, or pass `project:` explicitly on every durable
write. Use `project: global` only when global is the point. The full precedence
chain is in [Projects & scope](/concepts/projects/).

## Uninstall

`seamlessd uninstall` (default `--client all`) removes the chat-surface entry
along with everything else; `--client claude-desktop` limits the client step
to this entry alone, though uninstall always removes the service and binaries
too - it un-installs the program, not one client. Removal deletes only the
reserved `seamless` entry - every other key, including a now-empty
`mcpServers`, stays exactly as found - and prints the same restart notice,
since the app also loads config at startup only. To drop just this entry and
keep Seamless, edit the config by hand:
[Add or remove one client](/updating/#add-or-remove-one-client).

## Verify

```bash
seamlessd doctor
```

Look for the `claude desktop mcp` line. Not registered is an **info** line, not
a warning - the chat surface is an explicit opt-in, so doctor never nags you
into it. An exact registration reports OK with one honest caveat: whether the
*running* app has actually loaded it is unverifiable, because the app reads the
config at startup and exposes no way to ask. If you registered and did not
restart, doctor cannot tell - the restart is on you.

The live evidence behind what this page claims - protocol handshake, in-app
tool calls, and the scope gotchas above - is recorded in the
[Claude app compatibility matrix](/reference/claude-app-compatibility/).
