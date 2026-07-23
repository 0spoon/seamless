# Notes, projects & capture

> Work artifacts, project scope, and SSRF-safe URL capture - the eight tools around the edges of memory.

## A note is not a memory

This is the most common confusion in the whole system, so it is worth being blunt
about it.

|  | Memory | Note |
|---|---|---|
| Answers | "What is true about this project?" | "What did we produce?" |
| Length | One idea, one line of `description` | However long the artifact is |
| Lifecycle | Superseded and archived | Written, occasionally updated |
| Reaches a briefing | Yes - this is what agents get injected | No; found via `recall` |
| Good examples | A constraint, a gotcha, a decision | Research findings, a meeting summary, a design record |

The test: **would a future agent need this injected into its context before it
starts working?** If yes, it is a memory, and it needs to fit in a `description`.
If it is something you'd want to *find* and read in full when the topic comes up,
it is a note.

Writing a journal entry into memory is the classic failure: it is too long to
inject, too specific to generalize, and it pushes real constraints out of the
briefing's budget.

Agent-created notes are automatically tagged `created-by:agent`.

## Notes are how plans get their narrative

A plan is not a primitive - it is a composition keyed by `plan:<slug>`. Tag a
note `plan:<slug>` and it joins that plan's supporting context, so the next agent
inherits the design and the reasoning behind it, not just the step list. See
[Tasks](https://thereisnospoon.org/docs/reference/mcp/tasks/) for the step half of the composition.

## Projects and scope

`project_list` and `project_create` manage the scopes everything else inherits.
Most agents never call either: a git repo maps itself on the first session and
resolves its project from the agent's working directory, and `session_start`
binds it.

The `global` project slug is the deliberate cross-project scope. It is a token
you pass on purpose, never a default you fall into - see the fail-closed rule in
the [MCP API overview](https://thereisnospoon.org/docs/reference/mcp/).

## capture_url is SSRF-safe on purpose

`capture_url` fetches a URL and returns its content as markdown. It is the one
tool that makes an outbound request on an agent's behalf, so it is guarded:
destination ports are restricted to `capture.allowed_ports` (80 and 443 by
default, never "any port"), and the fetcher refuses to be talked into reaching
things it should not. See [Configuration](https://thereisnospoon.org/docs/reference/configuration/).

Scope resolves before the network fetch, so an ambiguous durable destination
fails without making a request. Success returns the note's `id`, `slug`, `title`,
resolved `project`, and `source_url` - the URL as requested; redirects are
followed but do not rewrite it. The fetcher validates the initial URL and every
redirect, rejecting non-HTTP schemes, private/loopback destinations, and
disallowed ports. Size is bounded by truncation rather than rejection: at most
2 MB of the response is read, and the stored readable body is capped again at
50,000 runes with a visible `[content truncated]` marker.

## notes_create {#notes_create}

Create a work note (research finding, decision record, summary). Auto-tagged created-by:agent.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `title` | string | **yes** | note title |
| `body` | string | **yes** | markdown body (aliases: content, text) |
| `description` | string | no | optional one-line summary |
| `plan` | string | no | optional plan slug (plan:&lt;slug&gt; convention): tags this note into that plan's composition so it surfaces on the Plans screen alongside its tasks_add plan=&lt;slug&gt; steps. Use it whenever this note is a plan's narrative or supporting context. |
| `project` | string | no | project slug; defaults to the bound/ambient session's project. An unknown slug CREATES that project -- naming a new one is normal and never an error. Pass project=global ONLY for knowledge that belongs in EVERY project's briefing; it is not a neutral default. With no session and no explicit project the call is rejected as ambiguous. |
| `source_url` | string | no | optional source URL |
| `tags` | array | no | tags (a comma-separated string is also accepted) |

## notes_read {#notes_read}

Read a note by id.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |

## notes_update {#notes_update}

Update a note's fields by id (title, description, body, project, tags). Omitted fields are untouched; tags replace all. The slug and id stay stable.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |
| `body` | string | no | new body (aliases: content, text) |
| `description` | string | no | new description |
| `project` | string | no | new project slug ("" or "global" = global scope) |
| `tags` | array | no | tags, replacing all (a comma-separated string is also accepted); an empty list is read as absent and leaves the tags untouched |
| `title` | string | no | new title |

## notes_append {#notes_append}

Append a timestamped line to a note's body.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |
| `body` | string | **yes** | text to append (aliases: content, text) |

## notes_delete {#notes_delete}

Delete a note by id (removes the file and its index).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |

## project_list {#project_list}

List all projects (slug, name, description).

Takes no parameters.

## project_create {#project_create}

Create a project. The slug defaults to a slugified name.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | **yes** | human-readable project name |
| `description` | string | no | optional one-line description |
| `slug` | string | no | optional explicit slug |

## capture_url {#capture_url}

Fetch a web page (SSRF-guarded: private/loopback addresses are rejected) and save its readable content as a note. Returns the new note's id.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `url` | string | **yes** | http(s) URL to capture |
| `project` | string | no | project slug; defaults to the bound/ambient session's project. An unknown slug CREATES that project -- naming a new one is normal and never an error. Pass project=global ONLY for knowledge that belongs in EVERY project's briefing; it is not a neutral default. With no session and no explicit project the call is rejected as ambiguous. |
