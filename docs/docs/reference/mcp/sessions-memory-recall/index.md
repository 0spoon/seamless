# Sessions, memory & recall

> The eight tools an agent uses most - open a session, write and read memory, and search the store.

These eight tools are the agent loop. In a repo mapped to a project, most of them
need no `project` argument at all: `session_start` binds the connection, and
everything after it inherits that scope.

## The shape of a session

`session_start` returns the project briefing and binds the session to the
connection. `session_end` persists findings for the next agent's briefing. Both
are optional in the sense that Claude Code's hooks already open an *ambient*
session per agent - calling `session_start` explicitly adopts it and gets you the
full briefing rather than the short injected one.

If `tasks_update` ever fails claiming a task is held by *your own* session id,
the connection binding was lost. Re-run `session_start` with the same name to
rebind.

## Update, append, supersede, or delete?

Four ways to change memory, and picking the wrong one is how a store rots:

| You want to | Use | What happens |
|---|---|---|
| Correct or extend what a memory says | `memory_write` with the same `name` | Updated in place; the id is stable |
| Add to the end without rereading it | `memory_append` | Body grows; nothing else changes |
| Replace a **different**, now-outdated memory | `memory_write` with `supersedes` | The old one is marked invalid, leaves every index, and stays readable with a pointer to its replacement |
| Remove something written by mistake | `memory_delete` | Gone |

The distinction that matters is **supersede vs. delete**. Superseding is how the
store stays honest about its own history: the old memory leaves the briefing and
recall, but an agent that follows an old reference still finds it, marked
invalid, pointing at what replaced it. Delete is for mistakes - things that were
never true - not for things that stopped being true.

A `supersedes` that fails is reported rather than swallowed: the new memory is
still written and kept, and the call returns an error naming it. The target is
then still active, so re-run the supersede.

## Scope

`memory_write` **fails closed**: with no session and no explicit `project`, it is
rejected as ambiguous rather than silently landing in the global scope. Pass
`project: global` to write a deliberately cross-project memory.

## Recall is the only search tool

