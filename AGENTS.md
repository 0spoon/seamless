# AGENTS.md

Coding conventions for AI agents working in the Seamless repository. For the
project overview, structure, and commands, see `CLAUDE.md`.

## Go style rules

### General

- Go 1.25+. No CGO. Pure-Go SQLite driver (`modernc.org/sqlite`).
- Never use emojis in code or comments. Never include attribution/credit lines
  in commits, PRs, or code.
- Format with `gofmt`. No exceptions. Prefer modern stdlib (`slices`, `maps`,
  `strings.SplitSeq`, iterators) in new code.

### Package structure

Each domain package under `internal/` follows a consistent layout:

- `service.go` -- business logic (the public API of the package)
- `store.go` -- SQLite data access
- `handler.go` -- HTTP handlers (only for packages with an HTTP surface)
- `{feature}.go` -- pure functions (parsers, ranking, frontmatter, etc.)
- `*_test.go` -- tests alongside source

Strict layering, no circular imports. `cmd/` wires everything; no package imports
`cmd/` and no domain package imports another's HTTP layer:

```
cmd/seamlessd, cmd/seam
  -> internal/{mcp,hooks,console}         (API surfaces)
    -> internal/{retrieve,lifecycle,gardener,tasks,files,capture}  (domains)
      -> internal/{store,events,llm,core,config,validate}          (foundations)
```

### Naming

- Interfaces: role nouns (`Store`, `Recorder`, `Embedder`), not `IStore`.
- Constructors: `New{Type}(deps) *Type` (or `(*Type, error)` when it can fail).
- Domain errors: package-level `var Err{Condition} = errors.New(...)` sentinels,
  checked with `errors.Is`.
- Tests: `Test{Unit}_{Scenario}` or table-driven with `t.Run`.
- IDs: ULID everywhere (`github.com/oklog/ulid/v2`) via `core.NewID()`. Never UUID.

### Error handling

- Wrap with context and `%w`: `fmt.Errorf("pkg.Service.Method: %w", err)`.
- Sentinel errors via `errors.New`; reserve `fmt.Errorf` for wrapping (with `%w`).
- Either log or return an error, never both. Fatal to the operation -> return it;
  non-fatal (graceful degradation) -> `slog.Warn`/`Debug` and continue.

### Logging & context

- `log/slog` (stdlib). INFO for lifecycle, WARN for recoverable, ERROR for
  failures, DEBUG for per-request. Never log memory/note bodies -- IDs only.
- All service/store methods take `context.Context` first. Thread it through.
  Never `context.Background()` in a handler or a goroutine spawned by one; derive
  from the request ctx, or `context.WithoutCancel` for work that outlives it.

### Database

- SQLite with WAL mode and foreign keys ON, set by Go on connection open (never
  in migration SQL). Single `seam.db` at `{data_dir}/seam.db`. `SetMaxOpenConns(1)`.
- Migrations: numbered SQL files under `internal/store/migrations/`, embedded via
  `go:embed` and registered in the `Migrations()` list. Each runs once, inside a
  transaction, tracked in `schema_migrations`. NEVER edit an applied migration;
  append a new numbered one. Adding a `.sql` file requires a matching `go:embed`
  line and `Migrations()` entry, or it silently never runs.
- Files are the source of truth for durable knowledge (memory/, notes/). The
  `*_index` tables and FTS are rebuildable mirrors kept in sync by the files
  watcher + startup reconciliation; use `content_hash` to skip unchanged files.
- Embeddings live in the `embeddings` table as little-endian float32 BLOBs.
  Similarity is brute-force cosine in Go -- do NOT add a vector database.
- Unified FTS5 (`fts`) spans memories and notes; it is managed from the files
  layer (explicit INSERT/DELETE), not triggers, because it is not external-content.

### Testing

- `testify/require` (fail fast), table-driven for multi-case functions. (This
  overrides the global "stdlib testing only" preference -- the ported v1 code and
  this plan standardize on testify; stay consistent.)
- SQLite tests use a fresh on-disk DB in `t.TempDir()` or a named in-memory DB
  (`file:{t.Name()}?mode=memory&cache=shared`) -- never the unnamed shared-cache URI.
- Migrations are exercised against a fresh DB in tests.
- External services (Ollama, OpenAI) are mocked with `httptest.NewServer`. Never
  hit a real network service in a unit test.
- No `time.Sleep` for synchronization. Use channels, `sync.WaitGroup`, or polling
  with a deadline.

### Security invariants

