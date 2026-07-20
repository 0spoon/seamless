---
title: Integrate your agent
description: Wire another agent into the loop - the MCP handshake, stdio bridge, session binding, scope discipline, and seam CLI fallback.
---

[Claude Code](/claude-code/) and [Codex](/codex-cli/) get Seamless mostly for
free: [hooks](/reference/hooks/) open a session, inject a briefing, and harvest
findings without the agent deciding to. Any other client has no hooks. It has to
run the loop itself.

This page is that loop, for a client you are wiring up by hand.

## The loop

```text
session_start   ─▶ bind the connection, get the briefing
     │
     ├─ recall / memory_read     ─▶ pull what the briefing summarized
     ├─ ... do the work ...
     ├─ memory_write / notes_create ─▶ what should outlive this run
     │
session_end     ─▶ persist findings for the next agent's briefing
```

Four of those five steps are optional in the narrow sense that the tools work
without them. They are not optional in the sense that matters: **`session_start`
is what makes every later call know its scope**, and `session_end` is the only
thing that turns this run into something the next agent is told about. An agent
that skips both still works and leaves no trace - which is the same as not
integrating at all.

## Connecting

Seamless serves streamable-HTTP MCP at `/api/mcp` behind one static bearer key
([`mcp.api_key`](/reference/configuration/)). If your client speaks MCP over
HTTP, point it here and you are done. If you are writing the transport yourself,
the handshake is two calls.

**Client requires or prefers MCP over stdio?** Bridge it with `seam mcp-proxy`,
which speaks stdio to the client and forwards each frame to `/api/mcp`, carrying
the bearer key from config and preserving `Mcp-Session-Id` so session binding
survives:

```bash
seam mcp-proxy --config /abs/path/seamless.yaml   # invoked by the MCP client, not by hand
```

Register it the way your client registers a stdio server. Codex supports both
stdio and direct Streamable HTTP; Seamless deliberately installs the bridge with
`codex mcp add seamless -- /abs/path/seam mcp-proxy --config /abs/path/seamless.yaml`,
because this keeps the bearer key in Seamless's 0600 config. `seamlessd
install-hooks --client codex` does that for you. See [Codex local
setup](/codex-cli/).
The rest of this page - session binding, scope, findings - applies to direct and
bridged clients unchanged.

```bash
KEY=<mcp.api_key>

curl -sD - -X POST http://127.0.0.1:8081/api/mcp \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
       "params":{"protocolVersion":"2025-06-18","capabilities":{},
                 "clientInfo":{"name":"my-agent","version":"1"}}}'
```

The response body carries `serverInfo` (name and build version) plus concise
server `instructions` describing the Seamless workflow. The part your transport
must keep is a **header**: `Mcp-Session-Id: mcp-session-<uuid>`. Send it on every
subsequent request. A `tools/call` without it - or with one the daemon does not
know - is refused by the transport with `Invalid session ID` before any tool
runs.

Acknowledge the handshake, then call tools:

```bash
SID=<the Mcp-Session-Id from above>

curl -s -X POST http://127.0.0.1:8081/api/mcp \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -s -X POST http://127.0.0.1:8081/api/mcp \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call",
       "params":{"name":"session_start",
                 "arguments":{"cwd":"/abs/path/to/repo","source":"explicit"}}}'
```

Every tool returns its payload as JSON **encoded into a text content block**, not
as a JSON-RPC result object:

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"session_id\":\"01K...\",\"project\":\"myrepo\",\"briefing\":\"<seam-briefing>...\"}"}]}}
```

So parse twice: once for the envelope, once for the `text`.

### Auth is enforced at the tool, not the transport

`initialize` and `tools/list` answer without a key. Only `tools/call` checks it,
and a rejected call comes back as a **successful HTTP 200 carrying a tool error**:

```json
{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"unauthorized: valid bearer key required"}],"isError":true}}
```

A client that trusts HTTP status codes will read that as a working integration
returning odd text. Check `isError` on every result. This is the same shape every
other tool failure takes - errors are tool results with a `<tool>: <reason>`
message, never transport failures - so handling `isError` once handles all of
them.

## The connection is the session binding

`session_start` binds the session to your connection, keyed by the transport's
`Mcp-Session-Id`. That binding is what lets later calls omit `project` and
`session` entirely: `memory_write`, `recall`, `tasks_claim`, and the rest read the
scope off the connection.

The binding lives in the daemon's memory, keyed by an id it minted. Two
consequences you have to design around:

- **The id encodes nothing about you** - no working directory, no client
  identity. It is an opaque UUID. The only way scope reaches the server is the
  `cwd` you passed to `session_start`, or an explicit `project` argument.
