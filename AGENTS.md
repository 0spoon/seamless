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

## Domain invariants

Rules that plausible-looking code breaks. Each is enforced somewhere specific;
the pointer is where to look, not a substitute for reading it.

### Supersession (`internal/lifecycle`)

- `invalid_at` is the ONLY authoritative "has left the indexes" predicate
  (`core.Memory.Active()` is `InvalidAt == nil`; SQL filters `WHERE invalid_at
  IS NULL`). Never infer live-ness from an empty `superseded_by` -- an archived
  memory has `invalid_at` set and `superseded_by` empty, and is equally gone.
- `lifecycle.Supersede`/`Archive` are the only writers of
  `invalid_at`/`superseded_by` in non-test code; MCP, gardener, and console all
  route through them. Do not stamp those fields by hand.
- `valid_from` is never touched by supersession: `[valid_from, invalid_at)` is
  the memory's bi-temporal record.
- Stamped exactly once, guarded on the way in: already-invalid `old` ->
  `ErrAlreadyInvalid`, `old.ID == replacement.ID` -> `ErrSelfSupersede`,
  invalid/empty replacement -> `ErrInvalidReplacement`. Acyclicity falls out of
  those guards (every edge points invalid -> active, and nothing clears
  `invalid_at`); there is no cycle traversal to lean on.
- Pass the FULL body. Both functions append a tombstone to `old.Body` and
  rewrite the whole file, so handing them an index row -- which carries no body
  -- truncates the memory to just the tombstone. Re-read the file first.
- A superseded name stays occupied: reviving it is `ErrPathOccupied`, never a
  silent clobber. `memory_delete` is the only escape hatch, and it drops history.
- A supersede that fails after the new memory is written is a tool ERROR naming
  the still-active target, never a success payload with an error inside it (F9).

### Session binding and scope (`internal/mcp`)

- Never read `project` from tool args directly. Route it through
  `resolveReadScope` (nothing to infer -> global) or `resolveWriteScope`
  (nothing to infer -> `errNoScope`). They apply `validateProjectArg`, which is
  the path-traversal defense: a project slug becomes a directory under
  `memory/`/`notes/`, and the data-dir boundary check alone does not catch
  `../notes/inbox`.
- A durable create uses `resolveWriteScope` -- it fails closed. Reaching for
  `resolveReadScope` on a write silently lands it in global.
- Precedence: explicit `project` -> the bound session's project -> the sole
  unambiguous ambient. Ambients spanning more than one project are
  `errAmbiguousScope`, not a guess.
- Only `session_start` binds a connection (keyed by the MCP client session id).
  Bindings evict on `session_end` and via the opportunistic sweep.
- A lost binding does NOT error -- the call degrades to the ambient fallback.
  Never assume the binding you started with is still there.
- Stamp provenance with `s.boundSession(ctx)`, not the raw binding.
- A new tool must be registered in `registerTools` AND bump `ToolCount`, or
  `doctor` fails its tool-count assertion.

### FTS5 and LIKE escaping (`internal/store`)

- User text reaching `MATCH` goes through `ftsQuery` (`fts.go`): it splits on
  non-alphanumerics, drops single-rune tokens, quotes each term and ORs them, so
  `chroma-boot-race` is three literal terms rather than a subtraction. Never
  build a MATCH expression by concatenation.
- `ftsQuery` returning `""` means "no usable token"; callers treat that as no
  results, never as an unfiltered query.
- User text in `LIKE` goes through `escapeLikePrefix` (`\`, `%`, `_`, in that
  order, with `ESCAPE '\'`). Never LIKE-escape a value compared with `=`.

### LLM degradation: remote vs local (`internal/llm`, `internal/retrieve`)

- Remote -- `ErrUnavailable`, `ErrAuth`, `ErrRateLimited`: the provider answered
  badly or not at all and may recover. DEGRADE; `recall` drops to lexical-only,
  which is honest.
- Local -- `ErrConfig`: the request never got built. No provider was contacted,
  no retry helps, it will not clear. SURFACE it -- degrading trades one loud
  failure for quietly worse recall for the life of the daemon.
- Classify at the `do` call sites via `doErr(op, err)`. Never hand-wrap a
  request-build failure as `ErrUnavailable`.
- `base_url` is validated in `NewEmbedder`/`NewChatClient` (the single
  construction points) because `url.Parse` accepts a bare host:
  `"api.openai.com/v1"` builds a perfectly valid request and only fails inside
  `Do` as an opaque transport error indistinguishable from an outage. That
  validation is why `ErrConfig` should be unreachable at request time -- which
  is exactly why it must not hide when reached.
- `DedupHint` and `files.embedItem` swallow every embed error on purpose (dedup
  is advisory and must never block `memory_write`; indexing is best-effort with
  a hash-retry). Leave them.

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
| Unchecked `RowsAffected()` on UPDATE/DELETE | Not-found looks like success, and a driver failure looks like not-found. | Check both: the error, then `if n == 0 { return ErrNotFound }`. (`errcheck`) |
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

1. **`make check`** -- build + vet + fmt-check + lint + test-race, in that order.
   This is the gate; the individual targets exist for iterating.
2. Update `*_test.go` in the same change if a signature changed.
3. For any change touching a recurring pattern above, grep for siblings and fix
   them together.

`make lint` catches `ulid.MustNew`, `time.Sleep`, missing `rows.Err()`,
`err ==` sentinel comparisons, and -- via `errcheck` with `check-blank` --
errors discarded into `_`. That last one is the guardrail for the meta-rule
above: `n, _ := res.RowsAffected()` in front of an `if n == 0 { return
ErrNotFound }` turns a driver failure into a confident "not found".

Every surviving discard is either listed in `.golangci.yml`'s
`exclude-functions` (structurally uninteresting: deferred `Tx.Rollback`, writes
to an already-committed HTTP response) or carries a `//nolint:errcheck` with a
reason at the site. There is no third category -- if you add a discard, say why
in one of those two places rather than reaching for a blanket exclusion.

Note for anyone touching gofmt: the go tool's `./...` skips dot-dirs, but gofmt
walks the filesystem, so a bare `gofmt -l .` descends into `.claude/worktrees/`
(other agents' checkouts) and reports their drift as yours -- and `gofmt -w .`
rewrites their files mid-edit. `make fmt` and `make fmt-check` scope to tracked
files; use them.

## Console (server-rendered)

The observability console is `html/template` + vanilla JS + SSE, served by
`internal/console` -- no node, npm, React, or build step. It is read-mostly (the
one write is archiving a memory). Keep pages self-contained and dependency-free so
an agent can edit them without a toolchain.
