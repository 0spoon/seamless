# Claude app chat setup

> Register the seam mcp-proxy bridge in claude_desktop_config.json, restart the app, and run the session loop explicitly - a chat has no hooks and no cwd.

The Claude desktop app hosts two different Seamless surfaces, and they are wired
differently:

- **Code sessions** inside the app are real Claude Code - they share
  `~/.claude`, so the hooks, MCP registration, and skills from your [Claude Code
  setup](https://thereisnospoon.org/docs/claude-code/) apply unchanged. See [the code surface inside the Claude
  app](https://thereisnospoon.org/docs/claude-code/#the-claude-apps-code-surface).
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
[hand-integrated agent](https://thereisnospoon.org/docs/guides/integrate-your-agent/): `session_start` to bind
and fetch the briefing, `recall` before guessing, durable writes for what should
outlive the conversation, `session_end` to leave findings. The MCP handshake's
server instructions give the model that baseline workflow, but nothing forces
it - if a conversation never calls `session_start`, Seamless never hears about
it.

## Register the bridge

```bash
seamlessd install-hooks --client claude-desktop
```

This is the same registration the interactive installer offers as menu entry
`[3] Claude app (chat)` - answers are comma lists, so `1,3` wires Claude Code
and the chat surface together, and `all` includes the chat surface on the
platforms that can host it (the Claude app ships for macOS and Windows; on
Linux `all` deliberately excludes it).

There is no management CLI for the app, so registration is a direct,
merge-preserving edit of the app's config file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- `--desktop-config PATH` overrides the location.

The edit adds one stdio entry under `mcpServers` - the reserved name
`seamless`, launching `seam mcp-proxy --config <absolute seamless.yaml>` - and
touches nothing else. Foreign entries round-trip byte-for-byte (other servers'
entries may hold credentials in `env`), the original file is backed up once
before the first change, and the write is verified by re-reading the file. **No
secret lands in the app's config**: the bridge reads the bearer key from
Seamless's `0600` config at connect time, the same policy as every other
registration (see [why MCP goes through a bridge](https://thereisnospoon.org/docs/codex-cli/#why-mcp-goes-through-a-bridge)).

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
surface resolves scope. Three consequences worth knowing before the first
durable write - the failure modes are quiet, and two of them are **not**
rejections:

- **`session_start` has no `project` parameter.** The only way a chat session
  binds to a project is passing `cwd` - so when the conversation is about a
  repo, tell Claude the repo's absolute path and have it pass that as `cwd`.
  That one argument buys the full project briefing and correctly scoped
  unscoped calls for the rest of the session.
- **A cwd-less `session_start` binds the session to the global scope.** The
  response's scope line warns, but every later unscoped durable write then
  lands global silently - nothing at write time flags it.
- **Skipping `session_start` does not make writes fail closed.** An unscoped
  durable write with no bound session falls back to the sole active ambient
  session - and on a machine where you also run Claude Code or Codex, that is
  usually your *other* agent's session, so the chat's write lands in **that
  session's project**, stamped with its provenance. The
  "no session, no `project`, rejected" rule only holds when zero ambient
  sessions are live.

The discipline that avoids all three: have the chat pass a real `cwd` at
`session_start`, or pass `project:` explicitly on every durable write. Use
`project: global` only when global is the point. The full precedence chain is
in [Projects & scope](https://thereisnospoon.org/docs/concepts/projects/).

## Uninstall

`seamlessd uninstall` (default `--client all`) removes the chat-surface entry
along with everything else; `--client claude-desktop` scopes the run to just
it. Removal deletes only the reserved `seamless` entry - every other key,
including a now-empty `mcpServers`, stays exactly as found - and prints the
same restart notice, since the app also unloads config at startup only.

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
[Claude app compatibility matrix](https://thereisnospoon.org/docs/reference/claude-app-compatibility/).
