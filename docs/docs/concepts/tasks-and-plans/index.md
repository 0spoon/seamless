# Tasks & plans

> The dependency-aware ready queue, lease-based claiming that lets parallel agents divide work, and plans as compositions rather than primitives.

Seamless gives coding agents a shared task queue that is dependency-aware and
claimed under leases: a task becomes ready when its `depends_on` blockers
finish, `tasks_claim` takes it atomically so two parallel agents never work the
same task, and a crashed agent's lease expires instead of stranding the work.
A plan is deliberately not a primitive - it is a composition of a narrative
note and step tasks keyed by `plan:<slug>`.

Tasks are how agents hand work to each other. Plans are how a design survives the
agent that wrote it.

Not everything that blocks work belongs in the queue. A task is work someone
here can claim and finish; a state of the world you wait on - a PR awaiting
maintainer review, hardware in the mail - is a [`stage` memory](https://thereisnospoon.org/docs/concepts/memory/)
instead, which pins into the briefing while it gates rather than sitting in the
ready queue as forever-claimable work.

## The ready queue

A task is **ready** when it has no unfinished blocker. `tasks_ready` returns
exactly those, so an agent asking "what can I do now?" never reasons about the
dependency graph itself - it asks, and gets an actionable list.

`depends_on` names the tasks that must finish first. Either `done` or `dropped`
unblocks a dependent: work that was abandoned deliberately should not wedge the
queue forever. Cycles are rejected at creation, not discovered at scheduling
time.

## Claiming: the part that makes parallelism safe

Two agents that both call `tasks_ready` see the same task. Both will try to take
it. Exactly one wins:

```text
Atomic claim race
Agent A
tasks_claim 01K… Claim wins ready → in_progress
lease expires at now + 900s
while working Heartbeat or finish Re-claim refreshes the lease; release, close, or session end frees it.
Agent B
tasks_claim 01K… Claim is refused Error names Agent A as the live holder.
next action Take another ready task If A crashes, the expired lease makes this task reclaimable by id.
Both agents may see the same ready row; only one can own it. The loser pivots without duplicating work.
```

Four rules carry the whole model:

1. **Claiming is atomic.** The loser gets an error naming the holder, not a
   corrupted second claim.
2. **Re-claiming refreshes the lease.** That is the heartbeat for long work - an
   agent still working keeps its claim by claiming again.
3. **An expired lease is reclaimable - but it does not re-queue itself.** See
   the two clocks below. This is the subtlety most likely to bite you.
4. **Closing frees it.** `tasks_release`, `tasks_update` to `done`/`dropped`, or
   `session_end` (which releases all of a session's claims).

([Lease-based task claiming](https://thereisnospoon.org/docs/concepts/lease-based-task-claiming/) defines the
primitive on its own; the
[two-agents scenario](https://thereisnospoon.org/scenarios/task-collision/) shows a live claim collision.)

### The two clocks

When an agent dies holding a task, two different timers matter, and conflating
them is how a fleet quietly stalls.

| | Lease expiry | Session reaper |
|---|---|---|
| Controls | `lease_seconds` (default 900) | `gardener.session_idle_minutes` (default 45) |
| Enforced | Lazily, inside `tasks_claim` | By the gardener's periodic pass |
| Effect | The task becomes **stealable by id** | The task's status returns to `open` |
| Visible in `tasks_ready`? | **No** | **Yes** |

`tasks_ready` returns tasks whose status is `open`. A task with an expired lease
is still `in_progress`, so it is **invisible to the queue** - an agent polling
`tasks_ready` will never see it, even though a `tasks_claim` on that specific id
would now succeed.

What actually puts it back in the queue is the reaper: it expires the dead
agent's idle session and releases its claims, flipping the task back to `open`.

Two consequences worth internalizing:

- The lease is not the recovery mechanism; the reaper is. The lease only decides
  whether a claim *can* be taken.
- **With `gardener.enabled: false`, that second clock never runs**, and a crashed
  agent's claims stay `in_progress` indefinitely. Nothing will surface them. If
  you turn the gardener off, you have also turned off task recovery - release
  them yourself with `tasks_release`, or from the console.

A lease is **not a lock on the files**. Nothing physically stops an agent from
working on a task it did not claim. It is a coordination signal between
cooperating agents - the point is that well-behaved agents do not collide, not
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
inherits not just a checklist but *why* the checklist looks like that - which is
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

See the [tasks reference](https://thereisnospoon.org/docs/reference/mcp/tasks/) for the tool surface.
