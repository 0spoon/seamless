# Architecture

> The package layering, what each package owns, two data-flow traces through the real code, and the things Seamless deliberately does not have.

Seamless is one Go binary (`seamlessd`) plus a CLI (`seam`). Inside it, the code
is arranged so that the direction of every dependency is obvious from the package
name. This page is the map: the layers, who owns what, and how a write and a read
actually travel through them.

If you are looking for the conceptual model rather than the code, start at
[How Seamless works](https://thereisnospoon.org/docs/concepts/how-it-works/).

## The layering

```text
cmd/seamlessd, cmd/seam, cmd/docsgen
  │
  ▼  API surfaces - speak HTTP, own no domain logic
internal/{mcp, hooks, console}
  │
  ▼  domains - the business logic
internal/{retrieve, lifecycle, gardener, files, plans, capture, importer}
  │
  ▼  foundations - leaves, or nearly so
internal/{store, events, llm, markdown, core, config, validate}
```

Two rules make this hold, and both are load-bearing:

- **No package imports `cmd/`.** Wiring is the binary's job. A domain package that
  reached back into `cmd/` could not be tested without booting a daemon.
- **No domain package imports another's HTTP layer.** `mcp`, `hooks`, and
  `console` all sit at the same altitude and never call each other. When two of
  them need the same behavior, the behavior moves down a layer - which is why
  `lifecycle.Supersede` exists as a function rather than as a method on the MCP
  server. All three surfaces call it, so none of them owns it.

The layering is not enforced by a tool. It is enforced by the fact that a
violation is visible in the import block.

## Package responsibilities

| Package | Layer | Owns | Imports (internal) |
|---|---|---|---|
| `core` | foundation | Domain types and enums - `Project`, `Memory`, `Session`, `Task`, `Trial`, `Event`, `NewID()`. Pure data, no I/O. | - |
| `config` | foundation | One YAML file plus `SEAMLESS_*` env overrides; env wins over file, file over defaults. | - |
| `validate` | foundation | `Path`, `Name`, `Title` - the guards that stand between agent text and the filesystem. | - |
| `store` | foundation | SQLite: connection setup, migrations, FTS5, embeddings, and every query. Sessions, tasks (including the dependency-aware ready-queue and lease-based claims), trials, proposals, settings, and the retrieval stats live here. | `core`, `config` |
| `events` | foundation | The append-only event log - the single write path for the record of what happened - plus SSE fan-out to subscribers. | `core` |
| `llm` | foundation | Chat and embeddings across OpenAI (default), Ollama, and Anthropic, with the remote/local error taxonomy. | `config` |
| `markdown` | foundation | Rendering a body to HTML with raw HTML disabled and a bluemonday UGC policy on the output. | `core` |
| `files` | domain | The markdown layer: frontmatter parse/render, atomic writes, the watcher, startup reconciliation, and synchronous indexing of what it wrote. | `core`, `llm`, `store`, `validate` |
| `lifecycle` | domain | Supersession, archival, provenance. The only writer of `invalid_at`/`superseded_by`. | `core`, `files` |
| `retrieve` | domain | The briefing assembler, the prompt-context matcher, and recall/search (FTS5 + cosine, RRF-fused). | `config`, `core`, `llm`, `plans`, `store` |
| `gardener` | domain | The timed passes (dedup, staleness, digest, stale-plan) and request-driven interpretation. Writes proposals; never mutates a memory. | `config`, `core`, `events`, `files`, `lifecycle`, `llm`, `plans`, `store` |
| `plans` | domain | The captured-plan vocabulary: note-slug prefixes, the `plan-status` tag lifecycle, the tracking-task composition. One home so the tag spellings cannot drift. | `core`, `store` |
| `capture` | domain | SSRF-safe URL fetch: private-IP rejection, a pinned dialer, a port allowlist, redirect validation, a size cap. | - |
| `importer` | domain | One-way migration from the v1 store. Reads v1, writes v2, never modifies v1. | `core`, `files`, `store` |
| `mcp` | surface | The tool surface over streamable HTTP, plus per-connection session bindings and scope resolution. | `capture`, `core`, `events`, `files`, `gardener`, `lifecycle`, `llm`, `plans`, `retrieve`, `store`, `validate` |
| `hooks` | surface | Shared Claude Code/Codex hook endpoints and adapters, ambient sessions, bounded injection, findings harvest, and Claude-specific plan capture. | `config`, `core`, `events`, `files`, `plans`, `retrieve`, `store`, `validate` |
| `console` | surface | The server-rendered observability UI and its SSE feed. | `config`, `core`, `events`, `files`, `gardener`, `lifecycle`, `markdown`, `plans`, `retrieve`, `store` |

The import columns are the real ones, and they are the quickest way to check a
change: a foundation package that grows an edge to a domain package has inverted
the layering.

## Trace: `memory_write`, from tool call to file on disk

```text
agent ──▶ POST /api/mcp  (bearer key)
            │
            ▼  internal/mcp: handleMemoryWrite
      1. validate name / kind / description / body
      2. resolveWriteScope(project)          ← fails closed, never guesses
      3. resolveMemory(project, name)        ← update in place, or new ULID
      4. DedupHint (new names only)          ← advisory; errors swallowed
            │
            ▼  internal/files: Manager.WriteMemory
      5. ensurePathFree(relPath, id)         ← ErrPathOccupied, not a clobber
      6. suppress(relPath)                   ← the watcher ignores our own write
      7. Store.WriteMemory → RenderMemory → AtomicWrite   ◀── THE DURABLE ACT
      8. Indexer.IndexMemory                 ← memories_index upsert + FTS row
      9. embedItem                           ← best-effort; failure clears the hash
            │
            ▼  internal/mcp
     10. events.Record(memory.written)
     11. supersedes= → lifecycle.Supersede   ← only if asked
```

Five things in that path are decisions, not mechanics:

**Step 2 fails closed.** `resolveWriteScope` returns `errNoScope` when there is
no explicit project, no bound session, and no ambient session to inherit from.
The alternative - defaulting to global - puts a project's private knowledge in
front of every agent on the machine, silently. See
[Domain invariants](https://thereisnospoon.org/docs/internals/invariants/).

**Step 4 cannot fail the write.** `DedupHint` swallows every embedding error on
purpose. A dedup hint is advisory; a down embedder must not stop an agent from
recording what it learned.

**Step 5 refuses rather than overwrites.** A superseded memory keeps its
tombstone file at `memory/{project}/{name}.md`, so that name stays occupied. A
write that landed on it would destroy readable supersession history.
`memory_delete` is the only way to free the name.

**Step 7 is the whole point.** The file write is the durable act. Everything
after it - the index row, the FTS row, the vector - is rebuildable from disk by
`files.Reconcile` at startup. This is why the ordering is file first, index
second, and why the index is allowed to be best-effort while the file is not.

**Step 11 is a tool error, not a field in a success payload.** If the new memory
is written but superseding the old one fails, the call returns an error naming
the target that is *still active*. An error embedded in a success payload reads
as success to an agent, which would then leave two contradictory memories live.

## Trace: SessionStart, from Claude Code to injected briefing

```text
Claude Code SessionStart
    │  seam hook session-start  (stdin JSON → HTTP, bearer key)
    ▼
POST /api/hooks/session-start          internal/hooks: sessionStart
    │
    1. verifyBearer                     ← the ONLY non-2xx this endpoint returns
    2. ctx, cancel = WithTimeout(2s)    ← a slow store cannot stall the turn
    3. store.RegisterProjectForCWD      ← grow the repo→project map
    │
    ▼  internal/retrieve: Briefing
    4. effectiveBriefing                ← file/env base + console override row
    5. ResolveProjectForCWD             ← cwd → project slug
    6. familyMemoryScope                ← a child folds in its shared parent
    7. ActiveMemoriesForScope           ← WHERE invalid_at IS NULL
    8. partition: constraints | stages | index
       └─ subagent (agent_type set)? → constraints only, return
    9. trimMemoryIndex                  ← AFTER the partition: pinned kinds exempt
   10. findings, ready tasks, sibling findings/memories, plan rollups, pending plans
   11. assembleBriefing                 ← pack against the budget, then hard-cap
    │
    ▼  back in internal/hooks
   12. ensureAmbientSession             ← create or resume cc/{prefix}
   13. injectAmbientLine                ← appended before </seam-briefing>
   14. events.Record(retrieval.injected) ← records exactly what was sent
   15. 200 { continue, hookSpecificOutput.additionalContext }
```

The contract that shapes this path is **fail open**. Every error between steps 3
and 13 is logged and swallowed; the briefing degrades to empty and the agent
proceeds. Only a bad bearer key returns non-2xx. A memory system that can block
an agent from working is worse than no memory system, so the failure mode is
chosen deliberately: silence, not obstruction. The cost is that hook failure is
invisible, which is why `seamlessd doctor` and `seam doctor` exist - see
[Claude Code hooks](https://thereisnospoon.org/docs/reference/hooks/).

Two orderings inside it are also deliberate. Step 9 runs *after* step 8 so that
`constraint` and `stage` memories are never age-filtered or dropped for budget -
they are partitioned out before the trim ever sees them. And step 14 runs after
step 13 so the recorded event contains exactly the text the agent received,
ambient line included, rather than the briefing as it looked one step earlier.

## The deliberate no's

Each of these is a thing Seamless could have and does not.

**No CGO.** The database is `modernc.org/sqlite`, a pure-Go SQLite. `go build`
produces a static binary that cross-compiles and needs no toolchain on the target
machine. CGO would buy a faster driver at the price of a build that breaks
differently on every machine - a bad trade for a tool whose main virtue is that
it is one file you run locally.

**No vector database.** Embeddings are little-endian float32 BLOBs in the
`embeddings` table, and similarity is brute-force cosine in Go. There is no ANN
index and no second service. A personal knowledge store is thousands of items,
not billions; an exact scan over that is simple, has no index to rebuild, no
recall/latency tuning curve, and no separate process to keep alive. Adding
ChromaDB would double the number of things that must be running for recall to
work, in exchange for solving a scale problem nobody here has.

**No JWT, no users, no registration.** One static bearer key guards `/api/mcp`,
the hook endpoints, and the console, and the daemon binds `127.0.0.1`. The
security boundary is the loopback interface and the OS user account - the same
boundary that already protects `~/.ssh`. Sessions, refresh, and revocation are
the machinery of multi-user auth over a hostile network. There is no network here
and there is one user, so that machinery would be pure ceremony: more code, more
failure modes, and no threat it actually removes.

**No node, no npm, no React, no build step.** The console is `html/template`,
vanilla JS, and SSE. It exists so that an agent can edit a console page with the
Go toolchain it already has, and so `make build` produces the UI along with the
binary. A frontend build step would mean a second dependency tree, a second
lockfile, and a class of "works in dev, stale in prod" bugs - for a read-mostly
observability surface with no client-side state worth a framework.

**ULIDs, never UUIDs.** Every identity is a ULID via `core.NewID()`. ULIDs sort
lexicographically by creation time, so an id column is a timeline: `ORDER BY id`
is chronological, and a range scan over ids is a range scan over time. UUIDv4
throws that away for randomness this system does not need. (The console displays
the *last* 8 characters of an id, precisely because the leading characters are
the timestamp and recent ids all share them.)

## Where to go next

- [Domain invariants](https://thereisnospoon.org/docs/internals/invariants/) - the rules that plausible-looking
  code breaks.
- [Contributing](https://thereisnospoon.org/docs/internals/contributing/) - the `make check` gate and how to add
  a tool.
- [Storage layout](https://thereisnospoon.org/docs/reference/storage/) - what is on disk and what is in SQLite.
