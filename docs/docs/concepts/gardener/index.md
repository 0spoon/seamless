# The gardener

> The background pass that finds duplicates, staleness, and drift - and proposes, because nothing rewrites your knowledge behind your back.

The Seamless gardener is the background pass that keeps an agent-written memory
store from rotting: it detects near-duplicate memories by embedding similarity,
memories gone stale, and plans abandoned with steps still open, and files each
finding as a proposal. Nothing is applied until you approve it with
`gardener_apply` - the gardener has no write path of its own.

Any store that agents write to accumulates cruft: two memories saying the same
thing in different words, a runbook for a system that no longer exists, a plan
abandoned three weeks ago with its steps still open.

The gardener finds those. Then it stops and asks.

## Propose-only is the whole contract

**Every pass the gardener makes ends in a proposal for you to review.** It never
edits, merges, or archives on its own.

This is not timidity. The entire premise of Seamless is that the knowledge is
yours and legible - a store that quietly rewrites itself while you sleep is
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
| **stale-stage** | A `stage` memory whose `Status:` header is done, missing, or unrecognized, unchanged for `gardener.stale_stage_days` (14) | **archive**: a stage that gates nothing should not hold a permanent briefing pin |
| **dead-weight** | A memory briefings injected 20+ times in 30 days without a single recall hit, prompt match, or read | **archive**: exposure without demand means it costs tokens and steers nothing |
| **memory-wanted** | The same recall query returned zero hits in 2+ sessions inside 14 days | **memory_wanted**: write the knowledge agents keep searching for; applying opens a task in the queue |

The memory-wanted pass is the one place the gardener asks *for* knowledge
instead of curating what exists: recurring zero-hit `recall` queries are demand
for a memory nobody wrote, grouped per project so a one-off miss never fires.
Applying the proposal opens a task ("Write a memory: ...") in the ready queue -
the memory itself is only ever written by whoever picks the task up.

Two more proposal types come from requests rather than the timer:

- **reproject** - a memory filed under the wrong project, moved to a project that
  **already exists**.
- **split** - one project divided into new child projects. This creates projects
  and a shared parent, so it is planned as a unit, not as a pile of moves. That
  is why it is a separate tool (`gardener_split`) and not just a reproject to a
  name that does not exist yet.

## Constraints and stages are exempt from staleness

Age-filtering and staleness-archiving never touch `constraint` or `stage`
memories.

A constraint does not become less true by sitting still. "Never use CGO" is not
stale at 90 days - it is *settled*, and the absence of recent edits is evidence
it is working, not evidence it is rotting. Time-based archival encodes the
opposite assumption, so the pass simply skips the kinds where that assumption is
wrong.

Stages get their own pass instead, because staleness cannot see them at all:
a pinned stage is re-injected into every briefing, so by the activity metric it
never goes quiet. The stale-stage pass keys off the *update* time and the
`Status:` header - a stage still carrying a live gate
(`open`/`in_progress`/`blocked`) is never proposed at any age, while one that is
done, headerless, or unparseable is proposed for archiving once it has sat
unchanged for `gardener.stale_stage_days`. Both passes respect `[[links]]`: a
memory another body points at is not proposed.

## Asking for it in words

```text
gardener_request "fold the two console theme memories together"
```

`gardener_request` takes natural language and turns it into the same reviewable
proposals as the timed passes. It is a way to *aim* the gardener, not a way to
bypass the review step - what comes back is still a proposal.

## Where you review

- **The console** - `/console/gardener` lists proposals with what each would do.
- **`gardener_proposals`** - the same, for an agent.

The gardener runs every `gardener.interval_minutes` (60) when
`gardener.enabled` is on. Everything in this page is tunable - see
[Configuration](https://thereisnospoon.org/docs/reference/configuration/).
