---
title: What is Seamless?
description: A local-first memory and coordination substrate for the fleet of coding agents you run - markdown files you own, indexed by one Go binary.
---

Seamless gives the coding agents you run a **shared, persistent brain**: memory
that survives the end of a conversation, tasks they can hand off to each other,
and plans they can execute together. It stores all of it as markdown files in a
directory you own, indexes it in one SQLite database, and serves it to agents
over MCP from a single Go binary on your machine.

Its clients are agents. You are the observer and editor - there is a console, but
nothing in Seamless requires you to be in the loop for agents to use it.

## What it gives a fleet

- **Memory with a lifecycle.** Not an append-only log: memories are superseded,
  archived, and arbitrated, so what an agent recalls is what is currently true.
- **Ambient sessions.** Claude Code and Codex hooks open a session per agent,
  inject a budgeted briefing at startup, and harvest findings. No tool calls are
  required from the agent.
- **A ready-queue.** Dependency-aware tasks with atomic lease-based claiming, so
  parallel agents divide work without stepping on each other.
- **Hybrid recall.** One search entry point fusing keyword and vector search.
- **A console you can read.** Every memory, session, task, and retrieval decision
  is inspectable. The files are plain markdown; the store is not a black box.

## Design principles

**Files are the source of truth.** Durable knowledge lives in markdown you can
read, `grep`, edit, and put in git. The database is a rebuildable index over
them, plus the record for high-churn state (sessions, tasks, events).

**Local-first.** One binary, one SQLite file, bound to loopback. No external
database, no cloud dependency, no telemetry.

**Propose, don't act.** The gardener finds duplicates, staleness, and drift - and
proposes. Nothing rewrites your knowledge behind your back.

## Where to go next

| If you want to | Read |
|---|---|
| Get it running in ten minutes | [Quickstart](/quickstart/) |
| Wire it into Claude Code | [Claude Code setup](/claude-code/) |
| Wire it into Codex | [Codex CLI setup](/codex-cli/) |
| Understand the model before trusting it | [How Seamless works](/concepts/how-it-works/) |
| Point another MCP agent at it | [Integrate your agent](/guides/integrate-your-agent/) |
| Make a fleet divide work without colliding | [Coordinate multiple agents](/guides/coordinate-agents/) |
| Look up a tool, key, or command | [Reference](/reference/) |
| Fix something that is silently not working | [Troubleshooting](/guides/troubleshooting/) |
