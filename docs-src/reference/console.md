---
title: Console
description: The read-mostly observability UI at /console — the complete list of what it can change, how sign-in works, and what each page shows.
---

The console is the owner's window onto a system whose actual clients are agents.
It is `html/template` plus vanilla JS plus SSE, served by `internal/console` from
the same binary as everything else — no node, no npm, no React, no build step. It
is not how Seamless is driven; it is how you watch it.

## What the console can change

The console is **read-mostly**. That is a design claim, so here is the whole list
— every write it is capable of, taken from the `POST` routes in
`internal/console/console.go`. There are no others.

| Action | Route | Effect |
|---|---|---|
| Archive a memory | `POST /console/memories/{id}/archive` | Routes through `lifecycle.Archive`: the memory is stamped invalid and leaves every index, its file stays on disk with a tombstone. |
| Force-release a task claim | `POST /console/tasks/{id}/release` | The owner override: releases a claimed task's lock regardless of who holds it, reopening it for any agent. Not reachable from the agent MCP tools. |
| Approve a captured plan | `POST /console/plans/{slug}/approve` | The escape hatch for a Claude Code approval whose `PostToolUse` never fired: flips the `cc-plan` note to `plan-status:approved` and creates the tracking task, exactly as the hook would have. Only applies to a CC capture. |
| Ask the gardener for proposals | `POST /console/gardener/request` | Interprets a natural-language maintenance request into **pending proposals**. It never mutates a memory. |
| Plan a project split | `POST /console/gardener/split` | Interprets a split request into a plan batch of **pending proposals** (one split setup plus one reproject per memory). Also never mutates a memory. |
| Apply one proposal | `POST /console/gardener/{id}/apply` | Carries out that proposal's effect. |
| Dismiss a proposal | `POST /console/gardener/{id}/dismiss` | Drops it without acting. |
| Retarget a reproject proposal | `POST /console/gardener/{id}/retarget` | Rewrites a **pending** reproject's destination project before it is applied. Reproject proposals only. |
| Apply a whole plan batch | `POST /console/gardener/plan/{slug}/apply` | Applies every pending proposal in a plan, split setup first so the child projects exist before the memories move. Best-effort: it applies what it can, reports how many landed, and leaves the rest pending. |
| Save briefing settings | `POST /console/settings/briefing` | Writes the briefing knobs as a runtime **override row** in the DB. It never writes the config file. |
| Reset briefing settings | `POST /console/settings/briefing/reset` | Clears the override row, reverting to the file/env configuration. |
| Sign in / sign out | `POST /console/login`, `POST /console/logout` | Sets or clears the console cookie. Touches no data. |

Read the shape of that list. There is no "create memory", no "edit note", no
"delete", no "add task", no "start session". The two direct writes to knowledge
state are **archive a memory** and **approve a captured plan**; the rest either
manage gardener proposals — which are themselves proposals, reviewed before they
do anything — or free a lock, or set a display-time knob.

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
  is a SHA-256 hash of the key, not the key — so the raw credential never sits in
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
key to `/console/login`, which sets the cookie and 303s into the console — so you
land on an authenticated page with nothing to paste. It refuses to run if
`mcp.api_key` is empty or the server is not answering `/healthz`.

`make console-chrome` exists for agents: they drive Chrome, so this hands the auth
cookie to the browser they can actually see. (`--browser` is macOS-only.)

## Three ways to render a page

Every route answers in the shape the caller asked for:

- **HTML** by default — the full page, layout and all.
- **JSON** when the caller sets `?format=json` or an `Accept` header that wants
  JSON and not HTML. This is how `seam` reads the console's data.
- **An HTML fragment** for entity details when the caller passes `?peek=1` — the
  detail pane loads it without a page navigation.

Memory, note, task, event, and plan pages compose their full page from the same
`detail-body` block their peek fragment renders, so the two cannot drift. Session
and project deliberately do not: their fragments are compact summaries of much
richer bespoke pages.

Strictly-validated query params (`?sort`, `?scope`, `?tab`, `?w`) return a 400
naming the bad param and listing the valid values, rather than silently falling
back to a default — so an agent driving the console by URL sees the fix.