- **It does not reliably survive a daemon restart.** Sometimes the client
  re-initializes and is minted a new id, orphaning the old binding; sometimes it
  sends the old id and the fresh process accepts it. Which one happens is a race.

So: **re-run `session_start` on reconnect**, with the same `name` to resume the
same session rather than opening a second one. Treat a sudden run of
ambiguous-scope errors as a lost binding, not as a bug in your arguments.

One more `session_start` argument worth passing from a custom client: `model`,
the model id powering your agent exactly as the provider names it
(`claude-fable-5`, `gpt-5.5`). Every memory and note the session writes is
stamped with it (`model` in the frontmatter), so the store records which model
produced each piece of knowledge. Claude Code and Codex sessions get this for
free from the hooks; a bare MCP client only has attribution if it self-reports.

## Scope discipline

Scope is the thing most integrations get wrong, and the failures are quiet -
knowledge lands somewhere nobody looks. The full precedence chain is in
[Projects & scope](/concepts/projects/); the operational summary is short.

| Situation | What resolves |
|---|---|
| `session_start` with a `cwd` inside a mapped repo | That repo's project |
| `session_start` with a `cwd` inside an **unmapped git repo** | A project is registered automatically, named after the repository root directory |
| `session_start` with a `cwd` that is not in a git repo | Nothing - the session is global |
| No session, no `project` argument | The durable write is **rejected** |

That third row is the one that surprises people. A session started outside a git
repo has no project, and its writes go global - silently, because a bound session
always resolves, even to the global scope. If your agent does not run in a repo,
pass `project` explicitly on every durable write.

Writes **fail closed**. A `memory_write` with nothing to infer scope from is
rejected as ambiguous rather than landing globally, because a global memory is
seen by every agent in every repo forever and that is not something the system
should do when it is *unsure*. Two distinct errors say so:

- *no bound or ambient session to infer the project from* - nothing to inherit.
  Call `session_start`, or pass `project`.
- *active ambient sessions span multiple projects* - you are unbound and other
  agents are live in several repos, so inheriting would bleed your write into
  someone else's project. Pass `project`.

Pass `project: global` when you mean global. It is a token you use on purpose,
never a default you fall into.

## When to write a memory, and when to skip

The budget is real: constraints are never dropped, so everything else competes for
what is left of a briefing. A memory that does not earn its line pushes out one
that would have.

Write one when the knowledge is **durable, general, and not already discoverable**
- a constraint the project cannot violate, a trap and its symptom, a decision and
the alternatives it rejected, a belief that turned out false.

Skip it when:

| The knowledge is | Why not a memory |
|---|---|
| Already in the code | The next agent can read the code. A store that mirrors the repo is a store that goes stale silently |
| Already in `CLAUDE.md` or `AGENTS.md` | It is injected anyway; a memory saying it again just spends budget twice |
| A narration of what you just did | That is `session_end` findings, or a note |
| A long artifact | That is a note - found via [recall](/concepts/recall/), not injected |

If it is durable but you cannot compress it to one line, it is a note with a
memory pointing at it. See [Write memories that get
recalled](/guides/write-good-memories/).

## The seam CLI as a fallback client

An agent that can run a shell but cannot speak MCP still has the loop. `seam` is
a headless client for the same daemon, authenticating with the same key:

```bash
seam prime --cwd /abs/path/to/repo        # session_start; prints the briefing
seam recall "why is the console theme split"
seam remember --name lease-steal-window --kind gotcha \
  --description "..." --body "..."        # or pipe the body on stdin
seam ready                                # the actionable queue
seam task claim --lease 1800 01K7ABCD     # flags on either side of the id
```

Two things to know before you script it.

**Flags go on either side of a positional.** Every `seam` command parses flags
and positionals in any order, so `seam task claim 01K7ABCD --lease 1800` and
`seam task claim --lease 1800 01K7ABCD` are the same line. A typo'd flag is an
error rather than a silently different command. See the
[seam CLI reference](/reference/cli-seam/).

**There is no `seam session-end`.** The CLI covers start, write, search, and the
queue, but findings are harvested by the SessionEnd hook or the `session_end` MCP
tool. An agent driving Seamless purely through `seam` starts sessions that only
the idle reaper closes, and contributes nothing to the next briefing. If findings
matter - and they are the whole point of `session_end` - that agent needs the MCP
surface.

## Verify the integration

```bash
seam doctor        # /healthz, key acceptance, tools/list count, project_list
```

A green `mcp_tools` line proves the endpoint answers *and* your key works, which
is most of what an integration can get wrong. `seamlessd doctor` covers the other
half - config, database, embedder, hooks - on the server side. If something is
wrong and nothing is complaining, start at
[Troubleshooting](/guides/troubleshooting/): the hooks fail open, so silence is
the failure mode.
