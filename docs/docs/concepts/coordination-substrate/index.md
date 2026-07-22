# What is a coordination substrate?

> A definition - the durable, shared layer underneath a fleet of AI agents where memory, tasks, and plans live, so agents in different processes, sessions, and clients can act like one team.

A coordination substrate for AI agents is the durable, shared layer *underneath*
a fleet of agents: the place where memory, tasks, plans, and findings live so
that agents which never share a process, a context window, or even a vendor can
still act like one team. It is not a framework - it does not run your agents.
It is not a message bus - agents do not talk to each other through it in real
time. It is not a RAG pipeline - it is not about retrieving documents. It is
shared state with rules: what gets remembered, who holds which task, which of
two contradicting facts is current.

The term is doing a specific job. "Agent memory" names half the problem -
what an agent knows. A substrate also carries the other half - what the fleet
is *doing*: which plan step is claimed and by whom, what yesterday's session
concluded, what the next agent should pick up. Memory without coordination
re-derives the backlog every session; coordination without memory repeats last
month's mistakes on schedule.

## What qualifies

Four properties make a layer a substrate rather than a feature of some agent:

1. **Durable beyond any session.** State survives the context window, the
   process, and the day. A shared task list that dies with the team that made
   it is coordination, but not a substrate.
2. **Shared across runtimes.** An agent in one client can read what an agent
   in another wrote. State locked inside a single vendor's runtime is a silo
   with good ergonomics.
3. **Legible to the human.** The operator can read, diff, and edit the shared
   state directly - because a fleet's shared brain that its owner cannot
   inspect is a liability, not an asset.
4. **Governed by lifecycle rules.** Concurrent writers require rules:
   [supersession](https://thereisnospoon.org/docs/concepts/memory-supersession/) so contradictions
   resolve instead of accumulating,
   [leases](https://thereisnospoon.org/docs/concepts/lease-based-task-claiming/) so work is claimed
   without being stranded.

## Seamless as one implementation

[Seamless](/) is a coordination substrate built local-first: memory and notes
as markdown files in a folder you own, a SQLite index over them, a
dependency-aware task queue with lease claiming, plans as compositions of
notes and tasks, all exposed to agents over MCP with session-start hooks for
Claude Code and Codex CLI. The [how it works](https://thereisnospoon.org/docs/concepts/how-it-works/)
page walks the moving parts; the
[two agents, one queue scenario](https://thereisnospoon.org/scenarios/task-collision/) shows the
substrate doing its job in two live recorded sessions.

What a substrate is *not* for, in Seamless's case: it is not a hosted team
knowledge base, not a RAG framework over your documents, and not an
orchestrator that schedules or spawns agents. It is the ground they stand on.
