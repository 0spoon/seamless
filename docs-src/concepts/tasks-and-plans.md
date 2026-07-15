---
title: Tasks & plans
description: The dependency-aware ready queue, lease-based claiming that lets parallel agents divide work, and plans as compositions rather than primitives.
---

Tasks are how agents hand work to each other. Plans are how a design survives the
agent that wrote it.

## The ready queue

A task is **ready** when it has no unfinished blocker. `tasks_ready` returns
exactly those, so an agent asking "what can I do now?" never reasons about the
dependency graph itself — it asks, and gets an actionable list.

`depends_on` names the tasks that must finish first. Either `done` or `dropped`
unblocks a dependent: work that was abandoned deliberately should not wedge the
queue forever. Cycles are rejected at creation, not discovered at scheduling
time.

## Claiming: the part that makes parallelism safe

Two agents that both call `tasks_ready` see the same task. Both will try to take
it. Exactly one wins:

```text
agent A: tasks_claim 01K...           agent B: tasks_claim 01K...
            │                                     │
            ▼                                     ▼
   ┌──────────────────┐                  ┌──────────────────┐
   │ ready → in_progress │               │ error: already   │
   │ lease_expires_at =  │               │ claimed by A     │
   │   now + 900s        │               └──────────────────┘
   └──────────────────┘                            │
            │                                      ▼
            │                            B claims the next ready task
            ▼
   A works... re-claims to heartbeat (lease refreshed)
            │
            ├─ tasks_release / tasks_update done / session_end → freed
            └─ A crashes → lease expires → reclaimable by anyone
```

Four rules carry the whole model:

1. **Claiming is atomic.** The loser gets an error naming the holder, not a
   corrupted second claim.
2. **Re-claiming refreshes the lease.** That is the heartbeat for long work — an
   agent still working keeps its claim by claiming again.
3. **An expired lease is reclaimable.** An agent that crashed mid-task does not
   strand it forever, which is the failure mode that makes naive locks unusable
   for processes that die.
4. **Closing frees it.** `tasks_release`, `tasks_update` to `done`/`dropped`, or
   `session_end` (which releases all of a session's claims).

A lease is **not a lock on the files**. Nothing physically stops an agent from
working on a task it did not claim. It is a coordination signal between
cooperating agents — the point is that well-behaved agents do not collide, not
that misbehaving ones cannot.

## Plans are a composition, not a primitive

There is no `plan` object in Seamless. A plan is everything keyed by
`plan:<slug>`:

| Piece | How | Why |
|---|---|---|
| **Narrative** | A note tagged `plan:<slug>` | The design and the reasoning behind it |
| **Supporting context** | More notes, same tag | The research that informed it |
| **Steps** | Tasks created with `plan=<slug>` | The work, with dependencies |
| **Progress** | The steps' statuses | Rolled into the briefing automatically |

This composition is the reason a plan survives its author. The next agent
inherits not just a checklist but *why* the checklist looks like that — which is
the thing that usually evaporates between sessions.

**Plan steps are excluded from the default queue.** `tasks_ready` and
`tasks_list` skip them, so a twelve-step plan does not bury the handful of tasks
that are genuinely loose work. Pass `plan=<slug>` to either tool to see that
plan's steps instead.

The briefing surfaces each active plan as one rolled-up line:

```text
PLAN: marketing -- 2/3 done, 1 claimable, 0 in flight
```

That is enough for an agent to decide whether to pick something up, without
spending briefing budget listing every step.

## Claude Code plan mode feeds this automatically

You do not have to build the composition by hand. Claude Code's plan mode is
captured:

- Saving a plan file upserts a `cc-plan-<basename>` note, tagged with its status
  (`plan-status:draft|presented|approved|abandoned`).
- Planning subagents are cached as `cc-agent-<id>` notes in the same composition.
- Approving the plan creates the tracking task.
- Unapproved captures appear in the briefing as `PLAN (awaiting approval)` lines,
  so a plan that was designed and then forgotten is visible rather than lost.

See the [tasks reference](/reference/mcp/tasks/) for the tool surface.
