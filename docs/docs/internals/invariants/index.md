# Domain invariants

> The rules plausible-looking code breaks - supersession, scope resolution, FTS and LIKE escaping, LLM degradation - and why each exists.

These are the rules that reasonable code violates. Every one of them has a
sensible-looking alternative that compiles, passes review, and is wrong; each is
enforced somewhere specific, and the pointer here is where to look, not a
substitute for reading it.

## Supersession

Enforced in `internal/lifecycle`. Concepts: [Memory & notes](https://thereisnospoon.org/docs/concepts/memory/).

### `invalid_at` is the only authoritative "has left the indexes" predicate

`core.Memory.Active()` is `InvalidAt == nil`; SQL filters `WHERE invalid_at IS
NULL`.

**Never infer live-ness from an empty `superseded_by`.** An archived memory has
`invalid_at` set and `superseded_by` empty - it is equally gone, and it does not
point anywhere because nothing replaced it. Code that reads "no replacement,
therefore still valid" resurrects every archived memory into briefings and recall
at once, which is exactly the class of thing nobody notices until an agent acts on
a retired constraint.

### `lifecycle.Supersede` and `lifecycle.Archive` are the only writers

They are the only non-test writers of `invalid_at` and `superseded_by`. MCP, the
gardener, and the console all route through them. Do not stamp those fields by
hand.

They exist as one path because a supersession is not a field update: it stamps the
old memory, appends a tombstone to its body, rewrites its file, and updates the
index. Every hand-rolled version of that has been a subset of it.

### `valid_from` is never touched by supersession

`[valid_from, invalid_at)` is the memory's bi-temporal record: when it started
being believed, and when it stopped. Moving `valid_from` on supersession would
erase the first half and, with it, the ability to ask what the store believed at
some past moment.

### It is stamped exactly once, guarded on the way in

The guards are:

| Condition | Error |
|---|---|
| `old` is already invalid | `ErrAlreadyInvalid` |
| `old.ID == replacement.ID` | `ErrSelfSupersede` |
| replacement is invalid or empty | `ErrInvalidReplacement` |

**Acyclicity falls out of those guards** rather than out of any traversal. Every
edge points from an invalid memory to an active one, and nothing ever clears
`invalid_at` - so no cycle can form. There is no cycle check to lean on, because
there is nothing for one to find. Weaken a guard and you do not get a slow cycle
check; you get cycles.

### Pass the FULL body

Both functions append a tombstone to `old.Body` and rewrite the whole file.
Handing them an index row - which carries no body - truncates the memory to just
the tombstone. Re-read the file first.

This is the invariant most likely to be broken by code that looks correct: the
index row has every field the signature wants, so it type-checks, writes
successfully, and destroys the memory's content.

### A superseded name stays occupied

Reviving it is `ErrPathOccupied`, never a silent clobber. The superseded memory's
tombstone file still sits at `memory/{project}/{name}.md`, and overwriting it
would destroy the readable history that supersession exists to preserve.
`memory_delete` is the only escape hatch, and it drops that history - which is
why it is the escape hatch and not the default.

### A failed supersede is a tool ERROR, not a payload

When `memory_write` writes the new memory but the supersession fails, the call
returns an error naming the still-active target. It never returns a success
payload with an error nested inside it.

The reason is what an agent does with each. An error field inside a success
payload reads as success: the agent moves on, believing the old memory is retired,
and the store now serves two contradictory answers to the same question. An
explicit tool error makes the agent deal with it. The new memory is still kept -
its content is valid knowledge, and re-writing the same name is a lossless
in-place update, so fixing the target and retrying is safe.

## Session binding and scope

Enforced in `internal/mcp`. Concepts: [Projects & scope](https://thereisnospoon.org/docs/concepts/projects/).

### Never read `project` from tool args directly

Route it through `resolveReadScope` (nothing to infer → global) or
`resolveWriteScope` (nothing to infer → `errNoScope`). Both apply
`validateProjectArg`.

`validateProjectArg` is the path-traversal defense, and the reason it cannot be
skipped is specific: a project slug becomes a directory under `memory/` and
`notes/`. The data-dir boundary check alone does not catch `../notes/_global` -
that slug cleans to a path *inside* the data dir, just in the wrong tree, where
one item can clobber another's files.

### A durable create uses `resolveWriteScope`, because it fails closed

Reaching for `resolveReadScope` on a write silently lands the item in the global
scope. Nothing errors, nothing warns; a project's private knowledge is simply now
in front of every agent on the machine.

Failing closed is the whole point: with no bound session, no ambient session, and
no explicit `project`, the write is rejected as ambiguous and the agent must
choose. `project: global` says it on purpose.

### `resolveWriteScope` registers the slug it is given

A durable create into an unregistered slug used to write the file and its index row
while leaving the projects table untouched: the project existed for the write, yet
was absent from `project_list` and the console until some unrelated path (a
`session_start` in a mapped repo, `map-repo`, an import) happened to backfill the
row.

That gap made the fail-closed error above unanswerable in practice. An agent told
to "pass `project=<slug>`" had no cheap way to learn whether an unmapped slug
would be rejected, and an agent that cannot tell a new-project write from a broken
one picks `project=global` - the single scope that reaches every project's
briefing. The guard calls `EnsureProject` on the named slug, which is what lets
the tool descriptions and the scope error *promise* that a new slug creates its
project. The guidance and the behavior are the same fix; either alone is a lie.

A typo'd slug is registered too. That is the same orphan made visible rather than
a new failure: the typo already created a scope on disk, and a row the console
lists is where a wrong one can be noticed and merged.

### Precedence, and no guessing

```text
explicit project  →  the bound session's project  →  the sole unambiguous ambient
```

Ambient sessions spanning more than one project are `errAmbiguousScope`, not a
guess. Inheriting the machine-latest ambient in that situation is exactly how a
write bleeds into a concurrent agent's project - and the agent that wrote it will
never see the mistake, because from its side the call succeeded.

### Only `session_start` binds a connection

Bindings are keyed by the MCP client session id. They evict on `session_end` and
via an opportunistic sweep.

### A lost binding does NOT error

It degrades to the ambient fallback. Never assume the binding you started with is
still there.

### Stamp provenance with `s.boundSession(ctx)`

Not with the raw binding.

### A new tool must be in `registerTools` AND bump `ToolCount`

Otherwise `doctor` fails its tool-count assertion. See
[Contributing](https://thereisnospoon.org/docs/internals/contributing/) for the full wiring, which also includes
`Catalog()`.

## FTS5 and LIKE escaping

Enforced in `internal/store`. Concepts: [Recall](https://thereisnospoon.org/docs/concepts/recall/).

### User text reaching `MATCH` goes through `ftsQuery`

`ftsQuery` (in `fts.go`) splits on non-alphanumeric runes, drops single-rune
tokens, quotes each remaining term, and ORs them. **Never build a MATCH
expression by concatenation.**

The concrete failure is small and total: FTS5 reads `-` as an operator, so a query
for `chroma-boot-race` handed to MATCH raw is parsed as a *subtraction* - and
finds nothing, or the wrong thing, without erroring. Quoting turns it into three
literal terms, which is what the user meant.

### `ftsQuery` returning `""` means "no usable token"

Callers treat that as no results - never as an unfiltered query. The distinction
matters because the empty string is the natural input to "no filter": a caller
that forwards it returns the entire corpus in response to punctuation.

### User text in `LIKE` goes through `escapeLikePrefix`

It escapes `\`, `%`, and `_`, **in that order**, with `ESCAPE '\'`. The order is
not cosmetic - escape the escape character last and you double-escape the escapes
you just added.

**Never LIKE-escape a value compared with `=`.** An equality comparison has no
wildcards, so escaping only corrupts the literal being matched.

## LLM degradation: remote vs local

Enforced in `internal/llm` and `internal/retrieve`.

The single distinction that organizes this whole area: **did a provider answer
badly, or did we fail to ask?**

| Sentinel | Meaning | Response |
|---|---|---|
| `ErrUnavailable` | The provider could not be reached. | **DEGRADE** |
| `ErrAuth` | The provider rejected the key. | **DEGRADE** |
| `ErrRateLimited` | The provider throttled us. | **DEGRADE** |
| `ErrConfig` | The request was never built. | **SURFACE** |

### Remote errors degrade

The provider answered badly or not at all, nothing in this process is wrong, and
the condition may clear on its own. `recall` drops to lexical-only, which is
honest: fewer results, from a leg that still works.

### `ErrConfig` surfaces

No provider was contacted. No retry helps. It will not clear.

Degrading here would trade one loud failure for quietly worse recall **for the
life of the daemon** - with nothing anywhere to tell the owner that semantic
search had stopped. A store that silently searches half as well is worse than one
that says it is broken.

### Classify at the `do` call sites

Use `doErr(op, err)`. Never hand-wrap a request-build failure as
`ErrUnavailable`; that is precisely the reclassification the taxonomy exists to
prevent.

### Why `base_url` is validated at construction

`NewEmbedder` and `NewChatClient` are the single construction points, and both
validate `base_url` there, because `url.Parse` accepts a bare host:
`"api.openai.com/v1"` parses fine, builds a perfectly valid request, and only
fails inside `Do` as an opaque transport error - indistinguishable from an
outage.

So the configuration typo would arrive dressed as `ErrUnavailable`, degrade
silently, and never be found.

That validation is exactly why `ErrConfig` should be **unreachable** at request
time. Which is exactly why it must not hide when it is reached: an unreachable
error that occurs is a defect, and defects should be loud.

### `DedupHint` and `files.embedItem` swallow every embed error on purpose

Leave them. Dedup is advisory and must never block a `memory_write`; indexing is
best-effort with a hash-retry (a failed embed clears the recorded content hash so
the next reconcile re-indexes and tries again). These are not oversights that
survived review - they are the two places where the "no fake results on error"
rule is deliberately traded for "never block the agent".
