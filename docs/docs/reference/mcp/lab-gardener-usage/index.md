# Lab, gardener & usage

> Research trials, the propose-only gardener, the usage summary, and favorites - the nine tools for keeping the store honest.

## The research lab

A lab is a shared workspace for a systematic investigation: a hypothesis, a
series of trials, and what each one actually did. `lab_open` binds a lab to the
connection, so `trial_record` inherits it the way memory inherits a session's
project. `trial_query` reads the trials back.

The point is **parallel agents collaborating on one investigation**. Several
agents chasing the same bug can open the same lab, record what they tried, and
see each other's dead ends instead of re-walking them. When the investigation
resolves, distill the finding into a memory - the lab is the working record, not
the conclusion.

## The gardener proposes; it never acts

The gardener runs periodically over the store looking for drift. Every pass it
makes ends in a **proposal row for you to review** - it never rewrites your
knowledge behind your back. That is the whole contract, and it is why the tool
surface has both `gardener_proposals` (read them) and `gardener_apply` (act on
one) instead of a single "tidy up" button.

Its passes:

| Pass | What it looks for |
|---|---|
| dedup | Two memories that say the same thing, above `gardener.dedup_threshold` similarity. Proposes a **merge**: one is kept, the other superseded and pointed at it. |
| staleness | Memories untouched for `gardener.staleness_days`. Proposes an **archive**: marked invalid, still readable. |
| stale-stage | A `stage` memory whose gate is done, missing, or unparseable, unchanged for `gardener.stale_stage_days`. Proposes an **archive**. |
| dead-weight | A memory briefings keep injecting with zero recall hits, prompt matches, or reads. Proposes an **archive**. |
| digest | Enough recent activity to be worth a summary over `gardener.digest_days`. Proposes a **digest** note. |
| stale-plan | Plans idle for `gardener.stale_plan_days` with steps still open. |
| memory-wanted | The same recall query missing across sessions. Proposes a **memory_wanted**: applying opens a task to write the knowledge agents keep searching for. |
| tool-error | The same normalized tool or hook error recurring (tool errors across 2+ sessions). Proposes a **tool_error**: applying opens an investigation task with the observed evidence. |

Constraints and pinned stages are never age-filtered or staleness-archived. A
constraint does not become less true by sitting still.

