---
title: The gardener
description: The background pass that finds duplicates, staleness, and drift — and proposes, because nothing rewrites your knowledge behind your back.
---

Any store that agents write to accumulates cruft: two memories saying the same
thing in different words, a runbook for a system that no longer exists, a plan
abandoned three weeks ago with its steps still open.

The gardener finds those. Then it stops and asks.

## Propose-only is the whole contract

**Every pass the gardener makes ends in a proposal for you to review.** It never
edits, merges, or archives on its own.

This is not timidity. The entire premise of Seamless is that the knowledge is
yours and legible — a store that quietly rewrites itself while you sleep is
exactly the black box the project exists to avoid. If an automated pass can
decide your constraint is stale and archive it, then you do not actually know
what your agents will be told tomorrow.

So the surface is two tools, not one button: `gardener_proposals` to read what it
found, `gardener_apply` to act on one.

## What it looks for

| Pass | Trigger | Proposes |
|---|---|---|
| **dedup** | Two memories more similar than `gardener.dedup_threshold` (0.88) | **merge**: one is kept as-is, the other superseded and pointed at it |
| **staleness** | A memory untouched for `gardener.staleness_days` (90) | **archive**: marked invalid, still readable |
| **digest** | Enough recent activity over `gardener.digest_days` (30) | a **digest** note summarizing it |
| **stale-plan** | A plan idle for `gardener.stale_plan_days` (14) with steps still open | surfacing it, so it is abandoned deliberately rather than by neglect |

Two more proposal types come from requests rather than the timer:

- **reproject** — a memory filed under the wrong project, moved to a project that
  **already exists**.
- **split** — one project divided into new child projects. This creates projects
  and a shared parent, so it is planned as a unit, not as a pile of moves. That
  is why it is a separate tool (`gardener_split`) and not just a reproject to a
  name that does not exist yet.

## Constraints and stages are exempt

Age-filtering and staleness-archiving never touch `constraint` or pinned `stage`
memories.

A constraint does not become less true by sitting still. "Never use CGO" is not
stale at 90 days — it is *settled*, and the absence of recent edits is evidence
it is working, not evidence it is rotting. Time-based archival encodes the
opposite assumption, so the pass simply skips the kinds where that assumption is
wrong.

## Asking for it in words

```text
gardener_request "fold the two console theme memories together"
```

`gardener_request` takes natural language and turns it into the same reviewable
proposals as the timed passes. It is a way to *aim* the gardener, not a way to
bypass the review step — what comes back is still a proposal.

## Where you review

- **The console** — `/console/gardener` lists proposals with what each would do.
- **`gardener_proposals`** — the same, for an agent.

The gardener runs every `gardener.interval_minutes` (60) when
`gardener.enabled` is on. Everything in this page is tunable — see
[Configuration](/reference/configuration/).
