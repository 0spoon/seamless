---
title: Sessions, memory & recall
description: The eight tools an agent uses most - open a session, write and read memory, and search the store.
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
session per agent - calling `session_start` explicitly adopts it and gets you the
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
invalid, pointing at what replaced it. Delete is for mistakes - things that were
never true - not for things that stopped being true.

A `supersedes` that fails is reported rather than swallowed: the new memory is
still written and kept, and the call returns an error naming it. The target is
then still active, so re-run the supersede.

## Scope

`memory_write` **fails closed**: with no session and no explicit `project`, it is
rejected as ambiguous rather than silently landing in the global scope. Pass
`project: global` to write a deliberately cross-project memory.

## Recall is the only search tool

There is one search entry point. `recall` fuses FTS5 keyword matching and vector
similarity with reciprocal rank fusion, nudges the fused order by favorite and
[utility](/concepts/recall/#the-utility-nudge) (both bounded), scoped to the
current project plus global items, and packs results into a token budget. A call
that finds nothing is recorded as a miss - recurring misses become the
gardener's [memory-wanted proposals](/concepts/gardener/#what-it-looks-for).

The optional `kind` filter restricts hits to memories of one frontmatter kind.
It implies memories-only: combining it with `scope=notes` is rejected as
contradictory rather than returning a misleading empty result, and a
kind-filtered miss still counts as memory-wanted demand.

With `kind` set, `query` becomes optional: a kind alone is the **browse mode**
behind briefing hints like `recall kind=convention` - the scope's active
memories of that kind, listed newest-first under the same limit and token
budget. A browse is a listing, not a search: no fusion, no favorite or utility
boost, its hits record as passive exposure (never query-gated demand), and an
empty browse records no miss - "this project has no conventions yet" is not a
missing memory.

It degrades rather than fails: if the embedding provider is unreachable, recall
falls back to keyword-only results instead of erroring. A local misconfiguration
is surfaced instead of hidden - the two cases are deliberately not treated alike.

## Results and failures

| Call | Success result | Failure that matters |
|---|---|---|
| `session_start` | `session_id`, `name`, resolved `project`, explanatory `scope`, and `briefing`; resumed/adopted sessions also say `resumed: true` | Briefing assembly degrades to an empty string and logs; creating or binding the session itself still fails loudly |
| `memory_write` | Stable `id`, canonical `name`, resolved `project`, `updated`, optional `similar`, and optional `superseded` | An occupied tombstone path is an error; if the new memory lands but supersession fails, the tool errors while naming the kept replacement and the still-active target |
| `recall` | `hits`, possibly empty | Remote embedder failures degrade to lexical-only; local request/config construction errors surface |
| `session_end` | Confirmation of the close - `session_id`, `claims_released`, `mishaps_recorded`; findings persist for the next briefing | A missing/ambiguous session is an error rather than a fabricated successful close |