`gardener_request` takes a request in natural language ("fold the two console
theme memories together") and turns it into the same reviewable proposals. It can
also **reproject** a memory filed under the wrong project - moving it to a
different project that already exists.

Its `project` argument scopes the memories it may reference: a project slug (that
project plus globals), `global` for globals only, or `all` for every project on
the machine. Omit it and the scope resolves the way every other read does - from
the session - rather than quietly widening to everything. A slug that is not a
project is an error, not an empty result.

## Splits are a different thing

Moving a memory into a project that does not exist yet is not a reproject - it is
a split, and `gardener_split` plans it. Splitting one project into new children
creates those projects and a shared parent, so it is planned as a unit rather
than as a pile of individual moves.

## usage_summary

`usage_summary` reports what the store actually contains and what retrieval has
been doing: memory counts, retrieval statistics including the highest-utility
memories by decayed demand, events by kind. It answers "is
this thing working, and on how much?" - the same question the console's Overview
answers, for an agent that cannot open a browser.

## favorite_set

`favorite_set` stars or unstars an item - a memory, note, project, plan, task,
session, or trial. A starred memory is pinned into every session briefing as a
`FAVORITE:` line and gets a mild recall rank boost; in the console, favorites
carry a star, sort first, and can be filtered. For memories and notes the flag
lives in the file's frontmatter (`favorite: true`), so it survives an index
rebuild and can be hand-edited. A plan's favorite lives on its primary note, so
a task-only plan (no note) cannot be starred. Starring never bumps an item's
updated time - it is metadata, not authorship.

## Applying is an explicit boundary

`gardener_apply` accepts only `apply` or `dismiss`. Dismissal closes the proposal
without changing its target. Apply performs the proposal's typed action and
returns that real outcome; it never substitutes a plausible dummy when the
target disappeared or a file/store mutation failed. Memory-changing actions
route through the lifecycle/files services, so supersession, atomic Markdown
writes, and occupied-path protections remain the same as direct tools.

## lab_open {#lab_open}

Open a research lab and get its recent trial history for context. Binds the lab to this connection so later trial_record calls inherit it. A lab is just a label; opening a new one creates it implicitly on first trial_record.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `lab` | string | **yes** | lab name (a stable label for a line of investigation) |
| `goal` | string | no | optional note on what this lab is investigating |

## trial_record {#trial_record}

Record one experiment in a research lab: what changed, expected vs actual, outcome, and optional structured metrics for later querying. Inherits the lab from lab_open unless you pass one.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `title` | string | **yes** | short trial title |
| `actual` | string | no | observed result |
| `changes` | string | no | what was changed for this trial |
| `expected` | string | no | expected result |
| `lab` | string | no | lab name; defaults to the lab opened on this connection |
| `metrics` | object | no | optional object of structured metrics, e.g. {"hz":497,"err_pct":0.2} (a JSON-object string is also accepted) |
| `outcome` | string | no | pass\|fail\|partial\|inconclusive (free-form) |
| `project` | string | no | project slug; defaults to the bound/ambient session's project. An unknown slug CREATES that project -- naming a new one is normal and never an error. Pass project=global ONLY for knowledge that belongs in EVERY project's briefing; it is not a neutral default. With no session and no explicit project the call is rejected as ambiguous. |

## trial_query {#trial_query}

Query recorded trials, filtered by lab, outcome, and/or an exact-match metrics filter (native structured query over the metrics recorded by trial_record).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `lab` | string | no | lab name; defaults to the lab opened on this connection |
| `limit` | number | no | max results (default 20) |
| `metrics_filter` | object | no | optional object; trials whose metrics equal every given key match, e.g. {"hz":497} (a JSON-object string is also accepted) |
| `outcome` | string | no | filter by outcome (e.g. fail) |

## gardener_proposals {#gardener_proposals}

List pending gardener proposals (merge/consolidate duplicate memories, archive stale memories, write a monthly session digest, reproject a memory to another project, set up a project split, abandon a never-approved captured plan, write a memory agents keep searching for in vain, or fix an error agents keep hitting). Review, then apply or dismiss each with gardener_apply. Read-only.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `kind` | string | no | filter by proposal kind (default: all pending). One of: `merge`, `archive`, `digest`, `consolidate`, `reproject`, `split`, `abandon_plan`, `memory_wanted`, `tool_error`. |

## gardener_request {#gardener_request}

The natural-language entry point for REORGANIZING memory. Describe the change in plain language and it returns reviewable pending proposals -- fold duplicates together ("these two memories are duplicates -- keep the newer"), retire stale memories ("archive anything about the old port 8080"), synthesize several into one ("combine the three auth-flow notes"), or move a mis-filed memory to another EXISTING project ("the iOS DFU memory belongs in arctop-ios"). Use this whenever the user describes how they want their knowledge organized; if the intended change is ambiguous, ask them a clarifying question first. It NEVER mutates memories: it only creates pending proposals -- review with gardener_proposals, resolve with gardener_apply. If the request is to split one project into NEW child projects, it recognizes that and returns guidance (splitSource) pointing you at gardener_split instead. Needs an LLM chat client.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `request` | string | **yes** | the reorganization request in plain language |
| `project` | string | no | scope candidate memories: a project slug (its memories + globals), "global" for globals only, or "all" for every project on the machine. Omit to use the session's project. |

## gardener_split {#gardener_split}

Plan a project SPLIT: divide one existing project into two or more NEW child projects, keeping cross-platform memories in a shared parent (e.g. split arctop-app into arctop-ios + arctop-android with shared arctop-mobile-apps). Use this when the user wants to break one project into several -- gardener_request points you here (via splitSource) when it detects that intent. It NEVER creates a project or moves a memory: it only creates reviewable pending proposals -- one 'split' setup proposal plus one 'reproject' per memory, all under plan 'split-&lt;source&gt;'. Review with gardener_proposals, then apply each with gardener_apply (or retarget a memory first in the console). Needs an LLM chat client and a known source project slug (see project_list).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `source` | string | **yes** | the project slug to split (its own memories are classified into the children/shared parent) |
| `instruction` | string | no | optional guidance: which children, what stays shared |

## gardener_apply {#gardener_apply}

Resolve a gardener proposal. action=apply carries out the effect (archive -&gt; retire the memory; merge -&gt; supersede the older by the newer; consolidate -&gt; write a unified memory superseding its sources; digest -&gt; save the summary as a note; reproject -&gt; move the memory to another project; split -&gt; create the child/shared projects, link the family, parent the children, retire the source; memory_wanted -&gt; open a task to write the missing memory; tool_error -&gt; open a task to fix the recurring error); action=dismiss discards it. A dismissed proposal is never re-raised.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `id` | string | **yes** | proposal id (ULID) |
| `action` | string | no | apply (default) or dismiss. One of: `apply`, `dismiss`. |

## usage_summary {#usage_summary}

Report a roll-up of activity: memory/note/session/task counts, retrieval totals with the most-injected memories, pending gardener proposals, and events by kind. Read-only.

Takes no parameters.

## favorite_set {#favorite_set}

Star or unstar an item. Favorites sort first in the console, are pinned into session briefings (memories), and get a mild recall rank boost. For memories and notes the flag is stored in the file's frontmatter; starring never bumps an item's updated time.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `kind` | string | **yes** | what kind of item to star. One of: `memory`, `note`, `project`, `plan`, `task`, `session`, `trial`. |
| `id` | string | **yes** | the item's identifier: memory name, note id (or slug), project slug, plan slug, task id, session id (or name), trial id |
| `favorite` | boolean | **yes** | true to star, false to unstar |
| `project` | string | no | project scope for memory-name/note-slug resolution; defaults to the bound session's project |
