---
title: Contributing
description: The make targets, the check gate, the conventions that matter, the forbidden APIs, and the three places a new MCP tool must be wired.
---

The conventions live in `AGENTS.md` at the repo root, and that file is the source
of truth. This page is the orientation: what to run, what the gate checks, and the
handful of rules that are worth knowing before you write a line.

## The targets

```bash
make build      # ./bin/seamlessd and ./bin/seam, with build metadata stamped in
make test       # unit tests
make test-race  # unit tests under the race detector
make vet        # go vet
make lint       # golangci-lint
make fmt        # gofmt, over tracked files
make fmt-check  # report gofmt drift instead of fixing it
make docs       # regenerate docs-src/ -> docs/docs/ (committed)
make docs-check # fail if the committed site is stale
make check      # the gate - everything above, in order
```

`make help` prints the full list, including the launchd dev-loop and prod-install
targets.

Note that `make build` is not the same as `go build`: it stamps the commit and
build date into the binary via `-ldflags`, and those show up in `/healthz`, the
MCP handshake, and the startup log. A plain `go build` leaves them `unknown`,
which makes a stale running daemon invisible.

## The `check` gate

```bash
make check
```

That is the one command that must be green before work is done. It runs six steps
sequentially - as separate `$(MAKE)` invocations rather than prerequisites, so it
stops at the first red step and stays ordered under `make -j`:

| Order | Step | Catches |
|---|---|---|
| 1 | `build` | It compiles. |
| 2 | `vet` | The stdlib's own suspicious-construct checks. |
| 3 | `fmt-check` | gofmt drift in tracked files. |
| 4 | `docs-check` | A committed docs site that no longer matches `docs-src/`. |
| 5 | `lint` | golangci-lint, including the repo's custom bans. |
| 6 | `test-race` | Everything, under the race detector. |

The order is cost-ascending: the cheapest and most-likely-to-fail steps run first,
and `test-race` is last because it is by far the slowest. The individual targets
exist for iterating; `check` is the thing you run before you claim to be done.

`docs-check` regenerates into a temp dir and diffs, rather than rewriting your
working tree and running `git diff`. That means it never mutates the file you are
editing, and `diff -r` catches untracked drift that `git diff` cannot see - a page
deleted from `docs-src/` but still committed under `docs/docs/`. Two docs pages
are generated from the code (the MCP tool reference reads `mcp.Catalog()`, the
configuration reference reflects `config.Defaults()`), so changing a tool or a
config key makes the committed output stale, and this is the step that says so.

## Use `make fmt`, never `gofmt -w .`

This one bites in a specific way, so it is worth stating plainly.

The go tool's `./...` pattern skips dot-directories, so `build`, `vet`, `test`,
and `lint` never see `.claude/worktrees/` - other agents' checkouts of this same
repo. **gofmt takes paths, not packages, and walks the filesystem raw.** So:

- `gofmt -l .` descends into those worktrees and reports *their* drift as yours.
- `gofmt -w .` **rewrites their files underneath them, mid-edit.**

`make fmt` and `make fmt-check` scope to tracked files (`git ls-files '*.go'`).
Always go through them.

## Style essentials

- **Go 1.25+, no CGO.** Pure-Go SQLite (`modernc.org/sqlite`). Prefer modern
  stdlib (`slices`, `maps`, `strings.SplitSeq`, iterators) in new code.
- **No emojis in code or comments. No attribution lines** in commits, PRs, or
  code.
- **Package layout is consistent** across `internal/`: `service.go` for the
  package's public API, `store.go` for SQLite access, `handler.go` for an HTTP
  surface, `{feature}.go` for pure functions, `*_test.go` alongside.
- **Interfaces are role nouns** - `Store`, `Recorder`, `Embedder`, not `IStore`.
- **Constructors are `New{Type}(deps) *Type`**, or `(*Type, error)` when they can
  fail.
- **Domain errors are package-level sentinels** (`var Err{Condition} =
  errors.New(...)`), checked with `errors.Is`.
- **IDs are ULIDs**, via `core.NewID()`. Never UUID.
- **Wrap errors with context and `%w`**: `fmt.Errorf("pkg.Service.Method: %w",
  err)`. Reserve `fmt.Errorf` for wrapping; sentinels come from `errors.New`.
