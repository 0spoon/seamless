---
title: MCP API overview
description: The endpoint, the auth model, the scope rules, and an index of every tool Seamless serves.
---

Seamless serves its tool surface over **streamable HTTP MCP** at
`/api/mcp`, guarded by a single static bearer key.

```text
POST http://127.0.0.1:8081/api/mcp
Authorization: Bearer <mcp.api_key>
```

`GET /healthz` needs no auth and reports the running build — the fastest way to
tell whether a daemon is up and which version it is.

## Scope resolution

Almost no call passes `project`. Scope is resolved once, in this order, and
inherited by everything after it:

1. An explicit `project` argument on the call.
2. The **bound session's** project — set by `session_start`, held per connection.
3. The **ambient session's** project, resolved from the agent's cwd via the
   `repo_project_map` setting.

Writes **fail closed**: with no session and no explicit `project`, a durable
write is rejected as ambiguous rather than silently landing in the global scope.
Pass `project: global` to mean global deliberately.

## Conventions

- **Body aliases.** Tools taking a markdown body accept `body`, `content`, or
  `text` interchangeably — agents disagree about the name, and the disagreement
  is not worth an error.
- **IDs are ULIDs**, never UUIDs. They sort lexically by creation time.
- **Errors** come back as tool errors with a `<tool>: <reason>` message, not as
  transport failures.

## The tool surface

Seamless registers **30 tools**. They are documented in groups:

| Group | Tools |
|---|---|
| Sessions, memory, and recall | `session_start`, `session_update`, `session_end`, `memory_write`, `memory_append`, `memory_read`, `memory_delete`, `recall` |
| Notes, projects, and capture | `notes_create`, `notes_read`, `notes_update`, `notes_append`, `notes_delete`, `project_list`, `project_create`, `capture_url` |
| [Tasks](tasks/) | `tasks_add`, `tasks_update`, `tasks_ready`, `tasks_list`, `tasks_claim`, `tasks_release` |
| Lab, gardener, and usage | `lab_open`, `trial_record`, `trial_query`, `gardener_proposals`, `gardener_request`, `gardener_split`, `gardener_apply`, `usage_summary` |

Every tool's parameters on these pages are generated from the running server's
own registration, so they cannot drift from what the daemon accepts.
