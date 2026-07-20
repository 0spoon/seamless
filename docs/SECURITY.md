# Security

This document is the output of a full-codebase security audit of Seamless. It
records the threat model, what the security architecture already does well, the
findings that remain, and a checklist a team can act on. Line references are to
the tree as audited; they drift as code moves, so treat them as starting points.

## Threat Model

### Assets

- **The MCP bearer key** (`mcp.api_key`, 256-bit hex). Single static credential
  that authorizes every MCP tool call, every hook callback, and console access.
  Stored in `~/.config/seamless/seamless.yaml` (`0600`) and mirrored into each
  client's settings file as an `Authorization: Bearer` header.
- **The knowledge corpus**: memory and note bodies under `~/.seamless/memory` and
  `~/.seamless/notes`, plus the SQLite DB (`~/.seamless/seam.db`) holding sessions,
  tasks, trials, events, telemetry, and embeddings. Bodies can contain whatever an
  agent chose to persist -- credentials, host details, private decisions.
- **Third-party LLM/provider API keys** (`llm.api_key`, e.g. an OpenAI
  `sk-svcacct-...`) held in config.
- **Agent context integrity**: the briefing/recall text injected into every
  agent's context on session start is assembled from the corpus. Tampering with
  the corpus steers downstream agents.

### Threat Actors

Seamless is a **local-first, single-user** daemon that binds loopback
(`127.0.0.1:8081` by default) with a static bearer key. The realistic actors are:

- **A co-resident local process or user** on the same machine (another account, a
  compromised local dev tool, malware running as the user or another user) trying
  to read the corpus/keys or forge requests to the daemon.
- **Malicious web content** the operator's browser loads -- either from a remote
  site (DNS-rebinding, clickjacking) or from another service on `127.0.0.1:<port>`
  (same-site CSRF against the console).
- **A hostile HTTP origin** reached by the `capture_url` tool or a misconfigured
  LLM `base_url` (SSRF, oversized-response DoS).
- **A supply-chain attacker** who can publish a malicious release or MITM the
  install/update channel (`curl | sh`, `irm | iex`, `seamlessd update`).
- **Not in scope by design**: the local AI agents themselves. They hold the bearer
  key and run as the same OS user; they are inside the trust boundary. A "malicious
  agent" is equivalent to the user, so agent-reachable input is defended against
  corruption/escape, not against a fully hostile caller.

### Attack Surface

| Surface | Entry point | Auth |
|---|---|---|
| MCP tools (30) | `POST /api/mcp` (streamable HTTP) | Static bearer, per-tool-call |
| Console UI + SSE | `/console/*` | Bearer (CLI) or host-scoped cookie (browser) |
| Hook callbacks | `/hooks/*` | Static bearer |
| Health | `GET /healthz` | None (build metadata only) |
| Outbound: capture | `capture_url` tool -> arbitrary remote HTTP | SSRF-guarded egress |
| Outbound: LLM | OpenAI/Ollama/Anthropic over HTTPS | provider key |
| Install / update | `docs/install`, `docs/install.ps1`, `seamlessd update` | TLS + SHA-256 checksum + cosign-signed manifest; `update` verifies a Sigstore bundle in-process |
| Filesystem | memory/note markdown under `~/.seamless` | path/name validation |

## Security Architecture

What is already implemented and sound:

- **Authentication.** Every sensitive route requires the static bearer key.
  MCP verifies it with `subtle.ConstantTimeCompare` and a case-insensitive scheme
  check, failing closed on an empty key (`internal/mcp/server.go:356-365`), and the
  auth middleware is the outermost tool wrapper so unauthorized calls never reach a
  handler (`server.go:274-277,345-352`). The console applies the same constant-time
  check for both the CLI bearer path and the browser cookie
  (`internal/console/console.go:153-184`), and hook callbacks do likewise
  (`internal/hooks/handler.go:580-589`).
- **Cookie handling.** The console cookie stores `sha256("seamless-console\x00" +
  key)`, never the raw key, and is set `HttpOnly`, `SameSite=Lax`, `Path=/console`
  (`console.go:172-175,214-221`). Post-login redirect has an open-redirect guard
  (`safeNext`, `console.go:188-193`). The key never appears in a URL or query
  string; the CLI uses an `Authorization` header and the pre-auth console flow POSTs
  the key in a form body.
- **Key generation.** The bearer key is 32 bytes from `crypto/rand`, hex-encoded,
  written with `O_WRONLY|O_CREATE|O_EXCL` mode `0600` in a `0700` dir, race-safe
  against a concurrent bootstrapper (`internal/config/bootstrap.go:38-54`). No
  hardcoded or default credentials exist anywhere in the tree.
