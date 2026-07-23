---
title: What is Seamless?
description: A local-first memory and coordination substrate for the fleet of coding agents you run - markdown files you own, indexed by one local daemon.
---

Seamless is a local-first memory and coordination system for coding agents -
a **shared, persistent brain** for Claude Code, Codex, and any other MCP client
you run. Memory survives the end of a conversation, tasks can be handed from one
agent to another, and plans get executed together. All of it is stored as
markdown files in a directory you own, indexed by one SQLite database, and
served over MCP by the local `seamlessd` daemon. The companion `seam` CLI gives
headless agents a direct interface. There is no hosted Seamless service,
external vector database, or account.

Its clients are agents. You are the observer and editor - there is a console, but
nothing in Seamless requires you to be in the loop for agents to use it. And it
is one instance on one machine, bound to loopback: a personal substrate, not a
hosted team knowledge base.

## What it gives a fleet

- **Memory with a lifecycle.** Not an append-only log: memories are superseded
  and archived, so what an agent recalls is what is currently true.
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

**Local-first.** One daemon process, one SQLite file, bound to loopback. No
external database, no required cloud service, and no outbound product
telemetry.

**Propose, don't act.** The gardener finds duplicates, staleness, and drift - and
proposes. Nothing rewrites your knowledge behind your back.

## Where to go next

| If you want to | Read |
|---|---|
| Get it running in ten minutes | [Quickstart](/quickstart/) |
| Wire it into Claude Code | [Claude Code setup](/claude-code/) |
| Wire it into Codex | [Codex local setup (app, CLI, and IDE)](/codex-cli/) |
| Understand the model before trusting it | [How Seamless works](/concepts/how-it-works/) |
| Point another MCP agent at it | [Integrate your agent](/guides/integrate-your-agent/) |
| Make a fleet divide work without colliding | [Coordinate multiple agents](/guides/coordinate-agents/) |
| Look up a tool, key, or command | [Reference](/reference/) |
| Fix something that is silently not working | [Troubleshooting](/guides/troubleshooting/) |