- **Either log or return an error, never both.** Fatal to the operation → return
  it. Non-fatal (graceful degradation) → `slog.Warn`/`Debug` and continue.
- **`log/slog`, and never log memory or note bodies** - IDs only.
- **Every service and store method takes `context.Context` first.** Never
  `context.Background()` in a handler or in a goroutine spawned by one: derive
  from the request context, or use `context.WithoutCancel` for work that must
  outlive it.

## Database rules

- **WAL and foreign keys are set by Go on connection open**, never in migration
  SQL. One `seam.db` at `{data_dir}/seam.db`, with `SetMaxOpenConns(1)`.
- **Migrations are numbered SQL files** under `internal/store/migrations/`,
  embedded with `go:embed` and registered in the `Migrations()` list. Each runs
  once, inside a transaction, tracked in `schema_migrations`.
- **Never edit an applied migration.** Append a new numbered one.
- **A new `.sql` file needs both a `go:embed` line and a `Migrations()` entry**,
  or it silently never runs. This is the failure mode that looks like a working
  build and a mysteriously absent column.
- **Files are the source of truth** for `memory/` and `notes/`. The `*_index`
  tables and FTS are rebuildable mirrors, kept in sync by the files watcher and
  startup reconciliation; `content_hash` is what lets an unchanged file be
  skipped.
- **Embeddings are little-endian float32 BLOBs** in the `embeddings` table, and
  similarity is brute-force cosine in Go. Do not add a vector database.
- **The unified FTS5 table (`fts`) spans memories and notes** and is managed from
  the files layer with explicit INSERT/DELETE - not triggers, because it is not
  an external-content table.

## Testing rules

- **`testify/require`** (fail fast), table-driven for multi-case functions. This
  overrides any global "stdlib testing only" preference: the ported code and this
  project standardize on testify. Stay consistent.
- **SQLite tests use a fresh on-disk DB** in `t.TempDir()`, or a *named* in-memory
  DB (`file:{t.Name()}?mode=memory&cache=shared`). Never the unnamed shared-cache
  URI.
- **Migrations are exercised against a fresh DB.**
- **External services are mocked with `httptest.NewServer`.** A unit test never
  hits a real network service.
- **No `time.Sleep` for synchronization.** Channels, `sync.WaitGroup`, or polling
  with a deadline. This one is enforced by lint.
- **Tests are named `Test{Unit}_{Scenario}`**, or table-driven with `t.Run`.

## Forbidden APIs

Each row is a pattern that compiles, looks fine in review, and is wrong.

| Pattern | Why | Use instead |
|---|---|---|
| `ulid.MustNew` | Panics on entropy failure. | `core.NewID()` |
| `os.WriteFile` on a `.md` file | Non-atomic; a crash mid-write corrupts a source-of-truth file. | `files.AtomicWrite` (temp file in the same dir + fsync + rename) |
| `_, _ = time.Parse(...)` | Silently yields zero-value timestamps. | Capture the error, `slog.Warn`, do not emit zero times. |
| `err == ErrXxx`, `err == sql.ErrNoRows` | Breaks the moment the error is wrapped. | `errors.Is(err, ErrXxx)` |
| `_ = json.Marshal/Unmarshal(...)` | Marshal can fail; Unmarshal silently zeroes. | Check the error; warn and propagate. |
| Unchecked `RowsAffected()` on UPDATE/DELETE | Not-found looks like success, and a driver failure looks like not-found. | Check the error, *then* `if n == 0 { return ErrNotFound }`. |
| `close(ch)` outside `sync.Once` in `Close()` | Double-close panics. | `closeOnce.Do(func(){ close(done) })` |
| `a + "/" + b` for filesystem paths | Breaks portability and traversal guards. | `filepath.Join` |
| `context.Background()` in a handler/goroutine | Leaks request scope, disconnects shutdown. | Derive from the request ctx, or `WithoutCancel`. |
| `os.Stat`/`WalkDir` following symlinks across user data | Leaks files outside the tree. | `os.Lstat`; skip `ModeSymlink` in `WalkDir`. |
| `strings.Contains(err.Error(), ...)` for control flow | Fragile across message changes. | Typed sentinels + `errors.Is` |
| Returning `(nil, nil)` for a missing row | Callers forget the nil check. | Return `(nil, ErrNotFound)`. |
| New `migrations/NNN_*.sql` without a `Migrations()` entry | Silently never runs. | Add the `go:embed` + `Migrations()` entry; verify on a fresh DB. |
| `time.Sleep` for synchronization | Flaky tests; masks ordering bugs. | Channels / `WaitGroup` / deadline poll. |