- **SQL.** Every query is parameterized. Dynamic WHERE/ORDER/LIMIT builders splice
  only compile-time-constant fragments and bind every value as `?`; IN-clauses use a
  safe `placeholders(n)` generator with matched bound args
  (`internal/store/*.go`). No caller-controlled sort column exists.
- **FTS5 / LIKE.** FTS `MATCH` input is tokenized on non-alphanumeric runes and each
  token is double-quoted, so caller text cannot become an FTS5 operator or column
  filter (`internal/store/fts.go:158-170`). LIKE queries escape `\ % _` with an
  explicit `ESCAPE '\'` clause (`internal/store/notes.go:163`, `search.go:29-31`).
- **Path safety.** `validate.Name` rejects `/`, `\`, `..`, null bytes, empty, and
  >255 chars; `validate.PathWithinDir` re-checks that the cleaned join stays under
  the data dir; every read/write/remove routes through `Store.abs` and a
  `checkTree` backstop catches a hostile `project` that tries to cross trees
  (`internal/validate/validate.go`, `internal/files/files.go:32-42,257`). Writes are
  atomic (temp-in-same-dir + `os.Rename`), which replaces rather than follows a
  symlink at the destination. The fsnotify watcher and reconciler skip symlinked
  dirs/files, so out-of-tree content is never indexed.
- **SSRF defense (capture).** `capture_url` resolves the host, rejects the lookup if
  *any* returned address is loopback/private/link-local/multicast/reserved (incl.
  CGNAT, 6to4, Teredo, NAT64), then dials the **validated IP literals** -- no
  re-resolution between check and dial, closing the DNS-rebind/TOCTOU gap. Redirects
  are re-validated on every hop (<=10, http/https only, no https->http downgrade),
  the body is capped at 2 MB, and the port allowlist fails closed to `{80,443}`
  (`internal/capture/url.go:55-64,88-104,125-157,198-260`).
- **Markdown -> HTML.** goldmark runs with raw HTML disabled; every rendered
  fragment additionally passes through a bluemonday **UGCPolicy** that restricts
  URLs to http/https/mailto/relative, defeating `javascript:`/`data:` links and
  images (`internal/markdown/render.go:34-71`). Console templates use `html/template`
  auto-escaping; the few `template.HTML` sinks emit only server-controlled content,
  and agent-authored text reaching HTML is escaped first (`internal/console/`).
- **Release/update integrity (transport).** All download URLs are HTTPS. Both
  installers verify the release archive's SHA-256 against `checksums.txt` with an
  exact-filename match, download to an unpredictable temp dir with an `EXIT` trap,
  and install via atomic rename. Install is per-user and refuses root by default;
  services run `--user`/`Limited`. `seamlessd update` streams the bootstrap script
  via stdin (no on-disk TOCTOU) and caps the response.
- **Release CI.** `release.yml` triggers only on `v*` tag pushes with
  `contents: write` and the ephemeral `GITHUB_TOKEN`; CI is `contents: read` with no
  secrets. No `pull_request_target` anywhere, so untrusted PRs run with a read-only
  token and no secret access.
- **LLM clients.** Keys come only from config/env, are sent over HTTPS auth headers,
  and are never logged (only the config path is logged on first-run generation).
  TLS verification is intact; calls have per-call context timeouts plus a client
  timeout, and retries are bounded, jittered, and cap a hostile `Retry-After`.

## Audit Findings

Given the loopback-bound, single-user, static-key architecture, **no finding is
CRITICAL or HIGH** -- the trust model deflates most severities, and the
agent-reachable input paths (SQL, filesystem, SSRF) are genuinely well-defended.
The findings below are hardening items and a mechanical dependency bump.

### Triage: distinguish the runtime threat model from the supply chain

Two different scopes apply here, and they must not be conflated:

1. **The runtime** is a personal, loopback-only, single-user daemon whose clients
   (local AI agents) are inside the trust boundary. The web-app and multi-user
   *runtime* findings defend against threats that do not exist in that model, so
   they are **accepted as out-of-scope**.
2. **The download / install / update supply chain protects every developer who
   downloads and runs Seamless.** Their safety is a first-class goal. Maintainer
   effort is *not* a valid reason to drop a finding in this scope -- absorbing that
   effort so users run authentic, untampered code is exactly the maintainer's job.
   "Keep it simple for the user" means the *end user's* experience, never the
   maintainer's convenience.

"Simple for the user" therefore means simple for the person downloading and running
the tool -- not fewer safeguards on the artifacts they receive. Crucially, the
downloading audience is heterogeneous: some run on shared/multi-user machines, some
bind non-loopback or run in a container, and all of them open the console in a
browser alongside other tabs. Findings that bite a realistic downloader are pursued
when the fix is *invisible* (no user-facing friction); only user-friction or
genuinely-moot items are accepted. Every pursued item below adds zero user friction.

| Pursued (invisible safety for a realistic downloader, or runtime hygiene) | Accepted -- genuinely moot for a single-user localhost *runtime* |
|---|---|
| **M1** deps/toolchain bump (removes a reachable capture DoS) | **I2** unauth MCP `tools/list` enumeration -- no secret exposed, nothing executable; catalog is public in docs |
| **M3** sign release artifacts + verify in installers/update (tampered-release protection for every downloader) | **I4** logout outside auth -- impact is clearing your own cookie; a nuisance, not a security issue, for anyone |
| **L10** pin CI actions to SHAs (hardens the pipeline that builds what users download) | **I5** `/healthz` build metadata -- a version string, identical for all users; liveness needs it open |
| **L6** `update --url` https-only + no scheme downgrade (only place remote content becomes shell code) | **L8 / I8** SSE write deadline / subscriber cap -- require the bearer key; no remote-attacker path, self-inflicted only |
| **I1** Host/Origin validation -- DNS-rebind protection for **all** users + those who bind non-loopback | **L9** `O_NOFOLLOW` on direct reads -- needs same-user FS write to plant a symlink; other users can't write your `$HOME` |
| **M2** (scoped) Origin/`Sec-Fetch-Site` check on state-changing console POSTs -- every browser user's surface, no CSRF-token machinery | **I13** provider error snippet in logs -- local logs on the user's own machine, not shipped anywhere |
| **I3** (scoped) `X-Frame-Options: DENY` + `nosniff` -- clickjacking/MIME defense for all browser users (no CSP, to avoid breaking inline JS) | *(I9, I10 remain accepted by design -- see Accepted Risks)* |
| **L3 / L4** settings.json `0600` / no key in argv -- protects downloaders on **shared/multi-user machines** | |
| **L1** rotate the live LLM key (ops, must-do) | |
| **L2** delete the key-bearing `console-open` temp file (one-liner) | |
| **L5** corpus files `0600` / dirs `0700` (sensitive personal data) | |
| **L7** server `IdleTimeout`/`ReadTimeout` (cheap hygiene) | |
| **I6** warn when `Addr` is non-loopback (simple, protective) | |
| **I7** `io.LimitReader` on LLM responses (robustness) | |
| **I11** reject Windows-reserved/leading-dot names (real Windows correctness bug) | |
| **I12** slugify `name` at the MCP layer (UX consistency with notes) | |

The detailed write-ups for the accepted items are retained below for the record;
their tracking tasks are closed as `dropped` with this rationale. M2 and I3 are
**scoped down** from their write-ups: the cheap invisible half (an Origin check and
two response headers) is pursued; the heavier half (per-request CSRF tokens, a full
CSP) is deferred as friction/maintenance disproportionate to a localhost console.

### Critical

None.

### High

None.

### Medium

**M1. Known-vulnerable Go toolchain and `golang.org/x/net` -- 16 govulncheck hits with reachable call paths.** Status: FIXED (2026-07-19)
- Location: build toolchain `go1.25.8`; `go.mod:16` (`golang.org/x/net v0.51.0`).
- Description: `govulncheck ./...` reports 16 vulnerabilities whose vulnerable
  symbols are actually called. The most concretely reachable:
  `golang.org/x/net/html` XSS/DoS bugs (GO-2026-5030/5029/5028/5027/5025) via
  `capture.URLFetcher.FetchURL -> html.Parse` (`internal/capture/url.go:261`), the
  HTTP/2 infinite-loop (GO-2026-4918) on the transport, and several `html/template`
  escaper-bypass bugs (GO-2026-4982/4980/4865) reachable from the console login
  render (`cmd/seamlessd/console.go:99`).
- Impact: A malicious page fetched by `capture_url` can drive the HTML parser into
  a DoS; the `html/template` bypasses are lower-reachability here because the login
  template renders only server-controlled data, but they are live in the binary.
- Recommendation: Bump the build toolchain to >= go1.25.12 and run
  `go get golang.org/x/net@v0.55.0 && go mod tidy`. This one change clears all 16.
  Add `govulncheck ./...` to `make check` / CI so this cannot silently regress.

**M2. Console state-changing POSTs have no CSRF token; SameSite=Lax is port-blind on loopback.** Status: FIXED (2026-07-19)
- Location: cookie set at `internal/console/console.go:214-221`; mutating routes at
  `console.go:104,110,113,118-126` (memory archive, task release, plan approve,
  gardener request/split/apply/dismiss/retarget, briefing save/reset).
- Description: All mutations are POST (so a classic cross-*site* GET cannot trigger
  them) and rely on `SameSite=Lax` alone -- there is no per-request CSRF token.
  SameSite computes "site" from the registrable host and **ignores the port**, so
  any other content served from `127.0.0.1:<any-port>` (another local dev tool, a
  malicious page you load from a local server, a compromised co-resident service) is
  same-site and its forged POST carries the console cookie.
- Impact: An attacker able to run or get you to load a page on any localhost port
  can archive a memory, approve a plan, or mutate briefing settings on your behalf.
  Bounded to moderate-impact writes (gardener request/split only *propose*), which
  is why this is MEDIUM.
- Recommendation: Add a per-session CSRF token to the mutating forms and verify it
  server-side, or enforce a `Sec-Fetch-Site: same-origin` / `Origin` check on POST.

**M3. Release artifacts and installer scripts are checksum-verified but unsigned.** Status: FIXED (2026-07-19)
- Location: `.goreleaser.yaml:67-68` (produces `checksums.txt`, no `signs:` block);
  `docs/install:131-142`, `docs/install.ps1:103-114` (verify archive against
  `checksums.txt`); `cmd/seamlessd/update.go:114-127` (fetch installer, pipe to
  `sh -s` / `powershell -Command -`).
- Description: `checksums.txt` is fetched from the same GitHub release origin over
  TLS with no cryptographic signature, so the checksum proves integrity against
  transport corruption but not **authenticity**. An attacker who can publish a
  release (leaked `GITHUB_TOKEN` / maintainer account) or MITM both the Pages host
  and the release assets can swap the tarball and `checksums.txt` together.
- Impact: Arbitrary code execution on every `curl | sh`, `irm | iex`, and
  `seamlessd update`. This is the standard `curl | sh` trust model; the audit-worthy
  gap is the absence of artifact signing.
- Recommendation: Sign `checksums.txt` with cosign (keyless) or minisign in the
  release workflow, and verify the signature in both installers and in
  `update.go`'s `fetchInstaller` before executing.
- **Done (2026-07-19):** `.goreleaser.yaml` signs `checksums.txt` with keyless
  cosign (`signs:`), the release job carries `id-token: write` and installs
  cosign, and both installers verify `checksums.txt.sig`/`.pem` against a
  certificate identity pinned to this repo's `release.yml` on a `v*` tag. cosign
  absent warns and falls back to checksum (a first install is trust-on-first-use
  over TLS regardless, and requiring a signing tool up front is real friction);
  cosign present and failing is fatal.
- **Done (2026-07-19, second pass):** strict verification inside `seamlessd
  update`, via option (b) -- sigstore-go linked into the daemon. The release
  workflow signs `docs/install` and `docs/install.ps1` as self-contained
  Sigstore bundles (`cosign sign-blob --new-bundle-format`) and goreleaser
  publishes the scripts and bundles together as release assets; `update`
  fetches both from `releases/latest/download/` and verifies in-process
  (`cmd/seamlessd/update_verify.go`) against an embedded snapshot of the
  Sigstore trusted root, requiring a transparency-log entry and pinning the
  certificate identity to this repo's `release.yml` on a `v*` tag -- the same
  identity the installers pin for `checksums.txt`. Verification is offline
  (the bundle embeds the Rekor entry), unconditional, and fail-closed: there
  is no cosign-absent fallback because the verifier is compiled in. A custom
  `--url` carries no bundle and runs TLS-only with a printed warning, as an
  explicit owner override. The human `curl | sh` path is unchanged and serves
  the same bytes.

### Low

**L1. Live third-party LLM credential sits in cleartext in the working-tree config.** Status: CLOSED (2026-07-19)
- Location: `seamless.yaml:24` (MCP key) and `:41` (an OpenAI `sk-svcacct-...`).
- Description: The repo-root `seamless.yaml` holds what appears to be a **real,
  live** OpenAI service-account key. Mitigating and verified: the file is
  `.gitignore`d, is not tracked, and the key appears in no commit in history
  (`git log -S` and all-revs `git grep` are both empty) -- this is not a git leak.
- Impact: A live credential in cleartext in a sandbox dir readable by local tooling;
  exposure risk is from backups, dir sync, screen-share, or an accidental
  `git add -f`, not from the current git state.
- Recommendation: Rotate the `sk-svcacct-` key regardless, since it is now readable
  outside its intended `0600` store; confirm the working-tree file is `0600`.
- **Done (2026-07-19):** the working-tree `seamless.yaml` is now `0600` (it was
  `0644`); re-confirmed gitignored and untracked.
- **Closed (2026-07-19):** the owner closed the remaining rotation item. Key
  rotation happens in the provider dashboard and cannot be verified from this
  repo; the mitigations of record on the local side are the `0600` file mode,
  the gitignore, and the key's absence from all of git history.

**L2. `console-open` leaves the bearer key in a never-deleted temp file.** Status: FIXED (2026-07-19)
- Location: `cmd/seamlessd/console.go:73-93`.
- Description: `runConsoleOpen` writes the self-submitting login page -- which embeds
  `cfg.MCP.APIKey` verbatim in a hidden form field -- to
  `os.CreateTemp("", "seamless-console-*.html")` and opens it in a browser, but never
  removes the file. The file is `0600`; on macOS `$TMPDIR` is per-user, but on Linux
  `/tmp` is shared and the credential accumulates across runs until a reaper/reboot.
- Impact: A persistent copy of the daemon's sole credential lingers after every
  `console-open` / `make console`, exposed to backup/sync tooling.
- Recommendation: `defer os.Remove(f.Name())` after the browser launch (or delete on
  a short delay). Contrast `update.go` and both installers, which clean their temp.

**L3. Bearer key persisted into `settings.json` with the file's existing mode.** Status: FIXED (2026-07-19)
- Location: `internal/hooks/install.go:321-327` (writes `Authorization: Bearer <key>`
  into the client settings file); `install.go:235-246,561-575` (`loadSettings`
  preserves an existing file's mode; `writeSettings` writes with it).
- Description: A *new* settings file is created `0600`, but an existing
  `~/.claude/settings.json` (Claude Code commonly creates it `0644`) keeps its mode,
  so the long-lived bearer key can be persisted world-readable.
- Impact: On machines where the client already created a `0644` settings file, the
  key becomes readable by any local user/group. Lower-severity because the key also
  lives in the `0600` config file.
- Recommendation: Force `0600` whenever the written settings contain the secret.

**L4. Bearer key passed as a process argument to `claude mcp add` (ps-visible).** Status: FIXED (2026-07-19)
- Location: `cmd/seamlessd/install_hooks.go:421-423,442`.
- Description: `claudeMCPAddArgs` puts `Authorization: Bearer <key>` into the argv of
  `exec.Command(claude, ...)`, so the full key is visible via `ps auxww` for the
  lifetime of that subprocess. (The Codex path deliberately avoids this -- it passes
  only the config *path*.)
- Impact: Transient, local-only credential exposure during install-hooks.
- Recommendation: Pass the key via stdin or an env var to the subprocess rather than
  argv, matching the Codex path.

**L5. World-readable permissions on the corpus and DB.** Status: FIXED (2026-07-19)
- Location: `internal/files/files.go:51` (`fileMode = 0o644`),
  `internal/files/manager.go:108` and `files.go:365` (dirs `0o755`),
  `internal/store/store.go:41` (data dir `0o755`); `seam.db` inherits the driver
  default.
- Description: Memory/note bodies (which may hold sensitive knowledge) are written
  world-readable in world-traversable directories.
- Impact: Bounded by the mode of `~/.seamless`'s parent (`$HOME`). On a single-user
  Mac with a `750` home this is low; any other local account can read the corpus if
  the home is traversable.
- Recommendation: Create memory/note files `0600` and data dirs `0700`.

**L6. `seamlessd update --url` has no scheme enforcement and follows cross-scheme redirects before piping to a shell.** Status: FIXED (2026-07-19)
- Location: `cmd/seamlessd/update.go:100-121,135-158`.
- Description: `--url` overrides the installer endpoint; `fetchInstaller` uses
  `http.DefaultClient` (follows redirects, including HTTPS->HTTP) with no check that
  the URL is `https://`. The fetched body is executed via `sh`/`powershell`.
