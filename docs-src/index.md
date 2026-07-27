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

## Which agent do you run?

Setup is client-shaped: pick yours and that page walks install, wiring, and
verification end to end. The [Quickstart](/quickstart/) is the same path for
everyone - one install command, then your client.

<!-- Raw HTML, so hrefs are relative to the docs home (rewriteDocLinks only
     rewrites markdown links). This page renders at /docs/, one level deep. -->
<div class="card-grid">
  <a class="doc-card" href="claude-code/">
    <h2>Claude Code</h2>
    <p>The terminal CLI and the Claude app's code sessions - one setup covers both.</p>
  </a>
  <a class="doc-card" href="claude-app/">
    <h2>Claude app chat</h2>
    <p>Chat conversations in the desktop app - MCP only, no hooks, wired through the app's own config.</p>
  </a>
  <a class="doc-card" href="codex-cli/">
    <h2>Codex</h2>
    <p>CLI, desktop app, and IDE extension - one shared local profile named codex.</p>
  </a>
  <a class="doc-card" href="guides/mcp-clients/">
    <h2>Any other MCP client</h2>
    <p>Cursor, Cline, Windsurf, Zed, or an agent you wrote - point it at the MCP endpoint.</p>
  </a>
</div>

## Or start from the task

| If you want to | Read |
|---|---|
| Install from zero in ten minutes | [Quickstart](/quickstart/) |
| Update, uninstall, or add a client later | [Update & uninstall](/updating/) |
| Control the service, find logs and paths | [The service & where things live](/reference/service/) |
| Understand the model before trusting it | [How Seamless works](/concepts/how-it-works/) |
| Point a hand-rolled agent at it | [Integrate your agent](/guides/integrate-your-agent/) |
| Make a fleet divide work without colliding | [Coordinate multiple agents](/guides/coordinate-agents/) |
| Look up a tool, key, or command | [Reference](/reference/) |
| Fix something that is silently not working | [Troubleshooting](/guides/troubleshooting/) |
