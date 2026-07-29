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

Create a work note -- a research finding, decision record, meeting summary, or any artifact long enough to deserve its own file. Auto-tagged created-by:agent. Notes carry the long form; memory_write carries the compact durable knowledge a future session must not miss, so put the write-up here and the one-line lesson there rather than duplicating either. Pass plan=&lt;slug&gt; when the note is a plan's narrative or supporting context, so it joins that plan's composition beside its tasks. Do not use this for what the repo, AGENTS.md/CLAUDE.md, or the current conversation already records, and use notes_append to extend an existing note rather than creating a near-duplicate of it.

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

Read a note by id, or by slug within the current project (falling back to a global note of the same slug).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | no | note id (ULID); pass exactly one of id or slug |
| `project` | string | no | project slug for the slug lookup; defaults to the bound session's project |
| `slug` | string | no | note slug, as briefings, plan compositions, and notes_create responses name notes (alias: name) |

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

Append a UTC-timestamped line to an existing note's body, by id. Use it when a note is already the right home for what you learned -- a running investigation log, a decision record gaining one more data point -- so the note keeps its id, slug, and place in any plan composition instead of fragmenting into near-duplicates. Appending only ever adds: use notes_update to correct or restructure what is already there, and notes_create when the finding deserves an artifact of its own. Needs the note's id (ULID), which notes_create returns and briefings and plan compositions carry; notes_read resolves one from a slug.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |
| `body` | string | **yes** | text to append (aliases: content, text) |

## notes_delete {#notes_delete}

Delete a note by id: the markdown file leaves the disk and its index row goes with it. Permanent, and it leaves no pointer behind, so reserve it for notes that should never have existed -- a duplicate, a write into the wrong project, an agent's own scratch. To fix a note's content use notes_update, and to add to it notes_append; neither loses the artifact. A note tagged into a plan (plan:&lt;slug&gt;) is that plan's narrative for whoever inherits it, so read it with notes_read before deciding it is disposable.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | note id (ULID) |

## project_list {#project_list}

List every project (slug, name, description). Use it to learn the exact slug before a deliberate cross-project write or a project_create -- coining a near-duplicate of a slug that already exists is the failure this prevents -- and to see whether work already has a home. You usually do NOT need it to pick a scope: memory, note, and task calls inherit the project from the session binding, and passing project= is for writing outside that on purpose. It returns identity, not contents; to search what is inside a project, use recall.

Takes no parameters.

## project_create {#project_create}

Register a project up front, with a human-readable name and an optional description. You rarely need this: any durable write naming an unknown project slug (memory_write, notes_create, tasks_add, capture_url, trial_record) already registers that project, and a git repo maps itself to one on its first session. Reach for this only to give a project a proper name and description BEFORE anything is written into it, or to create one you will not write to yet -- an auto-registered project is named after its own slug until someone fixes it. Call project_list first: coining a near-duplicate of an existing slug is the failure mode here. To divide an existing project into children, use gardener_split rather than creating them by hand. The slug defaults to a slugified name; "global" and "all" are reserved, and an existing slug is an error, not an update -- this never renames or edits a project.

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
