---
title: Sessions & briefings
description: How an agent gets your knowledge injected before it does anything — ambient sessions, the briefing's packing order, and what never gets dropped.
---

A **session** is one agent's stretch of work. A **briefing** is what that agent
gets handed at the start of it, before it has called a single tool.

This is the mechanism that makes Seamless ambient rather than opt-in. An agent
does not have to remember to ask what it knows; a Claude Code SessionStart hook
resolves the working directory to a project, assembles a briefing inside a token
budget, and injects it into the agent's context. The agent begins already knowing
your constraints.

## Ambient vs. explicit sessions

| | Ambient | Explicit |
|---|---|---|
| Opened by | The SessionStart hook, automatically | `session_start` |
| Named | `cc/<id>` | Whatever you pass, or generated |
| Gets | The short injected briefing | The full briefing, returned by the call |

They are not two competing sessions. `session_start` **adopts** the sole ambient
session for the same working directory rather than opening a second one — that
adoption rule exists because the alternative was double-counting every agent.

Call `session_start` when the work is non-trivial: it returns the full briefing
(the injected one is deliberately shorter), and it binds the connection so
everything afterward inherits the project scope.

## An actual briefing, annotated

This is a real briefing, in the order the assembler packs it:

```text
<seam-briefing>
Seam project: seamless -- 4 constraints, 58 memories, 3 recent findings.
CONSTRAINT: errcheck-check-blank-two-category-rule: errcheck runs with check-blank ...
CONSTRAINT: llm-degradation-remote-vs-local: llm errors split remote ...
STAGE: deep-audit-f15-f18-landed -- status unknown
PLAN: marketing -- 2/3 done, 1 claimable, 0 in flight
PLAN (awaiting approval): seamless-documentation-site -- (presented, 2m)

Memories (seamless):
- gofmt-must-scope-to-tracked-files: gofmt walks the filesystem while go's ./... skips ...
- shared-worktree-concurrent-agents-verify: Agents share the main worktree ...
- (+34 older -- use recall)

Recent findings:
Recall on demand with recall; read a memory with memory_read.
Seam session: cc/8dd2fd5b (ambient)
</seam-briefing>
```

Line by line:

- **The header** counts what exists, so an agent knows how much it is *not* being
  shown.
- **`CONSTRAINT:` lines** come first and are **never dropped for budget**. A
  constraint is a rule the project cannot violate; a briefing that omitted one to
  fit a token budget would be worse than no briefing at all.
- **`STAGE:` lines** are pinned right after constraints, for the same reason: a
  gated stage's status is load-bearing for the whole session.
- **`PLAN:` rollups** follow, also pinned. The counts (`2/3 done, 1 claimable`)
  tell the next agent what work it can pick up right now.
- **The memory index** is `name: description` only — the description is the *only*
  text an index ever shows, which is why writing a good one matters more than
  writing a good body.
- **`(+34 older -- use recall)`** is the honest tail: the index was trimmed, and
  the briefing says so instead of pretending it is complete.
- **Recent findings** are what previous sessions learned, harvested at their end.

## The budget, and what survives it

Sections are packed against `budgets.max_briefing_tokens` (default 1500), then
the whole thing is hard-capped at `briefing.hard_cap_multiplier` times that
(default 2x).

The **never-drop invariant**: constraints, pinned stages, and active-plan rollups
are counted first and are exempt from budget dropping. Everything else — the
memory index, sibling findings, sibling memories, recent findings, ready tasks —
is packed in that order until the budget runs out. Later sections lose first.

Every knob is tunable in [Configuration](/reference/configuration/), and the
`briefing:` block is also editable live in the console. Those runtime edits are
stored in the database and win over both the file and the environment, applying
from the next session start without a daemon restart. If a briefing setting seems
to be ignored, check the console before you check the YAML.

## Liveness

Sessions heartbeat. A session that ends cleanly cascades immediately; one that is
abandoned is caught by an idle reaper after `gardener.session_idle_minutes` and
marked `expired`. The TTL only applies when there was no end signal at all — a
crashed agent's session does not sit "live" forever, and a slow one is not
reaped out from under itself.

`session_end` is where findings come from. Keep them tight — briefings show a
short preview — but they are stored in full, not rejected, so a long finding is
fine when it earns it.
