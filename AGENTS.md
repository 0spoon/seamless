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
cmd/seamlessd, cmd/seam, cmd/seambench, cmd/demoseed, cmd/docsgen
  -> internal/{mcp,hooks,console}         (API surfaces)
    -> internal/{retrieve,lifecycle,gardener,files,capture,bench,demokit} (domains)
      -> internal/{store,events,llm,core,config,validate}          (foundations)
```

(The dependency-aware ready-queue and lease-based claiming live in
`internal/store/tasks*.go`; there is no `internal/tasks` package.)

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

### Mutating a corpus file (`internal/files`)

- Every read-modify-write of a memory or note goes through
  `files.Manager.Mutate(path, fn)` / `MutateMemory` / `MutateNote`. Reading an
  item, editing the struct, and writing the whole file back is a lost update:
  two racers read the same starting content and the second rename wins, with no
  error anywhere -- the index upserts by id, so even `UNIQUE file_path` stays
  quiet and the loser is told it succeeded.
- The invariant is not "the write is locked", it is that **the read feeding the
  write happens inside the same lock**. Wrapping only the write fixes nothing
  and looks exactly like a fix.
- The lock is per path and NOT reentrant. `WriteMemory`/`WriteNote`/`Remove`
  deliberately take no lock, so calling them from inside `fn` is the point.
  `MutatePaths` locks several files in sorted order for a mutation spanning two
  of them (a note changing project); sorted order is the deadlock discipline.
- A callback returning `files.ErrNoChange` skips the write and reports the
  loaded item -- that is how an already-correct favorite flip avoids rewriting
  and re-indexing the file.
- `expect_hash` is compared against the FILE's hash read inside the lock, never
  the index row: the watcher re-indexes on a 300ms debounce, so the index
  confirms the stale hash for exactly the window the precondition exists to
  refuse.
- Edit vs supersede is a LIFECYCLE boundary, not a convenience call. `memory_edit`
  is for changes carrying no new claim (typo, formatting, stale path, stage
  `Status` flip, metadata); a changed MEANING goes through `memory_write`
  `supersedes` so the old memory retires into readable history. Both edit tools
  and `MCPInstructions` render `agentguide.EditVsSupersede` so the rule cannot
  drift into two versions.

### Session binding and scope (`internal/mcp`)

- Never read `project` from tool args directly. Route it through
  `resolveReadScope` (nothing to infer -> global) or `resolveWriteScope`
  (nothing to infer -> `errNoScope`). They apply `validateProjectArg`, which is
  the path-traversal defense: a project slug becomes a directory under
  `memory/`/`notes/`, and the data-dir boundary check alone does not catch
  `../notes/_global`.
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

### Benchmark scenarios and graders (`internal/bench`, `cmd/seambench`)

The agent-scenario benchmark produces a NUMBER the owner acts on, so its
invariants are about the number meaning the same thing on every arm and in six
months. Workflow and flags: `cmd/seambench/README.md`. (`make seambench`, not
`make bench` -- the latter is the unrelated ns/op micro-benchmarks.)

- **The run directory is the whole handoff.** `run` writes artifacts and grades
  nothing; `report` reads artifacts and runs nothing. A grader takes a
  `RunArtifacts` whose every path points inside one preserved run dir -- never a
  live arm, never the runner's process state. Keep it that way: it is what makes
  `report --regrade` re-apply a grader fix to runs that already cost their
  tokens, and what lets a run tree be graded on another machine. The layout is
  frozen in `internal/bench/artifacts.go`; adding a file is fine, repurposing one
  breaks the other half. Never read a run's coordinates out of its path -- the
  `run.json` manifest carries them (a version comparison nests one level deeper).
- **`Metrics` has two disjoint halves.** The runner owns turns/tokens/cost/
  duration; the grader owns everything derived from the artifacts.
  `MergeMetrics` recombines them FIELD-WISE, not "whichever is non-zero", so an
  agent that genuinely made no tool calls stays at zero.
- **Gate vs observed.** Every check reports; only a `gate: true` check can fail
  the run. Outcome checks (the repo-state assertions) gate on every arm. Of the
  event-log checks, only DEFECTS gate -- the mechanism failing to fire at all,
  or a durable write landing outside the scenario's project. "The agent read the
  memory / moved the plan step / left a finding" is recorded and measured but
  MUST NOT gate: gating it would fail a Seamless arm that solved the task while
  the vanilla arm doing the same thing passes, which understates the very uplift
  being measured. The LLM judge never gates; its absence degrades the run
  (constraint `llm-degradation-remote-vs-local`), it does not fail it.
- **Only the condition dimension may run concurrently.** The N runs of a cell
  share an arm and re-establish its starting state (git-restore + wipe and
  re-seed) on every run, so overlapping them would reset each other mid-flight;
  scenarios stay serial so a systemic failure surfaces after one scenario rather
  than after the whole token-metered matrix. Condition arms own separate repos,
  data dirs, HOMEs and ports, which is why `--parallel-conditions` is safe --
  and why a run made under it is stamped `Concurrent`, since its wall clock
  carries contention the serial runs it would be compared against do not.
- **Three outcomes, never two.** `graded` / `failed to run` / `ungradeable`.
  Only graded runs enter a pass-rate or a trial verdict; an infrastructure flake
  recorded as a `fail` is how a later reader mistakes it for a regression. An
  unrecognized status falls to ungradeable, not to a verdict. Rates are
  `bench.Rate` (passed + graded) so a rate can never be quoted without its
  counts.
- **Never string-match briefing layout.** Not in a grader, not in a seed test.
  The briefing is the fastest-churning surface in the repo AND is itself prime
  regression surface for this benchmark, so a layout assertion both breaks
  constantly and measures the wrong thing. Assert DB rows, event-log entries,
  and stable markers instead: memory names, tool names, task ids, session
  findings, project scope. `internal/bench/eventlog.go` is the vocabulary, and
  the seed tests assert through store queries and files on disk.
- **Seeds write to the data dir, never into the demo repo working tree.** The
  repo must stay Seamless-free (memory `scene-demo-repo-must-be-seamless-free`)
  or the vanilla arm reads its way out of character; the `full` arm's CLAUDE.md
  block is the one deliberate exception and the harness writes it. A `SeedFunc`
  gets a `*demokit.Seeder` already opened on a throwaway dir -- never point
  `demokit.New` at a live instance.
- **Bench scenario fixtures are FORKED from `internal/demokit`'s scene specs, not
  imported.** The scene defs are branding surface that must stay stable while
  this suite churns. Reuse demokit's seeding PRIMITIVES; copy its data.
- **The arm env file is the only interface to the shell harness.** `harness.sh
  --mode bench` owns what an arm contains (throwaway HOME, demo-repo copy, data
  dir, config, key, port, `install-hooks --client claude` under the arm's own
  HOME); the runner reads that env file and reimplements none of it -- in
  particular it never runs `install-hooks` itself, which is what keeps the live
  `~/.claude`/`~/.codex`/`~/.seamless` out of reach (memory
  `fixture-install-hooks-needs-home-override`). The condition grammar
  `name[:profile[:client]]` is parsed on both sides on purpose; a mismatch
  between the requested and the built arm is fatal, not adaptive.
- **One live write, and it is the results trial.** Everything else seambench
  touches is throwaway. `results.json` is written BEFORE any trial, an
  unreachable instance is a warning with a retry line rather than an error, and
  recording is idempotent per run (the trial id is stamped into `grade.json`).
  Do not add a second live write, and pass `--no-trials` for dry runs -- fake-agent
  results in the live lab are indistinguishable from measurements later.

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
| Bare `default:` in a switch over caller-supplied input | The unrecognized value silently becomes the default -- a plausible dummy the caller cannot tell from a real result (meta-rule 3). `recall{scope:"memoires"}` searched everything and reported success. | Validate at the boundary and name the accepted values: `invalid %q: valid values are %s`. A *library* may keep a permissive default where the zero value legitimately means "unset" -- only the boundary can tell absent from wrong. |
| `mcp.Enum(...)` / `mcp.Required()` as the only guard | Advertisement, not enforcement: mcp-go's `handleToolCall` looks the tool up and dispatches with no schema pass. The declaration reads like a check and is not. | Declare via `enumOf(canonicalSet)`; `validateMiddleware` (`internal/mcp/argspec.go`) is what enforces it. |
| Hand-transcribing a canonical set into a schema, help string, or prompt | Drifts in silence. `tools_gardener.go` once listed all of `store.ProposalKinds` but one, so `abandon_plan` was undiscoverable for months while working fine everywhere else. | Derive from the canonical slice (`core.MemoryKinds`, `core.TaskStatuses`, `core.SessionSources`, `store.ProposalKinds`, `retrieve.RecallScopes`) + a same-package test asserting schema == set (`catalog_test.go`'s `TestArgsEnumsDeriveFromCanonicalSets`). |

### Required patterns

- **A default is only legitimate when a parameter is ABSENT, never when it is
  present-but-uninterpretable.** Absent -> default; present but uninterpretable ->
  error. One sentence covering every finding in the input-boundary sweep: a typo'd
  enum, a wrong JSON type, a misspelled parameter name, and `limit:0` are all
  *present*, so none of them may quietly become the default. This is the owner's
  standing "no automatic fallbacks for ambiguous requests" directive applied to
  arguments, and it is why `resolveWriteScope` refuses to guess a project rather
  than picking one.
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

1. **`make check`** -- build + vet + fmt-check + docs-check + installer-check +
   site-check + lint + vulncheck + test-race, in that order. This is the gate;
   the individual targets exist for iterating. `make seambench` is NOT part of
   it and must never be added: it spends real API tokens (see the benchmark
   section above).
2. Update `*_test.go` in the same change if a signature changed.
3. For any change touching a recurring pattern above, grep for siblings and fix
   them together.

`make install-git-hooks` (once per clone) enables `.githooks/pre-commit`, which
runs **`make check-fast`** -- `check` minus build, vulncheck, and test-race (~3s
against ~39s). It is a
convenience, not the gate: git runs hooks against the working tree rather than
the index, so under partial staging it describes the tree you are in and not the
commit you are making. It catches gofmt/docs/lint drift early; `make check` and
CI still decide whether the work is done. Bypass with `git commit --no-verify`.

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
`internal/console` -- no node, npm, React, or build step. It is read-mostly: the
writes are the owner's overrides and curation actions -- archive a memory, approve
a plan, force-release a task's claim lock, ask/split/apply/dismiss/retarget a
gardener proposal, and save or reset the briefing settings. Keep pages
self-contained and dependency-free so an agent can edit them without a toolchain.
