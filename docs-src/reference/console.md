---
title: Console
description: The read-mostly observability UI at /console - the complete list of what it can change, how sign-in works, and what each page shows.
---

The console is the owner's window onto a system whose actual clients are agents.
It is `html/template` plus vanilla JS plus SSE, served by `internal/console` from
the same binary as everything else - no node, no npm, no React, no build step. It
is not how Seamless is driven; it is how you watch it.

## What the console can change

The console is **read-mostly**. That is a design claim, so here is the whole list
- every write it is capable of, taken from the `POST` routes in
`internal/console/console.go`. There are no others.

| Action | Route | Effect |
|---|---|---|
| Archive a memory | `POST /console/memories/{id}/archive` | Routes through `lifecycle.Archive`: the memory is stamped invalid and leaves every index, its file stays on disk with a tombstone. |
| Force-release a task claim | `POST /console/tasks/{id}/release` | The owner override: releases a claimed task's lock regardless of who holds it, reopening it for any agent. Not reachable from the agent MCP tools. |
| Approve a captured plan | `POST /console/plans/{slug}/approve` | The escape hatch for a Claude Code approval whose `PostToolUse` never fired: flips the `cc-plan` note to `plan-status:approved` and creates the tracking task, exactly as the hook would have. Only applies to a CC capture. |
| Star / unstar an entity | `POST /console/favorites/{kind}/{id}` | Toggles the same starred flag the `favorite_set` MCP tool sets: a starred memory pins into briefings and gets a post-fusion recall boost. For memories and notes the star lives in frontmatter; it never bumps `updated`. |
| Ask the gardener for proposals | `POST /console/gardener/request` | Interprets a natural-language maintenance request into **pending proposals**. It never mutates a memory. |
| Plan a project split | `POST /console/gardener/split` | Interprets a split request into a plan batch of **pending proposals** (one split setup plus one reproject per memory). Also never mutates a memory. |
| Apply one proposal | `POST /console/gardener/{id}/apply` | Carries out that proposal's effect. |
| Dismiss a proposal | `POST /console/gardener/{id}/dismiss` | Drops it without acting. |
| Retarget a reproject proposal | `POST /console/gardener/{id}/retarget` | Rewrites a **pending** reproject's destination project before it is applied. Reproject proposals only. |
| Apply a whole plan batch | `POST /console/gardener/plan/{slug}/apply` | Applies every pending proposal in a plan, split setup first so the child projects exist before the memories move. Best-effort: it applies what it can, reports how many landed, and leaves the rest pending. |
| Save briefing settings | `POST /console/settings/briefing` | Writes the briefing knobs as a runtime **override row** in the DB. It never writes the config file. |
| Reset briefing settings | `POST /console/settings/briefing/reset` | Clears the override row, reverting to the file/env configuration. |
| Save a project family | `POST /console/settings/families/save` | Creates a family or replaces one family's name and member set - the same `project_families` setting `seamlessd family` manages. Members come from a closed picker of registered projects, so a typo cannot create an inert member. |
| Delete a project family | `POST /console/settings/families/delete` | Removes the whole family. Its projects lose the sibling-findings channel; nothing else about them changes. |
| Sign in / sign out | `POST /console/login`, `POST /console/logout` | Sets or clears the console cookie. Touches no data. |

Read the shape of that list. There is no "create memory", no "edit note", no
"delete", no "add task", no "start session". The direct writes to knowledge
state are **archive a memory**, **approve a captured plan**, and the **star**
flag; the rest either manage gardener proposals - which are themselves
proposals, reviewed before they do anything - or free a lock, or set a
configuration knob (briefing overrides, project families) that shapes future
briefings without touching any memory's content.

