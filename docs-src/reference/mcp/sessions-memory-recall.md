---
title: Sessions, memory & recall
description: The eight tools an agent uses most — open a session, write and read memory, and search the store.
generate: mcp-tools
tools:
  - session_start
  - session_update
  - session_end
  - memory_write
  - memory_append
  - memory_read
  - memory_delete
  - recall
---

These eight tools are the agent loop. In a repo mapped to a project, most of them
need no `project` argument at all: `session_start` binds the connection, and
everything after it inherits that scope.

## The shape of a session

`session_start` returns the project briefing and binds the session to the
connection. `session_end` persists findings for the next agent's briefing. Both
are optional in the sense that Claude Code's hooks already open an *ambient*
session per agent — calling `session_start` explicitly adopts it and gets you the
full briefing rather than the short injected one.

If `tasks_update` ever fails claiming a task is held by *your own* session id,
the connection binding was lost. Re-run `session_start` with the same name to
rebind.

## Update, append, supersede, or delete?

Four ways to change memory, and picking the wrong one is how a store rots:

| You want to | Use | What happens |
|---|---|---|
| Correct or extend what a memory says | `memory_write` with the same `name` | Updated in place; the id is stable |
| Add to the end without rereading it | `memory_append` | Body grows; nothing else changes |
| Replace a **different**, now-outdated memory | `memory_write` with `supersedes` | The old one is marked invalid, leaves every index, and stays readable with a pointer to its replacement |
| Remove something written by mistake | `memory_delete` | Gone |

The distinction that matters is **supersede vs. delete**. Superseding is how the
store stays honest about its own history: the old memory leaves the briefing and
recall, but an agent that follows an old reference still finds it, marked
invalid, pointing at what replaced it. Delete is for mistakes — things that were
never true — not for things that stopped being true.

A `supersedes` that fails is reported rather than swallowed: the new memory is
still written and kept, and the call returns an error naming it. The target is
then still active, so re-run the supersede.

## Scope

`memory_write` **fails closed**: with no session and no explicit `project`, it is
rejected as ambiguous rather than silently landing in the global scope. Pass
`project: global` to write a deliberately cross-project memory.

## Recall is the only search tool

There is one search entry point. `recall` fuses FTS5 keyword matching and vector
similarity with reciprocal rank fusion, scoped to the current project plus global
items, and packs results into a token budget.

It degrades rather than fails: if the embedding provider is unreachable, recall
falls back to keyword-only results instead of erroring. A local misconfiguration
is surfaced instead of hidden — the two cases are deliberately not treated alike.