`make lint` catches the grep-detectable subset: `forbidigo` bans `ulid.MustNew`
and `time.Sleep`, `rowserrcheck` catches a missing `rows.Err()`, `errorlint`
catches `err ==` sentinel comparisons, and `errcheck` runs with `check-blank: true`
so errors discarded into `_` are reported too.

That last one is the guardrail worth understanding. `n, _ := res.RowsAffected()`
in front of `if n == 0 { return ErrNotFound }` turns a driver failure into a
confident "not found" - the caller believes it, and there is no way to tell the
two apart afterwards. Every surviving discard is either listed in
`.golangci.yml`'s `exclude-functions` (structurally uninteresting: a deferred
`Tx.Rollback`, a write to an already-committed HTTP response) or carries a
`//nolint:errcheck` with a reason at the site. **There is no third category.** If
you add a discard, say why in one of those two places rather than reaching for a
blanket exclusion.

## Required patterns

- **Atomic markdown writes.** Every `.md` write goes through the files layer's
  atomic writer, including rollback paths.
- **DB-then-file ordering.** An operation touching both commits the DB
  transaction first, then performs the filesystem mutation in a post-commit step,
  and undoes partial filesystem state on rollback.
- **`rows.Err()` after every `for rows.Next()`.**
- **FTS5 MATCH sanitization** and **LIKE escaping** - see
  [Domain invariants](/internals/invariants/).
- **`filepath.Join` everywhere**, tests included.

## Adding an MCP tool

A tool has to be wired in **three** places inside `internal/mcp`, and each one is
guarded so that missing it fails a check rather than shipping quietly:

1. **`registerTools`** - the actual registration. Without it the server does not
   serve the tool.
2. **`ToolCount`** (in `server.go`) - bump it. `Server.NumTools()` counts what was
   registered, and `seamlessd doctor` asserts the two are equal, so a tool written
   but never wired in fails the doctor check.
3. **`Catalog()`** (in `catalog.go`) - add the tool's constructor, **in the same
   order as `registerTools`**. `Catalog` exists so `cmd/docsgen` can render the
   tool reference without constructing a `Server`: the constructors are plain data
   (name, description, input schema) and need no DB, config, or listening port.

`internal/mcp/catalog_test.go` enforces the parity, and it is a same-package test
so it can read the server's private `toolNames` - the registration record itself,
not a second hand-maintained list:

```go
require.Equal(t, srv.toolNames, names, "Catalog order/content must mirror registerTools")
require.Len(t, names, ToolCount)
```

A second test, `TestCatalogToolsAreDocumentable`, guards what docsgen renders: a
tool with no description would emit an empty docs section, and a duplicate name
would collide on the page anchor.

Two places outside `internal/mcp` also track the surface:

- **`cmd/seam/doctor.go`** has an `expectedTools` constant that mirrors
  `ToolCount` without importing the mcp package (which would pull its whole
  dependency tree into the CLI). `seam doctor` asserts the running server exposes
  that many tools via `tools/list`.
- **A docs page's `tools:` frontmatter list** (under `docs-src/reference/mcp/`)
  decides where the generated reference for the tool appears. A page listing a
  name that is not in `Catalog()` is a docsgen error.

## Before declaring done

1. `make check` is green.
2. `*_test.go` is updated in the same change if a signature changed. After any
   interface, store, schema, or migration change, run `make build && make test`
   and update mocks and fakes for changed signatures in that same change.
3. **Propagate every fix.** When you fix a buggy pattern, grep the repo for other
   instances and fix them all together.
4. **No fake results on error.** Never swallow an error and return a plausible
   dummy value - an agent cannot distinguish it from a real result. Return the
   error.