`GET /console/events` is the SSE stream: every recorded event as one JSON `data:`
frame, with a ping every 25 seconds. `?feed=interactions` opts into the richer
transport-level rows the Interactions screen consumes.

## Overview

`/console/`

The landing page and the health check. It carries:

- **Counts** — active memories (broken down by kind), notes, sessions, and tasks
  by status.
- **Retrieval health** over a selectable window (`?w=24h|7d|30d|all`): injection
  volume, a trend chart, the **reach rate** (distinct active memories that
  actually surfaced, over all active memories), sessions reached, and the
  most-injected memories.
- **Coverage** — the share of in-window sessions that retained anything, with a
  per-channel breakdown (findings, memories, notes, trials) and a windowed trend.
  The channels overlap, so the shares need not sum to 100%.
- **Projects at a glance** — the top projects by recent activity, drawn from the
  same batched query the Projects board uses, so a row here reconciles with a row
  there exactly.
- **Recent activity** — the last twelve events, each linking to its detail page.

Live sessions are counted TTL-aware (active *and* heartbeated within the idle
threshold), so the headline matches the Sessions screen rather than the raw
`active` count that an idle session inflates until the reaper runs.

## Interactions

`/console/interactions`

The live feed of what agents are actually doing: MCP tool calls, hook injections,
recall-miss prompts, session lifecycle, and the plan-mode capture stream. Rows
carry the full request and response bodies — the tool's arguments and result, or
the exact injected text — so you can read what an agent asked for and what it got
without a second fetch.

The feed streams over SSE and pages backwards through history. A recall via the
MCP tool records both a `retrieval.injected` and a `tool.call`; the injected twin
is dropped here because the tool call carries the same content plus its
arguments. Session lifecycle twins are kept on purpose, as feed markers.

## Search

`/console/search`

One query across every entity the console can link to. Memories and notes come
through the same fused FTS + semantic retrieval that [recall](/concepts/recall/)
uses, with snippets; tasks, plans, projects, and sessions have no FTS mirror and
match by `LIKE`.

The command palette (⌘K, available on every page) fetches this same route with
`?format=json&fast=1`, which drops the semantic leg — a query per keystroke must
never cost a remote embedding round-trip.

Coverage is deliberately partial. Trials are excluded because the console has no
trial surface for a hit to link to; events are excluded because the telemetry
stream has its own Interactions surface and would flood results.

## Projects

`/console/projects`

The board: one row per project, with live sessions, total sessions, open and
blocked tasks, memory count, inherited memories, reach rate, and last activity.
Grouped by family (`?group=family|flat`) and sortable (`?sort=recent|coverage|name`).
The global (`""`) scope appears as a row but is not a project and has no detail
link.

Selecting a project opens the **project workspace** — a seven-tab page over that
project alone:

| Tab | Shows |
|---|---|
| Overview | The project's metrics, memory kinds, injection trend, recent events. |
| Plans & tasks | Per-plan step timelines with each step's status, claiming session, lease countdown, and blocking dependency; plus the ready queue. |
| Sessions | The project's sessions, with the tasks each currently holds. |
| Memories | The project's memories with a lineage cell — provenance session, or a supersession pointer — plus the memories it *inherits* from a parent that a strict per-slug count excludes. |
| Notes | The project's notes. |
| Interactions | The project-scoped slice of the feed. |
| Relations | The project's plan → step → session → memory tree. |

A retired project still renders, with its banner — kept for provenance. Only an
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

The browser, grouped by project (global first) and then by kind in canonical
order, with active and inactive memories separated. Each row shows the
description — the only text an index ever shows — plus injection and read counts,
last-injected time, and a `vscode://` link straight to the file. Sortable by name,
recency, or reach; filterable by a substring of name, description, kind, or tag.

Inactive memories carry their status (`superseded` or `archived`) and, when
superseded, a link to what replaced them.

A memory's page renders the body (through the markdown layer, with raw HTML
disabled and a sanitizer on the output), its metadata — kind, project, tags,
timestamps, the session that produced it — its reach counts, and its supersession
neighbors in **both** directions: what replaced it, and what it replaced.
**Archive** is the one action here.

