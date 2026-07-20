---
title: Run research trials
description: The lab loop for systematic debugging - recording what was tried, letting parallel agents share dead ends, and distilling the result into memory.
---

Some problems are not solved by thinking harder; they are solved by trying twelve
things and keeping track. Firmware that boots sometimes. A race that reproduces
one time in thirty. A config that works on one machine.

Agents are bad at this in a specific way: each one starts fresh, re-derives the
same first three hypotheses, and tries them again. A **lab** is where that stops.

## The loop

```text
lab_open        ── name the investigation; binds it to your connection
    │
    ▼
trial_query     ── what has already been tried? (read BEFORE you try anything)
    │
    ▼
trial_record    ── what you tried, what you expected, what actually happened
    │
    └──▶ repeat
    │
    ▼
memory_write    ── distill: the finding, as a gotcha or a decision
```

`lab_open` binds the lab to the connection the same way `session_start` binds a
project, so `trial_record` inherits it without you naming the lab every time.

## Record the expectation, not just the result

The field that earns its keep is **what you expected**. A trial that says "tried
X, it failed" is worth little. One that says "expected the firmware to enumerate
after DFU because the descriptor changed; it enumerated but with the old PID"
tells the next agent which model of the system was wrong - and that is the actual
product of debugging.

Record trials that *succeeded* too. "This worked" is what stops the next agent
from redesigning a thing that already works.

## Parallel agents in one lab

This is the reason the lab exists rather than a scratch note.

Several agents can open the **same lab** and work the same problem concurrently.
Each one calls `trial_query` first and sees what the others have already
eliminated. Where a fleet of independent agents would explore the same three
obvious hypotheses three times, a fleet sharing a lab divides the space.

Pair it with the [ready queue](/concepts/tasks-and-plans/) when the trials are
enumerable up front: one task per hypothesis, `tasks_claim` to avoid two agents
testing the same one. See [Coordinate multiple
agents](/guides/coordinate-agents/).

## Distil, then stop

A lab is a working record, not a conclusion. When the investigation resolves,
write **one memory** that captures what is now known:

- The thing that was wrong, as a `gotcha` - with the symptom in the description,
  so the next agent recognizes it before diagnosing it.
- The thing you decided, as a `decision` - with the alternatives you rejected.
- The thing you believed that turned out false, as `refuted` - this is the kind
  people skip, and it is what stops the fleet re-deriving the dead end.

Do not copy the trial log into memory. The trials stay in the lab, queryable; the
memory carries the conclusion. A briefing has a token budget, and twelve trials
of a solved problem are not what an agent needs injected before it starts work.
See [Write memories that get recalled](/guides/write-good-memories/).

## The skill

The installer drops the portable `seam-research` package into the selected
client's skill home. From a clone, use `make install-research-skill
CLIENT=claude` for Claude Code or `make install-research-skill CLIENT=codex` for
Codex (`CLIENT=detect` is the default). It wraps this loop
- open the lab, query before trying, predict before running, record once with
the outcome, distill decisions into memory - and can activate when an
investigation becomes repeated experiments. Start or resume one explicitly with
`/seam-research <lab-name> <problem>` in Claude Code or
`$seam-research <lab-name> <problem>` in Codex.

## The tools

`lab_open`, `trial_record`, and `trial_query` are documented in
[Lab, gardener & usage](/reference/mcp/lab-gardener-usage/).
