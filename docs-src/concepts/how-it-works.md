---
title: How Seamless works
description: One daemon, three surfaces, and a hard line between the files that are truth and the database that is an index.
---

Seamless is one Go binary holding one SQLite database and one directory of
markdown files, bound to loopback on your machine. Everything below follows from
that.

## One daemon, three surfaces

```text
                    ┌──────────────────────────┐
   agents  ────MCP──▶                          │
                    │        seamlessd         │──▶  ~/.seamless/memory/*.md
   hooks   ───HTTP──▶   (one process, :8081)   │──▶  ~/.seamless/notes/*.md
                    │                          │──▶  ~/.seamless/seam.db
   you     ─browser─▶                          │
                    └──────────────────────────┘
```

- **Agents** speak MCP at `/api/mcp`. This is the primary interface; the clients
  of Seamless are programs, not people.
- **Hooks** are how sessions become ambient. Claude Code calls them at session
  start, on each prompt, and at session end, so an agent gets briefed and
  harvested without ever choosing to.
- **You** get a read-mostly console. It is an observability surface, not the way
  the system is driven.

One instance per machine, one port, one data directory. Not a distributed system,
by choice.

## Files are truth; the database is an index

This is the load-bearing decision in the whole design.

| | Lives in | Why |
|---|---|---|
| Memories, notes | **Markdown files** | Durable knowledge you own: readable, greppable, editable, git-able |
| Sessions, tasks, trials, events, telemetry | **SQLite** | High-churn state with no reason to be a file |
| FTS index, embeddings | **SQLite** | Derived data, rebuildable from the files |

So: **what would you lose if `seam.db` were deleted?** The sessions, the task
queue, the trials, and the event log - the record of what *happened*. You would
not lose a single memory or note, because those are files, and startup
reconciliation rebuilds their index from the disk.

That asymmetry is the point. The knowledge is yours in the strongest sense: it
survives this program. If Seamless disappeared tomorrow, you would still have a
folder of markdown files that a human - or any other tool - can read.

## The write path

```text
memory_write
   │
   ├─▶ validate name, scope (fail closed if ambiguous)
   ├─▶ write the markdown file       ← the durable act
   ├─▶ index it: FTS row + embedding
   └─▶ append an event
```

The file write is the one that matters. Indexing after it can fail and be
rebuilt; the file is already on disk.

## The read path

```text
SessionStart hook ─▶ resolve cwd → project ─▶ assemble briefing (budgeted) ─▶ inject
UserPromptSubmit  ─▶ match prompt against memories ─▶ inject <seam-recall>
recall            ─▶ FTS5 + cosine ─▶ RRF fuse ─▶ budget ─▶ return
```

All three are covered in [Recall](/concepts/recall/) and
[Sessions & briefings](/concepts/sessions/).

## What Seamless is not

- **Not a RAG pipeline over your codebase.** It stores what agents *learned*, not
  what the code says. The code is already in the repo; duplicating it into memory
  is how a store fills with noise.
- **Not a vector database.** Vectors are float32 blobs in SQLite, scanned
  exactly. No ANN index, no separate service.
- **Not a cloud service.** No account, no sync, no telemetry. The bind is
  loopback and the key is static.
- **Not autonomous.** The gardener proposes; you decide. Nothing rewrites your
  knowledge behind your back.
- **Not a chat memory.** It is not trying to remember your conversation. It
  remembers decisions, constraints, and dead ends across many agents and many
  months.
