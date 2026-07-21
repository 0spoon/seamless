---
title: Guides
description: Task-shaped walkthroughs - wiring an agent into the loop, writing memories that get recalled, coordinating a fleet, capturing plans, running trials, and fixing what broke.
---

Where the [concepts](/concepts/) pages explain how Seamless thinks, these pages
are organized around jobs: each one starts from something you are trying to do
and walks the shortest honest path through it, including the failure modes.

If you are connecting a client, start with
[Integrate your agent](/guides/integrate-your-agent/) - the MCP handshake, the
stdio bridge, session binding, and the scope discipline that keeps writes
landing in the right project. Once an agent is writing,
[Write memories that get recalled](/guides/write-good-memories/) is the guide
that pays for itself: the description line is the retrieval surface, and the
difference between a store that compounds and one that fills with noise is
mostly the four habits that page names.

Two guides cover multi-agent work.
[Coordinate multiple agents](/guides/coordinate-agents/) shows fan-out over the
ready queue, planner/executor splits via plan composition, and what happens
when a claim holder dies mid-task. [Run research trials](/guides/research-trials/)
is the lab loop for systematic debugging - recording every attempt so parallel
agents share dead ends instead of repeating them. Related:
[Capture Claude Code plans](/guides/plan-mode/) explains how plan mode is
captured into notes and tasks automatically, and which hook does what.

The last two are operational. [Import, back up & restore](/guides/data/) covers
putting `~/.seamless` in git, what deleting `seam.db` actually costs (an index
rebuild, not data loss), and moving to a new machine.
[Troubleshooting](/guides/troubleshooting/) is symptom-first, written for a
system whose hooks deliberately fail open - where a broken install looks like
silence, not an error message.
