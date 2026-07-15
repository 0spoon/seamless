---
title: Tasks
description: The six task tools — the dependency-aware ready queue and lease-based claiming that lets parallel agents divide work safely.
generate: mcp-tools
tools:
  - tasks_add
  - tasks_update
  - tasks_ready
  - tasks_list
  - tasks_claim
  - tasks_release
---

Tasks are how agents hand work to each other. A task is **ready** when it has no
unfinished blocker; `tasks_ready` returns exactly those, so an agent asking "what
can I do now?" never has to reason about the dependency graph itself.

## How claiming works

Claiming is the concurrency control. Two agents that both see the same ready task
will both try to take it, and exactly one wins:

1. `tasks_claim` atomically moves a ready task to `in_progress` and stamps a
   lease (default 900 seconds). The loser gets an error naming the holder.
2. Re-claiming a task you already hold **refreshes** the lease — that is the
   heartbeat for long work.
3. An **expired** lease is reclaimable: an agent that crashed mid-task does not
   strand it forever.
4. `tasks_release`, closing the task (`tasks_update` to `done`/`dropped`), or
   `session_end` frees the claim.

A lease is not a lock on the files — it is a coordination signal between
cooperating agents. Nothing stops a determined agent from working on a task it
did not claim; the point is that well-behaved agents do not collide.

## Plan steps are not queue items

A task created with `plan=<slug>` is a **step of a plan**, and is excluded from
the default `tasks_ready` and `tasks_list`. That keeps a twelve-step plan from
burying the handful of tasks that are actually loose work. Pass `plan=<slug>` to
either tool to see that plan's steps instead.