- Impact: `seamlessd update --url http://...` (or a default endpoint that 302s to
  plaintext) fetches over an unauthenticated channel and pipes it to a shell:
  MITM -> RCE. Requires the flag or a compromised default host, so likelihood is low.
- Recommendation: Reject non-`https` schemes and refuse cross-scheme redirects for
  anything that feeds a shell.

**L7. No `ReadTimeout` / `IdleTimeout` / `MaxHeaderBytes` on the HTTP server.** Status: FIXED (2026-07-19)
- Location: `cmd/seamlessd/main.go:369-376` (`newHTTPServer` sets only
  `ReadHeaderTimeout: 5s` and `BaseContext`).
- Description: `ReadHeaderTimeout` blocks header-phase Slowloris, but a slow request
  *body* and idle keep-alive connections are unbounded. (`WriteTimeout` is correctly
  omitted so it cannot kill the long-lived SSE stream.)
- Impact: A local client can pin connections/goroutines. Low on a loopback
  single-user service; escalates if ever bound off-loopback.
- Recommendation: Set an `IdleTimeout` and a modest `ReadTimeout` (or per-route body
  deadlines), plus `MaxHeaderBytes`.

**L8. SSE writes have no per-write deadline; stalled reader leaks a goroutine + subscription.** Status: ACCEPTED (see Triage)
- Location: `internal/console/sse.go:46-78`.
- Description: SSE writes have no `SetWriteDeadline`. A client that completes the
  handshake then stops reading (zero window) blocks `Write` without cancelling the
  request context, leaking one goroutine + one subscriber until the OS TCP timeout.
  Mitigating: event fan-out is non-blocking drop-on-full (`subBuffer=32`,
  `internal/events/recorder.go:55,99-108`), so a stalled reader never back-pressures
  the write path, and the stream requires auth.
