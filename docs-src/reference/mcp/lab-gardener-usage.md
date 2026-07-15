---
title: Lab, gardener & usage
description: Research trials, the propose-only gardener, and the usage summary — the eight tools for keeping the store honest.
generate: mcp-tools
tools:
  - lab_open
  - trial_record
  - trial_query
  - gardener_proposals
  - gardener_request
  - gardener_split
  - gardener_apply
  - usage_summary
---

## The research lab

A lab is a shared workspace for a systematic investigation: a hypothesis, a
series of trials, and what each one actually did. `lab_open` binds a lab to the
connection, so `trial_record` inherits it the way memory inherits a session's
project. `trial_query` reads the trials back.

The point is **parallel agents collaborating on one investigation**. Several
agents chasing the same bug can open the same lab, record what they tried, and
see each other's dead ends instead of re-walking them. When the investigation
resolves, distill the finding into a memory — the lab is the working record, not
the conclusion.

## The gardener proposes; it never acts

The gardener runs periodically over the store looking for drift. Every pass it
makes ends in a **proposal row for you to review** — it never rewrites your
knowledge behind your back. That is the whole contract, and it is why the tool
surface has both `gardener_proposals` (read them) and `gardener_apply` (act on
one) instead of a single "tidy up" button.

Its passes:

| Pass | What it looks for |
|---|---|
| dedup | Two memories that say the same thing, above `gardener.dedup_threshold` similarity. Proposes a **merge**: one is kept, the other superseded and pointed at it. |
| staleness | Memories untouched for `gardener.staleness_days`. Proposes an **archive**: marked invalid, still readable. |
| digest | Enough recent activity to be worth a summary over `gardener.digest_days`. Proposes a **digest** note. |
| stale-plan | Plans idle for `gardener.stale_plan_days` with steps still open. |

Constraints and pinned stages are never age-filtered or staleness-archived. A
constraint does not become less true by sitting still.

`gardener_request` takes a request in natural language ("fold the two console
theme memories together") and turns it into the same reviewable proposals. It can
also **reproject** a memory filed under the wrong project — moving it to a
different project that already exists.

## Splits are a different thing

Moving a memory into a project that does not exist yet is not a reproject — it is
a split, and `gardener_split` plans it. Splitting one project into new children
creates those projects and a shared parent, so it is planned as a unit rather
than as a pile of individual moves.

## usage_summary

`usage_summary` reports what the store actually contains and what retrieval has
been doing: memory counts, retrieval statistics, events by kind. It answers "is
this thing working, and on how much?" — the same question the console's Overview
answers, for an agent that cannot open a browser.