## Notes

`/console/notes`, `/console/notes/{id}`

The same browser shape for notes: grouped by project with the inbox (`""`) first,
sortable by recency or title, filterable by title, description, or tag. A note's
page renders the body.

## Tasks

`/console/tasks`, `/console/tasks/{id}`

Four buckets: **ready** (no unfinished blocker), **in progress**, **blocked** —
each blocked row naming the specific tasks blocking it — and **closed** (done and
dropped merged, newest first, capped at 25 with a count of the rest).

A task's page carries its detail; **force-release** is the action, and it is the
owner override — it takes the lock from whoever holds it.

## Plans

`/console/plans`, `/console/plans/{slug}`

Both kinds of plan in one list, grouped by phase (in progress, ready, done):

- **captures** — Claude Code plan-mode captures (`cc-plan` notes), with their
  lifecycle status, iteration count, and cached subagent runs.
- **composed** — plain [plans-as-composition](/concepts/tasks-and-plans/) plans (a
  note tagged `plan:<slug>` plus its tasks), which have none of the capture-only
  columns.

A capture owns its slug; composed plans fill only the rest. A plan's page shows
the rendered plan body, the notes attached to the composition (supporting notes
and agent caches), and the step tasks. **Approve** appears here, for captures
only.

## Relations

`/console/relations`

The dependency spine a flat list cannot show: a plan expands into its steps, each
step's claiming session, and the memories that session left behind. `?scope=all`
renders every project's tree on one page; `?scope=project&project=<slug>` narrows
to one.

Reachable from the Projects board.

## Retrieval

`/console/retrieval`

The reach funnel in detail, over a selectable window: injections, memories
surfaced, active memories, reach rate, sessions reached, reach broken down by kind
and by project, the injection trend, and the most-injected memories.

Plus **stale memories** — active memories not updated, injected, or read in 90
days, mirroring the gardener's default staleness horizon. Unlike everything else
on the page, this list is window-independent.

Reachable from the Overview's retrieval-health card.

## Gardener

`/console/gardener`

The review queue. Each pending proposal renders as a card showing exactly what it
would do — the memory to archive and why, the pair to merge with their similarity
score, the digest or consolidated memory with its body rendered, the reproject's
source and destination, the split's children and shared parent.

Split batches are grouped by plan and reviewed together, setup card first, with an
apply-the-whole-plan action.

The actions are **apply**, **dismiss**, **retarget** (reproject cards only), and
**apply plan**. Above them sit the two request boxes — ask in words, or split a
project — and both only ever produce more proposals for this same queue. A
recognized split typed into the general request box routes you to the split box
rather than creating loose proposals, because splitting needs a structured source
project.

See [The gardener](/concepts/gardener/) for what each proposal type means.

## Settings

`/console/settings`

A read-only view of the running configuration — data dir, budgets, gardener
settings, the registered projects, the repo→project map, and the project families
— with **one editable block**: briefing injection.

Saving writes a runtime override row in the DB. It layers over the file/env values
and wins until reset, and it applies from the next session start — no daemon
restart. It never touches your config file, so `seamless.yaml` stays the thing you
wrote. **Reset** clears the override and reverts to file/env.

The form validates: a non-numeric knob or a value that fails `Briefing.Validate()`
comes back as an error flash, not a silently-dropped save.

See [Configuration](/reference/configuration/) for what each knob does, and
[Sessions & briefings](/concepts/sessions/) for what they tune.

## Event detail

`/console/events/{id}`

What a Recent-activity or timeline row links to: one event-log entry in full — the
verbatim injected content, the memories it surfaced resolved to their live index
entries (or flagged missing if the id no longer resolves), the remaining payload
fields, and the raw JSON.

## Errors

A bad or stale URL renders a styled, layout-wrapped error page with a way back,
rather than dropping you on a bare `404 page not found`. A 404 names the missing
entity and a 400 names the bad parameter and its valid values. A 500 stays
generic in the browser — the detail is in the log, not the response. Fragment
fetches (`?peek=1`) get a fragment-shaped error, since a full page injected into
the detail pane would nest the whole console inside itself.
