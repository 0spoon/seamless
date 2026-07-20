---
title: Glossary
description: The vocabulary, with the distinctions that actually matter - memory vs note vs finding, briefing vs recall, archive vs supersede vs delete.
---

Terms are listed alphabetically. The ones worth reading even if you think you
know them are the four **disambiguation** entries at the end: each is a pair
people routinely use interchangeably, and each pair means genuinely different
things.

## A–Z

**Ambient session** - a session opened automatically by a Claude Code or Codex
SessionStart hook, displayed with an opaque `cc/...` or `cx/...` handle, without
the agent asking. Lifecycle identity uses the full external session ID plus
client, not the display handle. Contrast *explicit session*.

**Arbitration** - deciding which of several competing memories wins when they
disagree. Part of the memory lifecycle, alongside supersession and provenance.

**Archive** - marking a memory invalid because it is no longer relevant. It
leaves the indexes and stays readable. Proposed by the gardener's staleness pass,
never done to a `constraint` or a pinned `stage`.

**Binding** - the association between an MCP connection and a session, set by
`session_start`. Everything on that connection inherits the bound session's
project.

**Briefing** - the `<seam-briefing>` block injected into an agent's context at
session start: constraints, pinned stages, plan rollups, the memory index, recent
findings. Assembled inside a token budget. See [Sessions &
briefings](/concepts/sessions/).

**Claim** - an atomic, leased hold on a task, taken with `tasks_claim`. Exactly
one agent can hold a live claim.

**Console** - the read-mostly web UI at `/console`. An observability surface for
you; agents use MCP.

**Constraint** - a memory kind: a rule the project cannot violate. Pinned into
every briefing, never dropped for budget, never staleness-archived.

**Description** - the one-line summary in a memory's frontmatter. The **only**
text shown in any index, and therefore the entire retrieval surface. See [Write
memories that get recalled](/guides/write-good-memories/).

**Digest** - a note summarizing recent activity, proposed by the gardener.

**Explicit session** - a session opened by calling `session_start`. It adopts the
ambient session for the same working directory rather than opening a second one.

**Family** - a set of projects related by parent/child, so a child's briefing can
carry the parent's memories and a sibling's recent findings.

**Fail closed** - the rule that a durable write with no resolvable scope is
rejected rather than defaulted to global. See [Projects &
scope](/concepts/projects/).

**Fail open** - the rule that a hook never blocks an agent: an internal error
still returns success. The cost is that failure is silent.

**Finding** - what a session learned, passed to `session_end` and surfaced in
later briefings. Not a memory: a finding is what *happened*, a memory is what is
*true*.

**FTS5** - SQLite's full-text search, the keyword half of recall.

**Gardener** - the periodic pass that finds duplicates, staleness, and drift and
**proposes** fixes. It never acts on its own. See [The
gardener](/concepts/gardener/).

**Global** - the scope with no project: visible to every agent in every repo.
Reached only by passing `project: global` deliberately.

**Kind** - a memory's type: `constraint`, `runbook`, `protocol`, `gotcha`,
`decision`, `refuted`, `reference`, or `stage`. See [Memory &
notes](/concepts/memory/).

**Lab** - a shared workspace for a systematic investigation, holding trials.
Opened with `lab_open`.

**Lease** - the expiry on a claim (default 900 seconds). Re-claiming refreshes
it; an expired lease is reclaimable, so a crashed agent does not strand a task.

**Memory** - a markdown file with frontmatter holding one durable piece of
knowledge. The unit that reaches briefings.

**Note** - a markdown file holding a work artifact: research findings, a meeting
summary, a design record. Found via recall; never injected into a briefing.

**Plan** - not a primitive. A composition keyed by `plan:<slug>`: a narrative
note, supporting notes, and step tasks. See [Tasks &
plans](/concepts/tasks-and-plans/).

**Project** - the scope memories, notes, tasks, and sessions belong to. Resolved
from an explicit argument, a bound session, or the agent's cwd.

**Proposal** - the gardener's output. A suggestion for you to review, applied
only with `gardener_apply`.

**Provenance** - the record of where knowledge came from and what replaced it:
`source_session`, `superseded_by`, `invalid_at`.

**Ready** - a task with no unfinished blocker. `tasks_ready` returns exactly
those.

**Recall** - the single search entry point, fusing FTS5 and vector similarity
with RRF. Also, loosely, the `<seam-recall>` block injected on prompt match -
see the disambiguation below.

**Reproject** - moving a memory to a different project that **already exists**.
Moving it to one that does not is a *split*.

**RRF (reciprocal rank fusion)** - how recall combines the keyword and vector
rankings so neither retriever gets a veto.

**Session** - one agent's stretch of work. Heartbeats; an idle one is reaped and
marked `expired`.

**Split** - dividing one project into new child projects, creating them and a
shared parent. Planned as a unit by `gardener_split`.

**Stage** - a memory kind recording where multi-session work stands. Pinned into
briefings like a constraint.

**Supersede** - replacing an outdated memory with a new one. The old is marked
invalid, leaves the indexes, and stays readable pointing at its replacement.

**Trial** - one attempt recorded in a lab: what was tried, what was expected,
what happened.

**ULID** - the id format, sortable by creation time. Never UUID.

## Four distinctions worth getting right

**Memory vs. note vs. finding.** A *memory* is what is true (injected into
briefings). A *note* is what you produced (found by searching). A *finding* is
what a session learned (surfaced as recent activity). The test for the first
two: would a future agent need this injected before it starts? Then it is a
memory.

**Briefing vs. recall injection vs. recall call.** All three end with the agent
knowing something, which is why they blur. The *briefing* fires at session start
and is unconditional. A *recall injection* fires on prompt match, mid-turn, still
without the agent asking. A *recall call* is the agent choosing to search. The
first two are ambient; only the third is a decision. See
[Recall](/concepts/recall/).

**Ambient vs. explicit session.** *Ambient* is opened by the hook, per agent,
automatically. *Explicit* is `session_start`, which **adopts** the ambient one
rather than creating a rival.

**Archive vs. supersede vs. delete.** *Archive*: no longer relevant, marked
invalid, still readable. *Supersede*: replaced by something specific, marked
invalid, still readable, **pointing at its replacement**. *Delete*: gone. The
rule of thumb - delete is for things that were never true; supersede is for
things that stopped being true; archive is for things that stopped mattering.