- Impact: A bounded, authenticated, localhost-only goroutine leak.
- Recommendation: Set a write deadline per SSE flush, or a heartbeat with a deadline.

**L9. Direct file reads follow symlinks while the indexer deliberately does not.** Status: ACCEPTED (see Triage)
- Location: read path `internal/files/files.go:287,317,348` (`os.ReadFile(abs)`)
  vs. the symlink-skipping watcher/reconciler (`watcher.go:94`,
  `manager.go:155,160`).
- Description: `readFile` follows a symlink; `abs()` validates only the path string,
  not the resolved target. To exploit, an attacker must already have local FS write
  as the same user (inside the trust boundary), but the inconsistency means a
  write-time occupancy check could surface out-of-tree content in an error.
- Impact: Negligible under the trust model; noted as an inconsistency.
- Recommendation: Add an `O_NOFOLLOW`/`Lstat` guard on the direct read paths to match
  the watcher's stance.

**L10. GitHub Actions pinned to mutable major tags.** Status: FIXED (2026-07-19)
- Location: `.github/workflows/release.yml:20,24,29`, `ci.yml:24,26`
  (`actions/checkout@v7`, `actions/setup-go@v6`, `goreleaser/goreleaser-action@v6`).
- Description: The release job holds `contents: write` + `GITHUB_TOKEN`; a
  compromised/retagged third-party action would run in that context.