- Path traversal: reject any relative path with `..`, absolute paths, or null
  bytes (`internal/validate.Path`). Names that become filenames go through
  `validate.Name`; human titles through `validate.Title`.
- Auth: a single static bearer key guards `/api/mcp` and the console. Bind
  `127.0.0.1` by default. No JWT, no multi-user, no registration.
- SSRF: URL capture must reject private IPs, localhost, and `file://`.
- Every JSON/body handler starts with `http.MaxBytesReader`.

## Common pitfalls (checklist before declaring done)

### Meta-rules

1. **Propagate every fix.** When you fix a buggy pattern, grep the repo for other
   instances and fix them all in the same change.
2. **After any interface/store/schema/migration change, run `make build && make test`.**
   Update mocks/fakes for changed signatures in the same change.
3. **No fake results on error.** Never swallow an error and return a plausible
   dummy value; the LLM cannot distinguish it from a real result. Return the error.

### Forbidden APIs

| Pattern | Why | Use instead |
| --- | --- | --- |
| `ulid.MustNew` | Panics on entropy failure. | `core.NewID()` (`ulid.New(ulid.Now(), rand.Reader)` + return the error). |
| `os.WriteFile` on a `.md` file | Non-atomic; a crash mid-write corrupts the source-of-truth file. | `files.AtomicWrite` (temp file in same dir + fsync + rename). |
| `_, _ = time.Parse(...)` (discarded error) | Silently yields zero-value timestamps. | Capture the error, `slog.Warn`, do not emit zero times. |
| `err == ErrXxx`, `err == sql.ErrNoRows` | Breaks when wrapped. | `errors.Is(err, ErrXxx)`. |
| `_ = json.Marshal/Unmarshal(...)` | Marshal can fail; Unmarshal silently zeroes. | Check the error; warn and propagate. |
| Unchecked `RowsAffected()` on UPDATE/DELETE | Not-found looks like success. | Check `n`; `if n == 0 { return ErrNotFound }`. |
| `close(ch)` outside `sync.Once` in `Close()` | Double-close panics. | `closeOnce.Do(func(){ close(done) })`. |
| `a + "/" + b` for filesystem paths | Breaks portability/traversal. | `filepath.Join`. |
| `context.Background()` in a handler/goroutine | Leaks request scope, disconnects shutdown. | Derive from request ctx / `WithoutCancel`. |
| `os.Stat`/`WalkDir` following symlinks across user data | Leaks files outside the tree. | `os.Lstat`; skip `ModeSymlink` in `WalkDir`. |
| `strings.Contains(err.Error(), ...)` for control flow | Fragile across message changes. | Typed sentinels + `errors.Is`. |
| Returning `(nil, nil)` when a row is missing | Callers forget the nil check. | Return `(nil, ErrNotFound)`. |
| New `migrations/NNN_*.sql` without a `Migrations()` entry | Silently never runs. | Add the `go:embed` + `Migrations()` entry; verify on a fresh DB. |
| `time.Sleep` for synchronization | Flaky tests, masks ordering bugs. | Channels / `WaitGroup` / deadline poll. |

### Required patterns

- **Atomic markdown writes**: every `.md` write goes through the files layer's
  atomic writer (temp + fsync + rename), including rollback paths.
- **DB-then-file ordering**: operations touching both commit the DB transaction
  first, then perform the filesystem mutation in a post-commit step; undo partial
  filesystem state on rollback.
- **`rows.Err()` after every `for rows.Next()`** (enforced by `rowserrcheck`).
- **FTS5 MATCH sanitization**: user text fed to `MATCH` is sanitized (strip
  operators, quote terms) to avoid SQLite errors and injection.
- **LIKE escaping**: user input in `LIKE` escapes `\`, `%`, `_` in that order, with
  `ESCAPE '\'`; never apply LIKE-escaping to `=` comparisons.
- **`filepath.Join` everywhere**, including tests.

## Verification before declaring done

1. `make build && make test` (with `*_test.go` updates if a signature changed).
2. `make lint` (catches `ulid.MustNew`, `time.Sleep`, missing `rows.Err()`,
   blank-discarded errors, `err ==` sentinel comparisons).
3. For any change touching a recurring pattern above, grep for siblings and fix
   them together.

## Console (server-rendered)

The observability console is `html/template` + vanilla JS + SSE, served by
`internal/console` -- no node, npm, React, or build step. It is read-mostly (the
one write is archiving a memory). Keep pages self-contained and dependency-free so
an agent can edit them without a toolchain.