This is deliberate, and it is the same principle as
[the gardener's](/concepts/gardener/) propose-only contract. The store is written
by agents doing work, with provenance attached. A console that could quietly edit
a memory would produce knowledge that came from nowhere, attributable to no
session, explaining nothing.

Every write action redirects back with a flash message (`?notice=` for success,
`?error=` for failure) rather than rendering a result page in place, so a reload
never repeats the action.

## Signing in

There is one credential in the whole system: the static bearer key
(`mcp.api_key`). It guards `/api/mcp`, the hook endpoints, and the console alike.

Two ways to present it:

- **A browser** trades the key for a cookie at `/console/login`. The cookie value
  is a SHA-256 hash of the key, not the key - so the raw credential never sits in
  the browser's cookie jar. It is `HttpOnly`, `SameSite=Lax`, and scoped to
  `/console`.
- **The `seam` CLI** sends the key as a bearer token on the `Authorization`
  header, and asks for JSON.

Unauthenticated browsers are redirected to the login page with a `?next=` that is
validated against an open-redirect (an off-site or absolute candidate becomes
`/console/`). Unauthenticated JSON callers get a 401.

Public routes are the login page and the static assets (`console.css`,
`interactions.js`, `search.js`, `favicon.svg`). Everything else requires the key.

### `make console`

```bash
make console          # open in the default browser, already signed in
make console-chrome   # same, but force Google Chrome
```

This builds, then runs `seamlessd console-open`, which renders a one-shot
self-submitting login page to a `0600` temp file and opens it. The page POSTs the
key to `/console/login`, which sets the cookie and 303s into the console - so you
land on an authenticated page with nothing to paste. It refuses to run if
`mcp.api_key` is empty or the server is not answering `/healthz`.

`make console-chrome` exists for agents: they drive Chrome, so this hands the auth
cookie to the browser they can actually see. (`--browser` is macOS-only.)

## Three ways to render a page

Every route answers in the shape the caller asked for:

- **HTML** by default - the full page, layout and all.
- **JSON** when the caller sets `?format=json` or an `Accept` header that wants
  JSON and not HTML. This is how `seam` reads the console's data.
- **An HTML fragment** for entity details when the caller passes `?peek=1` - the
  detail pane loads it without a page navigation - or `?reader=1`, the richer
  reader fragment the library screens (memories, notes, tasks, plans, labs,
  trials) swap in place.

The event page composes its full page from the same `detail-body` block its
peek fragment renders, so the two cannot drift. Session and project
deliberately do not: their fragments are compact summaries of much richer
bespoke pages. Memory, note, task, plan, lab, and trial detail URLs render
their library screen with that entity open in the reader.

Strictly-validated query params (`?sort`, `?scope`, `?tab`, `?w`) return a 400
naming the bad param and listing the valid values, rather than silently falling
back to a default - so an agent driving the console by URL sees the fix.

`GET /console/events` is the SSE stream: every recorded event as one JSON `data:`
frame, with a ping every 25 seconds. `?feed=interactions` opts into the richer
transport-level rows the Interactions screen consumes.

## Overview

`/console/`

The landing page and the health check. It carries:

- **Counts** - active memories (broken down by kind), notes, sessions, and tasks
  by status.
- **Retrieval health** over a selectable window (`?w=24h|7d|30d|all`): injection
  volume, a trend chart, the **reach rate** (distinct active memories that
  actually surfaced, over all active memories), sessions reached, and the
  most-injected memories.
- **Coverage** - the share of in-window sessions that retained anything, with a
  per-channel breakdown (findings, memories, notes, trials) and a windowed trend.
  The channels overlap, so the shares need not sum to 100%.
- **Projects at a glance** - the top projects by recent activity, drawn from the
  same batched query the Projects board uses, so a row here reconciles with a row
  there exactly.
- **Agent-reported mishaps** - recent incidents agents explicitly supplied to
  `session_end`, attributed through the reporting session's harness and model.
  Warning tones appear only when reports exist; an empty rail is a positive
  "No mishaps reported" state.
- **Recent activity** - the last twelve events, each linking to its detail page.

Live sessions are counted TTL-aware (active *and* heartbeated within the idle
threshold), so the headline matches the Sessions screen rather than the raw
`active` count that an idle session inflates until the reaper runs.

## Interactions

`/console/interactions`

The clean live feed of what agents are actually doing: MCP tool calls, hook
injections, recall-miss prompts, session lifecycle, and the plan-mode capture
stream. A fresh page starts empty and listens from that moment forward; merely
visiting never restores old rows.

History is explicit and additive. Choose a recent window and select **Add** when
earlier context is useful; live rows stay in place, and **Load older events**
paginates only inside that chosen window. Filters operate over the rows already
in memory, by event category and session lane. Pausing buffers new arrivals
rather than discarding them.

Each compact row expands just enough to expose its request/result or injected
text. Selecting the inspector keeps that context beside the stream and links to
the full event page. A recall via the MCP tool records both a
`retrieval.injected` and a `tool.call`; the injected twin is dropped here because
the tool call carries the same content plus its arguments. Session lifecycle
twins are kept on purpose, as feed markers.

## Search

`/console/search`

One query across every entity the console can link to. Memories and notes come
through the same fused FTS + semantic retrieval that [recall](/concepts/recall/)
uses, with snippets; tasks, plans, trials, projects, and sessions have no FTS
mirror and match by `LIKE`.

The command palette (⌘K, available on every page) fetches this same route with
`?format=json&fast=1`, which drops the semantic leg - a query per keystroke must
never cost a remote embedding round-trip.

The semantic leg is nearest-neighbor: there is always a "nearest" memory,
however far, so a semantic-only hit must clear `search.semantic_floor` (cosine
similarity, default 0.3) to appear - without the floor any query, including
nonsense, would fill the page to its limit. A hit the keyword leg also matched
is exempt. Every hit the semantic leg found shows its similarity as a
percentage, so you can see where relevance falls off; keyword-only hits show a
highlighted snippet instead. Agent-facing recall applies no floor - an agent
can judge a weak hit for itself.

Coverage is deliberately partial in one place: events are excluded because the
telemetry stream has its own Interactions surface and would flood results.

## Projects

`/console/projects`

The board: one row per project, with live sessions, total sessions, open and
blocked tasks, memory count, inherited memories, reach rate, and last activity.
Grouped by family (`?group=family|flat`) and sortable (`?sort=recent|coverage|name`).
The global (`""`) scope appears as a row but is not a project and has no detail
link.

Selecting a project opens the **project workspace** - a seven-tab page over that
project alone:

| Tab | Shows |
|---|---|
| Overview | The project's metrics, memory kinds, injection trend, recent events. |
| Plans & tasks | Per-plan step timelines with each step's status, claiming session, lease countdown, and blocking dependency; plus the ready queue. |
| Sessions | The project's sessions, with the tasks each currently holds. |
| Memories | The project's memories with a lineage cell - provenance session, or a supersession pointer - plus the memories it *inherits* from a parent that a strict per-slug count excludes. |
| Notes | The project's notes. |
| Interactions | The project-scoped slice of the feed. |
| Context | The effective SessionStart flow into and out of the project: global and parent memory pools, sibling-family channels, and split lineage. |

A retired project still renders, with its banner - kept for provenance. Only an
unknown slug is a 404.

## Sessions

`/console/sessions`, `/console/sessions/{id}`

The list separates **active** (live: active and heartbeated within the idle TTL)
from **idle** (active but gone quiet past it, awaiting the reaper) from
**completed** and **expired**. Filterable by status, searchable, windowed.

A session's page is the workspace: its findings (rendered), its full event
timeline as interaction rows, per-session counts (tool calls, memory reads and
writes, items injected, and read-after-inject), the tasks it currently claims with
their lease countdowns, and the memories it produced.

## Memories

`/console/memories`, `/console/memories/{id}`

A two-pane library: a rail of memories grouped by project (global first, kinds
in canonical order, each dot colored by kind) beside a full-height reader.
Sortable by name, recency, reach, utility, or starred; filterable by a substring
of name, description, kind, or tag. Inactive memories collapse into an
archived-and-superseded group at the rail's end, each carrying its status and,
when superseded, what replaced it.

The reader renders the body uncapped (through the markdown layer, with raw HTML
disabled and a sanitizer on the output), the metadata - kind, project, tags,
timestamps, the session that produced it - its reach counts, its
[utility score](/concepts/recall/#the-utility-nudge) with the per-signal
demand breakdown behind it, the `vscode://` link straight to the file, and its
supersession neighbors in **both** directions: what replaced it, and what it
replaced. The actions here are
**star** - the flag that pins it into briefings and boosts recall - and
**archive**.

Opening `/console/memories` auto-opens the most recently updated match; a
memory's own URL opens the same screen with it selected. Clicking rail items
swaps the reader in place (real URLs, browser Back works), and `j` / `k` step
through the rail.

## Notes

`/console/notes`, `/console/notes/{id}`

The same library shape for notes: a project-grouped rail (global `""` first),
sortable by recency, title, or starred, filterable by title, description, or
tag. The
reader renders the note as a document - uncapped body in a measured reading
column, description, tags, word count, source URL, and the file path with an
editor link.

## Tasks

`/console/tasks`, `/console/tasks/{id}`

The same library shape, with the rail grouped into four buckets: **ready** (no
unfinished blocker), **in progress**, **blocked**, and **closed** (done and
dropped merged, newest first, capped at 25 with a count of the rest, collapsed
by default). The reader carries the task's body, claim and lease state, and
both dependency directions; **force-release** is the action, and it is the
owner override - it takes the lock from whoever holds it.

## Plans

`/console/plans`, `/console/plans/{slug}`

The same library shape, with the rail grouped by phase (**in progress**,
**ready**, **done**) and scoped by the window selector in the rail's tools
(24h by default). Both kinds of plan share the rail:

- **captures** - Claude Code plan-mode captures (`cc-plan` notes), with their
  lifecycle status, iteration count, and cached subagent runs.
- **composed** - plain [plans-as-composition](/concepts/tasks-and-plans/) plans (a
  note tagged `plan:<slug>` plus its tasks), which have none of the capture-only
  fields.

A capture owns its slug; composed plans fill only the rest. The reader shows
the rendered plan body, the step tasks, and the notes attached to the
composition (supporting notes and agent caches). **Approve** appears here, for
captures only.

## Labs

`/console/labs`, `/console/labs/{name}`

The research-lab surface (the console twin of `lab_open` / `trial_record` /
`trial_query`). A lab is not a stored entity - it is the label its trials carry,
a stable name for one line of investigation - so this screen is an aggregation
over the trials table and there is nothing to write.

The same library shape: a rail of labs, most recently active first, each with
its trial count and pass/fail tallies. The reader shows one lab's whole
identity - outcome tallies (pass, fail, partial, inconclusive, and *other* for
free-form or empty outcomes), the projects and sessions its trials touched,
first and last activity - and its trial history, newest first, each entry
linking into the Trials screen. Long histories cap at 100 with a pointer to the
uncapped, filterable view.

## Trials

`/console/trials`, `/console/trials/{id}`

The flat, filterable view over every recorded trial - the console twin of the
`trial_query` MCP tool. The rail groups trials by lab (a group sits where its
newest trial does) and filters by `?lab=` and `?outcome=`. Outcomes are
free-form by design, so `?outcome=` is an exact-match filter rather than a
validated enum; the seg offers the conventional values (`pass`, `fail`,
`partial`, `inconclusive`).

The reader shows one trial's full record: what changed, **expected vs actual**
side by side (the actual pane tinted by outcome), the structured metrics
`trial_record` captured, and its provenance - lab, project, and the recording
session, each linked. Trial hits also surface in [search](#search) and the
command palette, and a session's page lists the trials it recorded.

## Context

`/console/context`

The briefing topology that the plans board does not show: which knowledge pools
are eligible at SessionStart, which configured edges are currently enabled by
the effective briefing settings, and where project splits moved durable memory.
It covers global memory, one-way parent-memory inheritance, bidirectional sibling
families (findings and the opt-in memory channel), unregistered-scope warnings,
and retired-project split lineage reconstructed from the memory-move event log.

`?scope=all` renders every known project scope, with global memory shown as the
shared source pool; `?scope=project&project=<slug>` focuses the same topology on
one project. The legacy `/console/relations` route permanently redirects here
and preserves its query string.

Reachable from the Projects board.

## Retrieval

`/console/retrieval`

The circulation report: is stored knowledge actually reaching agents, and at
what cost? The hero pairs the **reach ring** (distinct active memories that
surfaced, over all active memories) with the window's volume and cost -
injections, sessions reached, and **estimated tokens injected**. Everything
follows the selectable observation window except where a panel says otherwise.

Five zones below it:

1. **Delivery path** - the funnel as a flow: injections → distinct memories →
   sessions reached, ending in the knowledge-base coverage meter (how many
   active memories are still waiting to surface).
2. **Circulation pattern** - the injection trend chart and the traffic-by-kind
   mix.
3. **Scope coverage** - reach per project scope (global first), each row with
   its own reach rate and injection count.
4. **Knowledge pressure** - the most-injected memories against **quiet
   knowledge**: active memories not updated, injected, or read in 90 days,
   mirroring the gardener's default staleness horizon. Unlike everything else
   on the page, the stale list is all-time, not windowed.
5. **Loop health** - push versus pull: is what briefings push also what agents
   pull? **Demand rate** is the share of briefed memories that were also pulled
   by a query; **waste share** is the share of injected tokens spent on memories
   with no query-gated demand, judged against a fixed trailing 30 days whatever
   the window. Two miss stats sit side by side and measure different paths:
   the **recall-miss rate** is ambient - prompts that matched no memory on the
   [`<seam-recall>` path](/concepts/recall/#the-recall-triad) - while **agent
   search misses** are deliberate `recall` calls that found nothing; recurring
   ones feed the gardener's
   [memory-wanted pass](/concepts/gardener/#what-it-looks-for). **Funnel by
   surface** splits the read-after-inject funnel by injection surface -
   session-start briefings versus subagent-start child injections - each with
   its injections, distinct memories, and the share pulled by a query-gated
   read within the following 24 hours. The zone closes
   with the **dead weight** panel: memories briefings kept injecting without a
   single recall hit, prompt match, or read in 30 days (constraints and stages
   exempt as pinned-by-design) - the evidence behind the gardener's dead-weight
   archive proposals.

Reachable from the Overview's retrieval-health card.

## Gardener

`/console/gardener`

The review queue. Each pending proposal renders as a card showing exactly what it
would do - the memory to archive and why (whether staleness, a dead stage, or
dead weight flagged it), the pair to merge with their similarity score, the
digest or consolidated memory with its body rendered, the reproject's source and
destination, the rekind's from and to kinds, the split's children and shared
parent, and the **knowledge gap**
card with the queries agents kept searching for in vain - applying that one
opens a task; nothing is written until someone writes the memory.

Split batches are grouped by plan and reviewed together, setup card first, with an
apply-the-whole-plan action.

The actions are **apply**, **dismiss**, **retarget** (reproject cards only), and
**apply plan**. Above them sits a single ask-in-words box, and it only ever
produces more proposals for this same queue. A request recognized as a project
split is planned as a split directly - the plan batch appears below like any
other. When the split's source project cannot be matched, an inline follow-up
asks you to pick the project and plans the split from there; nothing is retyped.

See [The gardener](/concepts/gardener/) for what each proposal type means.

## Settings

`/console/settings`

A view of the running configuration - data dir, budgets, gardener settings, the
registered projects, and the repo→project map - with editable blocks for the
semantic index, briefing injection (including utility ranking), and project
families.

**Semantic index & storage**: the embedding pipeline and the SQLite database,
side by side. The embedder card shows the active provider and model - and when
embeddings are off, the exact cause, with distinct copy for the owner off
switch, the no-key lexical fallback, and a config error. The off/auto switch is
a settings row read once at serve start, so the page flags a pending restart
whenever the stored switch disagrees with the running process. Below it, the
stored-vector counts: totals, the not-yet-embedded backlog, and a per-model
table that badges models the running embedder no longer writes as stale -
**re-embed everything** rewrites the corpus with the active model in the
background. The database card shows the file path, size on disk including the
WAL, and schema version.

**Briefing injection**: saving writes a runtime override row in the DB. It
layers over the file/env values and wins until reset, and it applies from the
next session start - no daemon restart. It never touches your config file, so
`seamless.yaml` stays the thing you wrote. **Reset** clears the override and
reverts to file/env. The form validates: a non-numeric knob or a value that
fails `Briefing.Validate()` comes back as an error flash, not a
silently-dropped save.

The form's **utility ranking** group holds the closed-loop knobs -
`utility_weight` (utility's share of the briefing sort key; 0 restores pure
recency) and `utility_mode` (`auto` arms each project as its demand history
matures, `on` everywhere now, `off` never). The same group's **utility
activation by scope** table lists every scope with each readiness gate against
its threshold (demand events, memories touched, history age - met gates turn
green) and, for scopes still building, spells out exactly what remains before
auto arms ("needs 7 more events, 3d more history"). A per-scope **force**
overrides the latch in either direction; when the global mode is `on` or
`off`, the table says the per-scope state is dormant until auto returns. See
[Sessions & briefings](/concepts/sessions/#the-budget-and-what-survives-it) for
how the blended order behaves once active.

**Project families**: create, rename, edit, or delete the named groupings that
[`seamlessd family`](/reference/cli-seamlessd/#seamlessd_family) manages from
the CLI - the same `project_families` setting, so a change on either surface
shows up on the other. Members are chosen from a closed picker of registered
projects; the CLI is the route for pre-registering a slug that does not exist
yet.

See [Configuration](/reference/configuration/) for what each knob does, and
[Sessions & briefings](/concepts/sessions/) for what they tune.

## Event detail

`/console/events/{id}`

What a Recent-activity or timeline row links to: a compact event review workspace
with the event's agent/session attribution, verbatim injected or transport
content, surfaced memories resolved to their live index entries (or flagged
missing), remaining payload fields, and raw JSON. The Interactions inspector
uses the same content model in a side pane; opening the full page adds context
without changing the underlying event.

## Errors

A bad or stale URL renders a styled, layout-wrapped error page with a way back,
rather than dropping you on a bare `404 page not found`. A 404 names the missing
entity and a 400 names the bad parameter and its valid values. A 500 stays
generic in the browser - the detail is in the log, not the response. Fragment
fetches (`?peek=1`) get a fragment-shaped error, since a full page injected into
the detail pane would nest the whole console inside itself.
