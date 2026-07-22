# MCP API overview

> The endpoint, the auth model, the scope rules, and an index of every tool Seamless serves.

Seamless serves its tool surface over **streamable HTTP MCP** at
`/api/mcp`, guarded by a single static bearer key.

```text
POST http://127.0.0.1:8081/api/mcp
Authorization: Bearer <mcp.api_key>
```

`GET /healthz` needs no auth and reports the running build - the fastest way to
tell whether a daemon is up and which version it is.

## Client transports and key handling

The daemon endpoint is always Streamable HTTP. A client can reach it in either
of two ways:

- **Direct HTTP** - configure the URL and an `Authorization: Bearer` header.
  Current Codex, Claude Code, and other HTTP-capable MCP clients support this
  shape.
- **A stdio bridge** - register `seam mcp-proxy --config <absolute yaml>` as a
  local stdio MCP server. The bridge forwards the protocol unchanged and keeps
  `Mcp-Session-Id` stable across HTTP calls.

Seamless installs the bridge for Codex by policy even though Codex supports both
stdio and Streamable HTTP. The bridge reads the bearer key from Seamless's 0600
config, avoiding a literal secret in `~/.codex/config.toml` or an environment
dependency. Claude Code's default direct-HTTP registration uses `seam
mcp-headers` as `headersHelper` for the same reason: the bearer value stays out
of client config and process argv.

The MCP initialize response also carries concise server instructions. Their
first 512 characters contain the essential cross-tool workflow: recall before
guessing, memory versus note, explicit scope when ambiguous, session handoff,
`plan:<slug>` composition, and treating `inputSchema` required fields/enums as
authoritative. That guidance is available even before a user runs the optional
onboarding skill.

## Scope resolution

Almost no call passes `project`. Scope is resolved once, in this order, and
inherited by everything after it:

1. An explicit `project` argument on the call.
2. The **bound session's** project - set by `session_start`, held per connection.
3. The **ambient session's** project, resolved from the agent's cwd via the
   `repo_project_map` setting.

Writes **fail closed**: with no session and no explicit `project`, a durable
write is rejected as ambiguous rather than silently landing in the global scope.
Pass `project: global` to mean global deliberately.

## Conventions

- **Body aliases.** Tools taking a markdown body accept `body`, `content`, or
  `text` interchangeably - agents disagree about the name, and the disagreement
  is not worth an error.
- **IDs are ULIDs**, never UUIDs. They sort lexically by creation time.
- **Errors** come back as tool errors with a `<tool>: <reason>` message, not as
  transport failures.

## The tool surface

Seamless registers **31 tools**. They are documented in groups:

| Group | Tools |
|---|---|
| Sessions, memory, and recall | `session_start`, `session_update`, `session_end`, `memory_write`, `memory_append`, `memory_read`, `memory_delete`, `recall` |
| Notes, projects, and capture | `notes_create`, `notes_read`, `notes_update`, `notes_append`, `notes_delete`, `project_list`, `project_create`, `capture_url` |
| [Tasks](https://thereisnospoon.org/docs/reference/mcp/tasks/) | `tasks_add`, `tasks_update`, `tasks_ready`, `tasks_list`, `tasks_claim`, `tasks_release` |
| Lab, gardener, and usage | `lab_open`, `trial_record`, `trial_query`, `gardener_proposals`, `gardener_request`, `gardener_split`, `gardener_apply`, `usage_summary`, `favorite_set` |

Every tool's parameters on these pages are generated from the running server's
own registration, so they cannot drift from what the daemon accepts.
