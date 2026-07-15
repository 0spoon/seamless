---
title: Notes, projects & capture
description: Work artifacts, project scope, and SSRF-safe URL capture - the eight tools around the edges of memory.
generate: mcp-tools
tools:
  - notes_create
  - notes_read
  - notes_update
  - notes_append
  - notes_delete
  - project_list
  - project_create
  - capture_url
---

## A note is not a memory

This is the most common confusion in the whole system, so it is worth being blunt
about it.

|  | Memory | Note |
|---|---|---|
| Answers | "What is true about this project?" | "What did we produce?" |
| Length | One idea, one line of `description` | However long the artifact is |
| Lifecycle | Superseded, archived, arbitrated | Written, occasionally updated |
| Reaches a briefing | Yes - this is what agents get injected | No; found via `recall` |
| Good examples | A constraint, a gotcha, a decision | Research findings, a meeting summary, a design record |

The test: **would a future agent need this injected into its context before it
starts working?** If yes, it is a memory, and it needs to fit in a `description`.
If it is something you'd want to *find* and read in full when the topic comes up,
it is a note.

Writing a journal entry into memory is the classic failure: it is too long to
inject, too specific to generalize, and it pushes real constraints out of the
briefing's budget.

Agent-created notes are automatically tagged `created-by:agent`.

## Notes are how plans get their narrative

A plan is not a primitive - it is a composition keyed by `plan:<slug>`. Tag a
note `plan:<slug>` and it joins that plan's supporting context, so the next agent
inherits the design and the reasoning behind it, not just the step list. See
[Tasks](/reference/mcp/tasks/) for the step half of the composition.

## Projects and scope

`project_list` and `project_create` manage the scopes everything else inherits.
Most agents never call either: a repo mapped with `seamlessd map-repo` resolves
its project from the agent's working directory, and `session_start` binds it.

The `global` project slug is the deliberate cross-project scope. It is a token
you pass on purpose, never a default you fall into - see the fail-closed rule in
the [MCP API overview](/reference/mcp/).

## capture_url is SSRF-safe on purpose

`capture_url` fetches a URL and returns its content as markdown. It is the one
tool that makes an outbound request on an agent's behalf, so it is guarded:
destination ports are restricted to `capture.allowed_ports` (80 and 443 by
default, never "any port"), and the fetcher refuses to be talked into reaching
things it should not. See [Configuration](/reference/configuration/).
