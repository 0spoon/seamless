# Connect Cursor, Cline, Windsurf & Zed

> Point any MCP client at Seamless's local streamable-HTTP endpoint with bearer auth - a verified config block per client, and an honest account of what a client without hooks does and does not get.

Seamless serves its whole tool surface as a **local streamable-HTTP MCP
server** on localhost:

```text
POST http://127.0.0.1:8081/api/mcp
Authorization: Bearer <mcp.api_key>
```

Any MCP client that can send a URL plus a bearer header - or spawn a local
stdio server - can connect to it. This page is the verified configuration for
four such clients: Cursor, Cline, Windsurf, and Zed.

First, the honest part.

## What these clients get, and what they do not

[Hooks](https://thereisnospoon.org/docs/reference/hooks/) exist for exactly two clients: Claude Code (seven
hooks) and Codex (five). Hooks are what makes Seamless *ambient* - a briefing
injected at session start, prompts matched against stored memories, findings
harvested when the session ends, all without the agent calling a tool. Every
other client connects as a plain MCP client: the [full tool
surface](https://thereisnospoon.org/docs/reference/mcp/) works, and nothing happens by itself.

| Capability | Claude Code | Codex | Cursor / Cline / Windsurf / Zed |
|---|---|---|---|
| Every MCP tool | yes | yes | yes |
| Briefing at session start | hook-injected | hook-injected | agent calls `session_start` |
| Prompt-matched recall injection | every prompt | every prompt | agent calls `recall` explicitly |
| Findings harvested | at session end | at turn end | agent calls `session_end` |
| Plan-mode capture | yes | no | no |

The right column is a manual mode, not a broken one. The loop the agent has to
run itself - `session_start` to bind scope, `recall` before guessing,
`session_end` with findings - is short, and [Integrate your
agent](https://thereisnospoon.org/docs/guides/integrate-your-agent/) walks through every step of it.
[Make the agent run the loop](#make-the-agent-run-the-loop), below, shows how
to put that loop into a rules file so the agent actually does.

## Before the config

Three facts every block below shares:

- The daemon binds **localhost only** (`127.0.0.1:8081` by default), so the
  client has to run on the same machine. `curl -s
  http://127.0.0.1:8081/healthz` proves it is up, no auth needed.
- The bearer key is `mcp.api_key` in your
  [`seamless.yaml`](https://thereisnospoon.org/docs/reference/configuration/) (usually
  `~/.config/seamless/seamless.yaml`). `seam mcp-headers` prints the current
  `Authorization` header as JSON if you would rather not open the file.
- **Auth is checked at `tools/call`, not at connect time.** A client with a
  wrong key still initializes and lists tools, so the server can show as
  connected while every call fails with `unauthorized: valid bearer key
  required` as a tool-error result. If tools list but never work, check the
  key before anything else.

## Cursor

Cursor connects to a local HTTP MCP server with a `url` and a bearer header.
Register Seamless globally in `~/.cursor/mcp.json` - one daemon serves every
repository, so the global file is the right scope (a per-project
`.cursor/mcp.json` also works, but would put the key in the repo's
working tree):

```json
{
  "mcpServers": {
    "seamless": {
      "url": "http://127.0.0.1:8081/api/mcp",
      "headers": {
        "Authorization": "Bearer <mcp.api_key>"
      }
    }
  }
}
```

A bare `url` is treated as streamable HTTP; no transport field is needed. If
you would rather not store the key as a literal, Cursor interpolates
`${env:VAR}` inside `headers` - or skip the problem entirely with the
[stdio bridge](#keep-the-key-out-of-client-config).

## Cline

Cline configures MCP servers from its panel: MCP Servers, then Configure MCP
Servers, which opens `cline_mcp_settings.json`:

```json
{
  "mcpServers": {
    "seamless": {
      "type": "streamableHttp",
      "url": "http://127.0.0.1:8081/api/mcp",
      "headers": {
        "Authorization": "Bearer <mcp.api_key>"
      },
      "disabled": false,
      "autoApprove": []
    }
  }
}
```

The `type` must be exactly `streamableHttp` - camelCase, no hyphen. Spelled
`streamable-http`, or omitted, Cline falls back to the legacy SSE transport,
which the daemon does not serve, and the connection fails with an unhelpful
error.

## Windsurf

Windsurf's MCP config lives at `~/.codeium/windsurf/mcp_config.json`. It
spells both fields differently from the clients above: the endpoint goes in
`serverUrl` (not `url`), and the transport is `streamable-http` (hyphenated
where Cline camelCases - neither client accepts the other's spelling):

```json
{
  "mcpServers": {
    "seamless": {
      "type": "streamable-http",
      "serverUrl": "http://127.0.0.1:8081/api/mcp",
      "headers": {
        "Authorization": "Bearer <mcp.api_key>"
      }
    }
  }
}
```

Windsurf interpolates `${env:VAR}` and `${file:/abs/path}` inside `headers`
if you want the key out of the JSON.

## Zed

Zed's custom context servers speak **stdio only** - there is no native
streamable-HTTP registration. That is exactly the shape `seam mcp-proxy`
exists for: it speaks MCP over stdio to the client and forwards every frame
to `/api/mcp`, reading the bearer key from Seamless's own 0600 config. In
Zed's `settings.json`:

```json
{
  "context_servers": {
    "seamless": {
      "source": "custom",
      "command": "/abs/path/to/seam",
      "args": ["mcp-proxy", "--config", "/abs/path/to/seamless.yaml"],
      "env": {}
    }
  }
}
```

Use absolute paths for both the binary and the config - Zed launches the
command from its own working directory, not your repo. The generic
`mcp-remote` proxy would also bridge Zed to a local HTTP server, but it takes
the bearer token on its command line, which lands the secret in
`settings.json`; the bridge keeps it in the one file that already holds it.

## Keep the key out of client config

The bridge is not Zed-specific. Every client on this page can spawn a stdio
server, so every one of them can trade its HTTP block for `command` + `args`
and hold no key at all - the same policy choice Seamless makes when
[installing for Codex](https://thereisnospoon.org/docs/codex-cli/):

```json
{
  "mcpServers": {
    "seamless": {
      "command": "/abs/path/to/seam",
      "args": ["mcp-proxy", "--config", "/abs/path/to/seamless.yaml"]
    }
  }
}
```

(Adapt the envelope to the client: Cline keeps its `disabled`/`autoApprove`
fields, Zed uses `context_servers` with `source: "custom"` as above.)

Direct HTTP is one fewer process; the bridge is one fewer place the key
lives. Both carry the full protocol, including the `Mcp-Session-Id` binding
that [session scope](https://thereisnospoon.org/docs/guides/integrate-your-agent/) depends on, so nothing on
this page changes between them.

## Make the agent run the loop

Without hooks, nothing tells the agent that Seamless exists or when to use
it. The fix is standing instructions in whatever rules file the client reads
(Cursor's `.cursor/rules`, `.clinerules`, `.windsurfrules`, Zed's `.rules`):

```text
Seamless is this machine's shared agent memory (MCP server "seamless").
- Start every task by calling session_start with cwd set to the repo root,
  and read the briefing it returns.
- Call recall before re-deriving project knowledge or asking the user.
- Durable knowledge goes to memory_write (compact) or notes_create (long);
  do not duplicate what the code or the current conversation already holds.
- Before finishing, call session_end with findings - it is what the next
  agent's briefing is built from.
```

That is the whole loop. The subtleties - scope resolution, when a write is
rejected as ambiguous, what deserves to be a memory - are covered in
[Integrate your agent](https://thereisnospoon.org/docs/guides/integrate-your-agent/) and [Write memories
that get recalled](https://thereisnospoon.org/docs/guides/write-good-memories/).

## Verify the connection

```bash
seam doctor        # /healthz, key acceptance, tools/list count
```

A green `mcp_tools` line proves the endpoint answers and the key works - the
two things a client config can get wrong. Then, in the client, ask the agent
to call `session_start`: getting a briefing back end-to-end exercises auth,
session binding, and scope in one call. If the tool list shows up but calls
fail, re-read the auth note [above](#before-the-config); for anything else,
[Troubleshooting](https://thereisnospoon.org/docs/guides/troubleshooting/) is symptom-first.