- Impact: Supply-chain risk contingent on an upstream action compromise.
- Recommendation: Pin the release workflow's actions to full commit SHAs with a
  version comment.

### Low / Informational

- **I1. No Host/Origin validation (DNS-rebinding).** `cmd/seamlessd/main.go:285-296`,
  `internal/mcp/server.go:285-294`. Sensitive routes require the bearer key or a
  host-scoped cookie that a rebinding attacker cannot present, so exposure is limited
  to the unauthenticated endpoints -- but a Host allowlist (`127.0.0.1`, `localhost`,
  `[::1]`) is cheap defense-in-depth and should be mandatory before any non-loopback
  bind. Status: FIXED (2026-07-19).
- **I2. MCP auth is per-tool-call; unauthenticated `tools/list` enumeration.**
  `internal/mcp/server.go:274-277`. An unauthenticated loopback client can complete
  `initialize` and enumerate tool names/schemas but **cannot execute any tool** (auth
  fails closed at call time). The catalog is already public in the docs. Status: ACCEPTED (see Triage).
- **I3. No security headers on console responses.** No CSP, `X-Frame-Options`, or
  `X-Content-Type-Options` anywhere in `internal/console/`. The console is framable
  (clickjacking, which pairs with M2). Recommend at least `X-Frame-Options: DENY` and
  a restrictive CSP. Status: FIXED (2026-07-19).