There is one search entry point. `recall` fuses FTS5 keyword matching and vector
similarity with reciprocal rank fusion, nudges the fused order by favorite and
[utility](https://thereisnospoon.org/docs/concepts/recall/#the-utility-nudge) (both bounded), scoped to the
current project plus global items, and packs results into a token budget. A call
that finds nothing is recorded as a miss - recurring misses become the
gardener's [memory-wanted proposals](https://thereisnospoon.org/docs/concepts/gardener/#what-it-looks-for).

The optional `kind` filter restricts hits to memories of one frontmatter kind -
the mechanism behind briefing hints like `recall kind=convention`. It implies
memories-only: combining it with `scope=notes` is rejected as contradictory
rather than returning a misleading empty result, and a kind-filtered miss still
counts as memory-wanted demand.

It degrades rather than fails: if the embedding provider is unreachable, recall
falls back to keyword-only results instead of erroring. A local misconfiguration
is surfaced instead of hidden - the two cases are deliberately not treated alike.

## Results and failures

| Call | Success result | Failure that matters |
|---|---|---|
| `session_start` | `session_id`, `name`, resolved `project`, explanatory `scope`, and `briefing`; resumed/adopted sessions also say `resumed: true` | Briefing assembly degrades to an empty string and logs; creating or binding the session itself still fails loudly |
| `memory_write` | Stable `id`, canonical `name`, resolved `project`, `updated`, optional `similar`, and optional `superseded` | An occupied tombstone path is an error; if the new memory lands but supersession fails, the tool errors while naming the kept replacement and the still-active target |
| `recall` | `hits`, possibly empty | Remote embedder failures degrade to lexical-only; local request/config construction errors surface |
| `session_end` | Confirmation of the close - `session_id`, `claims_released`, `mishaps_recorded`; findings persist for the next briefing | A missing/ambiguous session is an error rather than a fabricated successful close |

## session_start {#session_start}

Begin or resume an agent work session and bind it to this connection. Returns the project briefing. Later memory/recall/notes calls inherit this session's project scope, so you rarely pass project again.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `cwd` | string | no | Absolute working directory; auto-mapped to a project from the repo root on a repo's first session (no setup step -- `seamlessd map-repo` only overrides the derived slug) |
| `model` | string | no | Model id powering this agent, exactly as the provider names it (e.g. claude-fable-5, gpt-5.5). Stamped onto memories/notes this session writes; hooks keep it current for Claude Code/Codex sessions, so pass it mainly from other clients |
| `name` | string | no | Optional stable session name; reusing a name resumes that session |
| `source` | string | no | what began this session (default startup). One of: `startup`, `resume`, `clear`, `compact`, `explicit`. |

## session_update {#session_update}

Record interim progress on the current session (working findings so far). Uses the bound session unless you pass one.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `findings` | string | **yes** | Working findings / progress note so far |
| `session` | string | no | Optional session name; defaults to the bound session |
| `session_id` | string | no | Optional session ULID; takes precedence over session and the bound session |

## session_end {#session_end}

Complete the current session, persisting its findings for future briefings. Uses the bound session unless you pass one.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `findings` | string | **yes** | Final findings: what was learned, decided, or left open. Prefer a tight summary (briefings show a short preview), but long findings are stored in full -- they are not rejected. |
| `mishaps` | array | no | Self-report mishaps this session caused: an action a warning or convention said not to take, live state touched by mistake, a command that hit the wrong target. Pass an array with one short entry per incident; omit when none happened. When a mishap violated a stored memory, name that memory by its exact slug in the entry (e.g. "violated chroma-boot-race by ...") -- the report is then linked to it. Recorded for recurrence review, not blame -- report them even when fully recovered. |
| `session` | string | no | Optional session name; defaults to the bound session |
| `session_id` | string | no | Optional session ULID; takes precedence over session and the bound session |

## memory_write {#memory_write}

Create or update a durable memory. Writing an existing name updates it in place (its id is stable). On a new name, a semantically similar existing memory is reported as an advisory hint; the write still proceeds. Pass supersedes to replace a DIFFERENT, now-outdated memory: it is marked invalid and leaves every index (briefing, recall) but stays readable with a pointer here. If the supersede step fails the new memory is still written and kept, but the call returns an error naming it -- the target is then still active.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | **yes** | kebab-case identifier, unique within the project |
| `kind` | string | **yes** | memory kind. One of: `constraint`, `convention`, `runbook`, `protocol`, `gotcha`, `decision`, `refuted`, `reference`, `stage`. |
| `description` | string | **yes** | one line, &lt;=150 chars -- the only text shown in indexes |
| `body` | string | **yes** | markdown body (aliases: content, text) |
| `project` | string | no | project slug; defaults to the bound/ambient session's project. An unknown slug CREATES that project -- naming a new one is normal and never an error. Pass project=global ONLY for knowledge that belongs in EVERY project's briefing; it is not a neutral default. With no session and no explicit project the call is rejected as ambiguous. |
| `supersedes` | string | no | name of an existing memory this one replaces; that memory is marked superseded (invalid) and pointed here |

## memory_append {#memory_append}

Append markdown to an existing memory's body. The memory keeps its id. To create a new memory, use memory_write.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | **yes** | memory name |
| `body` | string | **yes** | markdown to append (aliases: content, text) |
| `project` | string | no | project slug; defaults to the bound/ambient session's project, then global. Pass project=global to target a global memory. |

## memory_read {#memory_read}

Read a memory by name within the current project, falling back to a global memory of the same name.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | **yes** | memory name |
| `project` | string | no | project slug; defaults to the bound session's project |

## memory_delete {#memory_delete}

Delete a memory by name (removes the file and its index).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | **yes** | memory name |
| `project` | string | no | project slug; defaults to the bound session's project |

## recall {#recall}

Search memories and notes by meaning and keyword (fused), scoped to the current project plus global items. This is the single search entry point.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `query` | string | **yes** | what you are looking for |
| `kind` | string | no | only memories of this frontmatter kind (e.g. convention); implies memories-only, so scope=notes is rejected. One of: `constraint`, `convention`, `runbook`, `protocol`, `gotcha`, `decision`, `refuted`, `reference`, `stage`. |
| `limit` | number | no | maximum results (default 10) |
| `project` | string | no | project slug; defaults to the bound session's project |
| `scope` | string | no | what to search (default all). One of: `all`, `memories`, `notes`. |