- **I4. `POST /console/logout` is outside the auth wrapper.**
  `internal/console/console.go:95`. A same-site local page can force-logout the
  operator; trivial impact (clears the victim's own cookie). Status: ACCEPTED (see Triage).
- **I5. `/healthz` is unauthenticated and returns build metadata.**
  `cmd/seamlessd/main.go:379-394` returns version/commit/built to any caller. Minor
  version disclosure. Status: ACCEPTED (see Triage).
- **I6. No enforcement that the bind address is loopback.**
  `internal/config/config.go:333-375` accepts any `Addr`; setting `0.0.0.0:8081`
  exposes MCP/hooks/console to the LAN behind only the static key (cookie is not
  `Secure`, correct for localhost but weaker off-host). Recommend warning on a
  non-loopback `Addr`. Status: FIXED (2026-07-19).
- **I7. LLM response bodies decoded with no size limit.** `internal/llm/openai.go:84`,
  `chat.go:139,212,287`, `ollama.go:77` use `json.NewDecoder(resp.Body)` with no
  `io.LimitReader`. `base_url` is owner-configured and TLS-verified, so a hostile
  oversized body requires a compromised/misconfigured endpoint. Bounded reader would
  be defense-in-depth. Status: FIXED (2026-07-19).
- **I8. No cap on concurrent SSE subscribers.** `internal/events/recorder.go:73-95`.
  Bounded only by the auth requirement; cleanup on disconnect is correct. Status: ACCEPTED (see Triage).
- **I9. Private knowledge is sent to an external LLM provider by default (privacy, by
  design).** With `provider: openai`, memory/note bodies (for embeddings), recall
  queries, and completed-session findings (gardener digests) leave the machine to
  `api.openai.com` (`internal/files/manager.go:329`, `internal/retrieve/recall.go:107`,
  `internal/gardener/digest.go:79`). This is the intended semantic-search design;
  `provider: ollama` keeps everything on-machine. Status: ACCEPTED (documented design).
- **I10. Store-driven context injection is an inherent prompt-injection surface.**
  Hook responses inject briefing/recall content assembled from the corpus into an
  agent's context (`internal/hooks/handler.go:196-219`). This is context injection,
  not host command execution (the response is never run as a shell command, and hook
  *command lines* are exec-form argv with no store influence). Inherent to a memory
  system where the human is an observer. Status: ACCEPTED (inherent).
- **I11. `validate.Name` accepts leading-dot and Windows-reserved names.**
  `internal/validate/validate.go:90-110` permits `.hidden`, `.`, and `CON`/`NUL`/etc.
  All stay within the item's tree, so no traversal; reserved names only matter if run
  on Windows (the target is macOS/Linux). Status: FIXED (2026-07-19).
- **I12. `memory_write` does not run `name` through `validate.Name` at the MCP layer.**
  `internal/mcp/tools_memory.go:38` checks only non-empty; validation happens
  downstream in `files.Store.WriteMemory` **before** any disk write, so no unsafe
  filename reaches disk. Cosmetic/consistency vs. `notes_create` (which slugifies).
  Status: FIXED (2026-07-19).
- **I13. Provider error-body snippet (<=512 B) propagated into errors and logs.**
  `internal/llm/openai.go:101-108` -> `internal/gardener/digest.go:44`. Could surface
  provider-side response content in daemon logs; bounded, not prompt data. Status: ACCEPTED (see Triage).

## Areas Reviewed With No Issues

| Category | Details |
|---|---|
| SQL injection | Every query parameterized; dynamic WHERE/ORDER/LIMIT splice only constant fragments and bind values as `?`; IN-clauses use `placeholders(n)`; no caller-controlled sort column. Migrations are embedded static files in transactions. |
| FTS5 injection | `ftsQuery` tokenizes on non-alphanumerics and double-quotes each token; caller text cannot become an FTS5 operator/column filter; `MATCH` value also bound (`internal/store/fts.go:158-170`). |
| LIKE wildcard smuggling | `\ % _` escaped with explicit `ESCAPE '\'` (`notes.go:163`, `search.go:29-31`). |
| Path traversal | `validate.Name`/`PathWithinDir` reject `/`, `\`, `..`, absolute, null byte; `Store.abs` + `checkTree` re-validate on every op; end-to-end write and read paths traced and sound. |
| Symlink write redirect | `AtomicWrite` renames temp-in-same-dir over target (replaces, not follows); watcher/reconciler skip symlinked dirs/files. |
| SSRF | `capture_url` validates every resolved address, dials the validated IP literal (no re-resolve), re-validates each redirect hop, caps body at 2 MB, fail-closed port allowlist. |
| XSS | goldmark raw-HTML disabled + bluemonday UGCPolicy on every fragment; `javascript:`/`data:` URLs stripped; console uses `html/template` auto-escaping; agent text escaped before HTML sinks. |
| Auth (MCP/console/hooks) | Constant-time key compare, fail-closed on empty key, auth as outermost middleware; console cookie stores a hashed token, `HttpOnly`/`SameSite=Lax`; key never in a URL. |
| Key generation | `crypto/rand` 32 bytes, `O_EXCL` `0600` in `0700` dir, race-safe; no hardcoded/default credentials. |
| LLM key handling | Keys only from config/env, sent over HTTPS headers, never logged; TLS verification intact; bounded timeouts and retries. |
| Secrets in git | No `sk-`/`ghp_`/`AKIA`/PEM in any tracked file or in history; `seamless.yaml` and `*.db*` gitignored; no committed `.env`. |
| Release CI | Tag-triggered, least-privilege `GITHUB_TOKEN`, no `pull_request_target`; installers verify SHA-256 with exact-filename match, atomic install, per-user/no-root. |
| Local exec surface | Service/hook/git exec calls use fixed argv with no shell; the one interpolated PowerShell arg is quote-escaped; `--purge` guards against catastrophic deletes. |
| Importer | v1 source opened read-only; every insert parameterized; imported names slugified then re-validated; malformed names rejected non-fatally. |

## Accepted Risks

- **External LLM egress by default (I9).** With the default OpenAI provider,
  memory/note bodies, recall queries, and session findings leave the machine for
  embeddings and gardener digests. This is the intended semantic-search and
  maintenance design and is documented; operators who require on-machine-only
  processing set `provider: ollama`.
- **Context-injection trust in the corpus (I10).** Briefing/recall text is assembled
  from stored memories/notes and injected into agent context. A tampered corpus can
  steer downstream agents. This is inherent to an agent-memory substrate where the
  human is an observer/editor; it is not host command execution.
- **`curl | sh` bootstrap.** The top-level install entry point pipes a TLS-fetched,
  checksum-gated script to a shell -- the accepted distribution *mechanism*. The
  authenticity gap it leaves (an unsigned checksum manifest) is **not** accepted:
  **M3** (artifact signing + verification) and **L10** (pinned CI actions) are
  pursued precisely because they protect the developers who download and run
  Seamless.
- **Genuinely-moot runtime findings (I2, I4, I5, L8, L9, I8, I13).** Reviewed and
  accepted because they expose no secret, need the bearer key, or require same-user
  filesystem access -- with no realistic angle even for a downloader on a shared
  machine or a non-loopback bind. See the triage section for the per-item reasoning;
  each is fully written up in Audit Findings for the record. (The findings that *do*
  bite a realistic downloader -- I1, M2, I3, L3, L4 on the runtime side, and M3, L10,
  L6 on the supply-chain side -- are pursued, not accepted; M2/I3 in a scoped-down,
  friction-free form.)

## Security Checklist

- [x] Authentication: all sensitive endpoints require auth (`/healthz` intentionally open, build metadata only)
- [x] Authorization: single-user model; no cross-user resources to isolate (agents are inside the trust boundary)
- [x] Input validation: external input validated/sanitized (names, paths, FTS, capture URLs)
- [x] SQL injection: all queries use parameterized statements
- [x] XSS: user content escaped in HTML output (goldmark + bluemonday UGCPolicy + `html/template`)
- [x] CSRF / browser surface: cookie-authenticated state-changing POSTs require a same-origin `Sec-Fetch-Site`/`Origin` (M2 -- bearer callers skip, having no ambient credential); every console response carries `X-Frame-Options: DENY`, `nosniff`, `no-referrer` (I3); `Host` allowlist rejects rebound names with 421 (I1)
- [x] Cryptography: strong algorithms, `crypto/rand` key, no hardcoded keys
- [x] Secrets management: no secrets in source (clean in git); settings.json holding a bearer key is clamped to `0600` (L3); the key is out of argv entirely, read at connect time via a `headersHelper` (L4); `console-open`'s temp login page is deleted after use and stale ones swept (L2); working-tree config is `0600`; the L1 rotation item was closed by the owner (2026-07-19)
- [~] Rate limiting: none today; accepted (auth-gated, loopback, single client)
- [x] Request size limits: MCP hook bodies capped (8 MiB); console free-text POSTs capped (16 KiB); capture body capped (2 MB) -- I7 adds an LLM-response cap
- [x] Error handling: no stack traces/internal paths leaked to clients
- [x] Dependencies: toolchain pinned to `go1.25.12` and `golang.org/x/net` at `v0.56.0` -- all 16 govulncheck hits cleared (M1); `make vulncheck` is a hard step of `make check` and of CI (clean including the sigstore-go tree added for M3), and dependabot watches actions + modules monthly
- [x] Supply chain (protects downloading users): `checksums.txt` is cosign-signed (keyless) and both installers verify it against a certificate identity pinned to this repo's release workflow (M3); `seamlessd update` verifies the bootstrap script's Sigstore bundle in-process (sigstore-go, embedded trusted root, same pinned identity) before piping it to a shell, fail-closed with no fallback (M3, second pass); CI actions pinned to full commit SHAs (L10); `update`'s fetches are https-only with no cross-scheme redirect (L6)
- [x] TLS: not applicable on loopback; all outbound and download traffic is HTTPS with verification
- [x] Logging: keys never logged; only config path logged on generation (I13 provider-snippet item accepted)
- [x] Filesystem hardening: corpus files are created `0600` and data dirs `0700`, and an idempotent startup pass narrows any pre-existing group/world bits (L5) -- `O_NOFOLLOW` (L9) accepted (needs same-user FS write)
- [x] DNS-rebinding: `Host` is validated against a loopback/bind allowlist, rejecting anything else with 421 (I1); a non-loopback bind logs a prominent startup warning (I6)

## Reporting Vulnerabilities

Seamless is a single-maintainer, local-first project (`github.com/0spoon/seamless`).
Report suspected vulnerabilities privately to the maintainer via a GitHub Security
Advisory (**Security -> Report a vulnerability**) on the repository, or by direct
contact with the maintainer -- do not open a public issue for an unfixed
vulnerability. Because the daemon binds loopback and serves a single local user,
most issues are local-privilege or supply-chain in nature; include the OS, install
method, and whether the bind address was changed from the loopback default.

## Last Audited

2026-07-19. Remediation of the pursued findings landed the same day
(plan `security-hardening-2026-07`): M1, M2, M3, L2-L7, L10, I1, I3, I6, I7,
I11, I12 fixed; L1 closed by the owner. M3 landed in two passes: signed
`checksums.txt` verified by both installers, then in-process Sigstore
verification of the bootstrap script inside `seamlessd update`.
I2, I4, I5, I8, L8, L9, I13 accepted per the triage above.
