---
title: seamlessd CLI
description: The daemon and operator CLI - serve, doctor, export, import, install-hooks, client-config, uninstall, update, map-repo, family, console-open, start/stop/restart/status, and version.
---

`seamlessd` is both the server and the operator CLI. `serve` runs the daemon;
every other subcommand is a one-shot that opens the same config and database
directly, without going through a running server. That means most of them work
whether or not the daemon is up - and that `map-repo` and `family` write state
the running daemon reads.

Each subcommand parses its own flags. None of them take positional arguments
except `family`, which takes only positionals.

For the keys every command below resolves, see
[Configuration](/reference/configuration/).

## seamlessd serve {#seamlessd_serve}

```bash
seamlessd serve [--addr HOST:PORT]
```

Starts the HTTP server and blocks until SIGINT or SIGTERM, then shuts down
gracefully. `--addr` overrides the configured bind address (default
`127.0.0.1:8081`). The flag is the bind address, so everything downstream that
derives "where do clients reach this daemon" answers from the address actually
listened on - a configured `server_url` still wins over both.

It refuses to start at all under `role: client`, before it opens a file or a
port: a client install has no daemon of its own by definition, and serving one
would give the machine a second, empty corpus its own hooks never write to.

With `tls.cert_file` and `tls.key_file` both set, the listener is **https**
with a TLS 1.2 floor, and the console session cookie is marked `Secure`. One
without the other is refused when the config loads. Outermost in the handler
chain is the Host-header allowlist: the loopback names, a concrete bind host,
the host of `server_url`, and `allowed_hosts`. Anything else gets `421
Misdirected Request`. A wildcard bind with no host named anywhere is the single
unguarded case - naming one arms the guard there too. Binding beyond loopback
logs a `SECURITY` warning at startup whose text differs with TLS on or off, and
a second warning when the bind is wide while `server_url` still names loopback.
[Share one daemon across a LAN](/guides/network-install/) is the setup guide.

On a true first run - no config file anywhere in the search order and no
`SEAMLESS_MCP_API_KEY` in the environment - it generates the bearer key and
writes it to `~/.config/seamless/seamless.yaml` before starting. An existing
config file is never edited, even when its key is empty.

It wires up:

- `/healthz` - liveness plus a database ping. Reports `degraded` with a 503 when
  the ping fails.
- `/api/mcp` - the MCP tool endpoint, bearer-authenticated.
- `/api/hooks/...` - the session and plan-capture hooks.
- `/console/...` - the observability console. The bare root `/` redirects here.

Startup is deliberately tolerant of a half-configured install, and the log is
where you find out:

- **No embedder** - recall degrades to FTS-only for the life of the process. A
  missing credential logs a warning; a *malformed* setting (a bad `base_url`)
  logs an error, because that one is a typo rather than a choice.
- **No chat client** - gardener digest passes no-op.
- **Empty `mcp.api_key`** (a config file exists but leaves it blank) - logs a
  warning, and every MCP and hook request is then rejected.
- **Gardener disabled** - logged, and no maintenance passes run.

The startup line carries the version, commit, and data directory, which is how
you spot a daemon running older code than your working tree.

## seamlessd doctor {#seamlessd_doctor}

```bash
seamlessd doctor
```

Server-side self-checks. Each line reports `ok`, `info`, `warn`, or `fail`;
**only a `fail` exits non-zero** - warnings and info lines are informational.
Checks stop early if config or the database cannot be loaded at all.

| Check | What it reports |
|---|---|
| `binary` | The version that ran. |
| `config` | Which file it loaded, or that it fell back to defaults + env. |
| `data_dir` | The resolved data directory. |
| `mcp.api_key` | Set, or a warning that `/api/mcp` will reject everything. |
| `llm` | The provider, or a warning that its credential is missing. |
| `embedder` | Probes the embedder with a real embed call. Unreachable, unconfigured, or provider `anthropic` (no embeddings API) is a warning: recall degrades to FTS. |
| `bind` | Loopback is OK. Non-loopback with TLS is OK with the reminder that the bearer key is still the only authentication; non-loopback *without* TLS is a warning naming what travels in the clear. |
| `server_url` | Fetches `/healthz` through the advertised URL. Not answering is **info** (the daemon is often stopped while doctor runs); a `421` is a **fail** naming the Host header it just refused, which means the allowlist and the advertised name disagree. A derived URL says so. |
| `tls` | Off, or the certificate's expiry (a warning from 30 days out, nothing auto-renews) and whether its SANs cover the host of `server_url` - the one that fails at the client's handshake with a message that names no file on the server. |
| `database` | Path, schema version, and table count. Opens and migrates if needed. |
| `schema version` | An **info** line pairing what the database has applied with what this binary compiles - `v25 applied / v25 compiled`. A database *ahead* of the binary warns: it was written by a newer `seamlessd`, which is also why an archive from it would be refused. |
| `repo map` | Warns when mapped paths belonging to **this** host name directories that no longer exist on disk. A moved repo adopts its project at its next session start; a moved-and-renamed repo needs the printed `map-repo` override. Rows belonging to other hosts are counted and reported as not verifiable from here - never stat'd, never treated as missing. |
| `remote sessions` | An **info** line: which other machines used this daemon in the last 24 hours, and how many daemon-side captures were skipped for them. `none in 24h` on a single-machine install. |
| `mcp_tools` | On a server install, fails if the number of registered tools disagrees with the expected count - catches a tool written but never wired in. On a `role: client` install it is the live count instead: `tools/list` against the server, judged against that server's effective feature state. |
| `claude CLI runtime` / `claude app runtime` | Each discoverable Claude Code runtime's self-reported version, separately: the PATH CLI and, on macOS, every runtime the desktop app has retained - they can differ, and collapsing them would hide exactly that skew. No discoverable runtime means no lines. |
| `hooks` | Claude Code definitions compared with today's desired profile. |
| `claude desktop mcp` | The chat surface's `claude_desktop_config.json` entry compared with the desired stdio bridge. Absent is an **info** line naming the opt-in command, never a nag; an exact entry reports OK while stating that the running app's loaded state is unverifiable (the app reads the config at startup). No lines when neither the app nor a desktop config exists. |
| `codex CLI runtime` / `codex app runtime` | Each discoverable Codex runtime's self-reported version, separately, on the same principle. |
| `codex hooks` | Codex current, stale, and missing definitions, including command targets. |
| `codex hook trust` | Always warns that trust is unverified and directs you to Codex `/hooks`; no private trust state is read. |
| `codex hook activity` | Last observed SessionStart/UserPromptSubmit event, if any; evidence only, not proof of current trust. |
| `codex mcp` | Exact enabled stdio bridge state from `codex mcp get seamless --json`, plus executable/config target existence. |
| `feature skills` | An **info** line when a client's skill home still holds a skill for an [optional feature](/reference/console/#optional-features) you switched off - the daemon never deletes there on a toggle. Re-run `install-hooks` to remove it, or re-enable the feature. |
| `gardener` | The ticker configuration, or a warning that it is disabled. |

Under `role: client` the report is a deliberately short, different list: a
`role` info line naming the server it dials, a `server_url` reachability probe,
the API key, the live MCP tool count, and the same desired-state hook and MCP
comparisons above. That tool count is the one client check that presents the
bearer key, so a wrong or rotated key surfaces there rather than as a silent
green. Everything else is absent because it describes a machine
that is somewhere else - the database, schema version, repo map, remote
sessions and feature skills all read a local `seam.db` a client does not have
(opening one would *mint* the database whose absence is what `role: client`
means); the gardener runs inside the daemon; and the LLM and embedder belong to
the server that does the embedding. The `server_url` probe is a **fail** rather
than info on a client, because a client has no benign "daemon is stopped"
state: until the server answers there are no briefings, memories, or tools on
this machine.

The definition checks compare current desired state, not mere existence. The
shared classifier recognizes exact current definitions, marked stale entries,
and only unmistakable legacy Seamless shapes; arbitrary foreign hooks survive.
Codex trust is a separate fact because Codex exposes no supported query for the
current trust decision. A recent hook observation cannot make a changed command
healthy.

Reach for it after changing config, after an upgrade, or as the first step when
recall has quietly gone lexical.

## seamlessd export {#seamlessd_export}

```bash
seamlessd export [-o FILE|-] [--no-db]
```

Writes the whole instance to one gzipped tar: the markdown corpus, a consistent
snapshot of `seam.db`, and a `manifest.json` describing what is inside.

**Config and `mcp.api_key` are never in the archive.** A restored instance gets
its own config and a freshly generated key, which is what lets an archive be
copied to another machine, a NAS, or a colleague without carrying this machine's
only credential.

| Flag | Default | Meaning |
|---|---|---|
| `-o` | `seamless-<host>-<UTC timestamp>.tar.gz` in the working directory | Where to write the archive. `-` streams it to stdout and puts the report on stderr. |
| `--no-db` | `false` | Export the markdown trees only. Sessions, tasks, trials, events, and embeddings are then **not** in the archive. |

Layout, in tar order:

```text
manifest.json                       always first
seam.db                             absent with --no-db
memory/{project|_global}/{name}.md
notes/{project|_global}/{slug}.md
```

`manifest.json` first is deliberate: a reader can refuse an archive - wrong
format, or a schema newer than its own binary understands - before extracting a
single byte. Every entry is a regular file with mode `0600` and uid/gid zeroed,
so a restore under another account carries no ownership from the exporting
machine.

**It is safe to run against a live instance.** The snapshot is SQLite's
`VACUUM INTO` on a handle that never migrates, so a newer binary cannot move the
schema under a running older daemon, and a write in flight is simply not in the
snapshot rather than half in it. The database is snapshotted *before* the trees
are walked, so the file set is a superset of what the snapshot's index describes;
the import's reconciliation heals that window.

A named destination is claimed with `O_EXCL` and written through a sibling
`.tmp` that is renamed into place, so an export never overwrites an existing
archive and never leaves a truncated one under a finished-looking name.

## seamlessd import {#seamlessd_import}

```bash
seamlessd import [--from DIR|FILE|-] [--embed=false]
                 [--skip LIST]              # v1 directory only
                 [--dry-run] [--force]      # archive only
```

Imports another store into this instance. **What `--from` names on disk decides
which of two unrelated operations runs** - never a flag:

| `--from` is | Source | What happens |
|---|---|---|
| a directory (the default `~/.seam`) | A Seam v1 data directory | Memory and note files are written and indexed here; trials, sessions, and tool-call events are inserted into the database. |
| a regular file, or `-` | A `seamlessd export` archive | A **restore** into an empty data directory, or a **merge** into a populated one. |

| Flag | Default | Applies to | Meaning |
|---|---|---|---|
| `--from` | `~/.seam` | both | Source path. A leading `~` expands; `-` reads an archive from stdin. |
| `--embed` | `true` | both | Embed imported items for cosine search, using the configured provider. |
| `--skip` | `briefings` | v1 directory | Comma-separated storage projects to skip. |
| `--dry-run` | `false` | archive | Report what the import would do and change nothing. |
| `--force` | `false` | archive | Allow a fresh restore while a daemon is still answering for this data directory. |

A flag from the other family is an **error**, not an ignored no-op: `--dry-run`
against a v1 directory would otherwise read as "previewed, nothing happened"
while the import actually ran.

### Importing a v1 directory

**Idempotent by id**, so re-running imports only what is new - which makes a
delta re-import safe after the first pass. It honours SIGINT/SIGTERM, and prints
a report even when the import ends in an error. With `--embed` on and no usable
embedder, it warns and imports without vectors rather than failing.

### Importing an archive

Restore-or-merge is a property of the **destination**, printed in the report and
never overridable. There is no `restore` verb and no `--mode`: the only thing an
override could do is replace a populated instance's database.

- **Fresh** (restore) when the data directory is absent, or holds neither
  `seam.db` nor `seam.db-wal` and no regular non-dot file in either tree. The
  markdown is restored byte-for-byte and the snapshot is renamed into place last,
  so an interrupted restore leaves files and no database - which the next run
  reads as fresh again and repeats cleanly.
- **Merge** otherwise, first-writer-wins by ULID: an item or row whose id is
  already here is skipped, which makes re-merging the same archive a no-op.

A fresh restore replaces `seam.db` wholesale, so it **refuses while a daemon is
answering** for that data directory and tells you to run `seamlessd stop`;
`--force` overrides. A merge and a `--dry-run` need no such thing - a merge
writes through the same files layer the daemon uses, and a dry run writes
nothing.

What a merge does **not** touch:

| Not merged | Why |
|---|---|
| `settings` | Repo paths, families, briefing overrides, and the embedder switch describe *this* machine. |
| `embeddings` | Vectors belong to whichever model this instance runs; imported items are embedded on write instead. |
| `*_index`, `fts`, `retrieval_stats`, `jobs` | Rebuildable mirrors, refreshed by the import itself. |

Collisions are **reported, never resolved**. A corpus file whose path is already
held by a different item is left unwritten and both ids are named; a
`sessions.name` or `projects.slug` already taken is reported rather than renamed,
because minting a new name would invent an identifier nothing refers to. The run
still exits 0 - the report is the work item.

When the archive's `embedding_models` differ from this instance's embedder, the
report says so and points at the console's re-embed
(Settings → Embeddings). Vectors from two models are not comparable, so that
mismatch does not heal itself.

## seamlessd install-hooks {#seamlessd_install_hooks}

```bash
seamlessd install-hooks [--client claude|claude-desktop|codex|all|detect] [--settings PATH] [--codex-hooks PATH] [--desktop-config PATH] [--url BASE] [--server-url URL] [--api-key KEY] [--seam PATH] [--mcp=false] [--skills=false]
```

Wires the selected install target(s) to Seamless: merges the hook entries into
each hook client's file (Claude Code `settings.json`, Codex `hooks.json`),
registers the MCP server (via the client's CLI, or for the Claude app chat
surface by editing `claude_desktop_config.json` directly), and installs the
embedded skills into each hook client's skill home - `seam-onboard` always, and
`seam-research` only while its
[optional feature](/reference/console/#optional-features) is on. A skill whose
feature is off is not installed, and a copy this installer previously delivered
is removed, so an agent never reads about tools the server no longer exposes.
The `claude-desktop` target is the chat surface's MCP bridge only -
it has no hooks and no skills - so selecting only it together with
`--mcp=false` is an error rather than a silent no-op.

| Flag | Default | Meaning |
|---|---|---|
| `--client` | `detect` | Which target(s) to wire: `claude`, `codex`, `claude-desktop`, a comma list of those (`claude,claude-desktop`), `all` (every target this platform can host), or `detect` (the targets present on this machine). With the flag omitted on a terminal, a multi-select menu prompts, defaulting to the detected set; non-interactive runs detect without prompting. With nothing detected, `detect` is an error, never a silent Claude Code default. |
| `--settings` | `~/.claude/settings.json` | Target Claude Code settings file, created if absent. Point it at a project-scoped `.claude/settings.json` to scope the hooks to one repo. |
| `--codex-hooks` | `$CODEX_HOME/hooks.json`, else `~/.codex/hooks.json` | Target Codex hooks file, created if absent. |
| `--desktop-config` | the app's per-OS location | Claude app `claude_desktop_config.json` to register the chat-surface bridge in (macOS `~/Library/Application Support/Claude/`, Windows `%APPDATA%\Claude\`). |
| `--url` | derived from the config addr | Base URL of the daemon, for this run only. It does not change the config file. |
| `--server-url` | none | Install as a **client** of the `seamlessd` at this base URL: writes `role: client`, `server_url`, and `mcp.api_key` into `~/.config/seamless/seamless.yaml` on first run, and wires every client against that URL. |
| `--api-key` | `$SEAMLESS_MCP_API_KEY` | That server's bearer key, for `--server-url`. On its own it is an error, not a silent no-op - a server reads its own key from its config file. |
| `--seam` | sibling of this binary, else `seam` on PATH | Path to the `seam` CLI baked into the command hooks. |
| `--mcp` | `true` | Register the MCP server via the client CLI (`claude mcp add-json --scope user` / `codex mcp add`). |
| `--skills` | `true` | Install the embedded skills for each wired client. A failure here degrades to a warning - skills must not cost the daemon bootstrap. |

It generates `mcp.api_key` on a true first run under the same rule as `serve`,
and refuses to run when an existing config leaves the key empty, since the key
is what the hooks authenticate with. The loaded config path is made absolute
and passed to command hooks as `--config`, so they resolve config from any
working directory. A `--seam` binary that cannot be found is a printed warning,
not an error - the hooks would fail at fire time, so it says so now.

With `--server-url`, that first-run step writes a *client* config instead -
`role: client`, `server_url`, `mcp.api_key`, and deliberately no `data_dir` -
and the run wires every selected target against the server's URL. The same
bootstrap rule applies: a config file already in the search order is never
edited, and the error names the exact lines to add by hand. The one file that
is not an error is one that already says exactly this, which is what keeps
re-running the installer from failing. No key is generated, because a client's
key belongs to the server it dials: it comes from `--api-key`, else
`$SEAMLESS_MCP_API_KEY`, and neither is an error. A client also has no local
database to read the [optional features](/reference/console/#optional-features)
from, so it asks the server over HTTP and falls back to its file/env config
with a warning. The run closes by naming the server it is now a client of.

Under an **https** base URL the wiring changes shape on purpose: every Claude
Code hook becomes a command hook (including `UserPromptSubmit`, otherwise the
one http hook) and the MCP registration becomes the `seam mcp-proxy` stdio
bridge. Claude Code performs http hooks and http MCP connections with its own
client, which has nowhere to be told about `tls.ca_file`; routing both through
`seam` puts them on the CLI's trust store. Under http the shapes are unchanged.

The hook file is written before MCP registration. Claude Code registration stays
best-effort because its current CLI exposes no machine-readable state: a missing
CLI or failed `mcp add-json` prints the exact command to run yourself. Codex is
stricter. It decodes `codex mcp get seamless --json`, leaves an exact enabled
stdio bridge unchanged, repairs an owned disabled/stale bridge with `mcp add`,
and re-reads it before reporting success. A direct-HTTP or other incompatible
entry under `seamless` is not overwritten; remove it explicitly or rerun with
`--mcp=false` to keep that manual transport.

The `claude-desktop` target has no management CLI at all, so its registration
is a merge-preserving edit of `claude_desktop_config.json`: the reserved
`seamless` entry is set to the `seam mcp-proxy` stdio bridge (absolute paths,
no secret - the bridge reads the bearer key from Seamless's config), every
foreign key round-trips byte-for-byte, the file is backed up once before the
first change, and the write is verified by re-reading it. An incompatible entry
under the reserved name is an error naming the in-app fix (Settings >
Developer > Edit Config), and every change ends with a restart notice - the
app reads the file only at startup. See
[Claude app chat setup](/claude-app/).

The hook merge preserves unknown keys and foreign entries, replaces marked stale
definitions, adopts only recognizable legacy Seamless URLs/commands, deduplicates
owned entries, and backs the file up once before the first change. An arbitrary
executable merely containing `hook <event>` is foreign. An already-current file
is reported as up to date and left untouched. Each event reports `added`,
`updated`, `adopted`, `deduped`, or `unchanged`.

For Claude Code, seven events are installed together: `SessionStart`,
`UserPromptSubmit`, `SessionEnd`, `PostToolUse`, `SubagentStart`,
`SubagentStop`, and
`PermissionRequest`. All are command hooks that run `seam hook <event>` (exec
form, no shell) except `UserPromptSubmit`, which is an http hook - Claude Code
will not run an http hook for SessionStart at all, and at SessionEnd a
fire-and-forget request races process teardown, so the findings harvest would
often be lost. The Codex profile is five shell-string command hooks. Both
profiles include safe constraint injection and parent-only lifecycle handling
for subagents; the [hooks reference](/reference/hooks/) has both tables.

## seamlessd client-config {#seamlessd_client_config}

```bash
seamlessd client-config [--redact]
```

Runs on the **server** and prints exactly what you paste on another machine to
make it a client of this daemon. It reads the resolved config and formats it:
it mutates nothing, opens no connection, and contacts no network.

The output is the server URL, the bearer key, the minimum client version, and
three paste-able commands - the macOS/Linux installer one-liner
(`SEAMLESS_SERVER_URL` + `SEAMLESS_MCP_API_KEY`), the PowerShell equivalent,
and the manual `install-hooks --server-url ... --api-key ...` form for a
machine that already has the binaries. `--redact` substitutes a placeholder for
the key so the block is safe to paste into a ticket or a chat; the shape of
every command is unchanged.

Every line it prints carries a URL and a key, so the refusals matter more than
the output. It errors rather than printing a command that looks right and
cannot work:

| State | Why it refuses |
|---|---|
| `role: client` | This install has no clients of its own to pair. The message names the server to run it on instead. |
| `mcp.api_key` empty | A client would have nothing to authenticate with. It names the config file and `openssl rand -hex 32`. |
| `server_url` resolves to loopback | Pasted on a second machine that command dials *that* machine's port 8081 and fails as a connection error from the wrong end of the network. It names the fix (`server_url` plus `addr: 0.0.0.0:8081`, then restart) and points out that a second user **on the same box** needs none of that and can pair against `http://127.0.0.1:8081` directly. |

Plain `http://` is a warning, not a refusal - it is a legitimate choice on a
trusted LAN, and the operator already had to widen the bind and name
`server_url` to get here. What it must not be is silent: the key in those
commands, and every memory fetched with it, cross the network in the clear.

See [Share one daemon across a LAN](/guides/network-install/) for the whole
procedure.

## seamlessd uninstall {#seamlessd_uninstall}

```bash
seamlessd uninstall [--client claude|claude-desktop|codex|all|detect] [--dry-run] [--yes] [--purge]
```

Reverses a full install on any supported OS: stops and removes the per-user
service, strips the Seamless hook entries, deregisters the MCP server
(including the chat surface's `claude_desktop_config.json` entry - only the
reserved `seamless` key is removed, everything else stays byte-for-byte),
removes the installed skills, and deletes the binaries. Config and the data dir
(`~/.seamless` - memories and notes are markdown that outlive the program) are
kept unless `--purge` is passed. Every external step is best-effort: an
already-gone file or a missing client CLI is a note, never a failure, so
uninstall is idempotent and safe to re-run.

Hook removal uses the installer's same classifier: current, marked-stale, and
recognizable legacy Seamless definitions are removed; foreign definitions are
preserved. Skill removal is scoped to `seam-onboard`, `seam-research`, and the
one-shot delivery marker in each selected client's skill root.

Under `role: client` two steps narrow rather than run. The service block reports
`not installed` and no teardown happens - a client registered none, and running
the teardown anyway would stop the daemon of a *server* sharing the same box.
And `--purge` deletes only the config directory: the `~/.seamless` a client's
config resolves to is a default it never wrote, and on a box converted from a
server it is the server's corpus.

| Flag | Default | Meaning |
|---|---|---|
| `--client` | `all` | Which target(s) to remove hooks/MCP/skills for: `claude`, `codex`, `claude-desktop`, a comma list, `all`, or `detect`. `claude-desktop` scopes the run to the chat surface's desktop-config entry. |
| `--dry-run` | `false` | Print what would be removed and exit without changing anything. |
| `--yes` | `false` | Skip the confirmation prompt. |
| `--purge` | `false` | Also delete the config dir (`~/.config/seamless`) and data dir (`~/.seamless`). |
| `--settings` | `~/.claude/settings.json` | Claude Code settings file to remove hooks from. |
| `--codex-hooks` | `$CODEX_HOME/hooks.json`, else `~/.codex/hooks.json` | Codex hooks file to remove hooks from. |
| `--desktop-config` | the app's per-OS location | Claude app `claude_desktop_config.json` to remove the chat-surface bridge from. |
| `--url` | derived from the config addr | Base URL the hook entries were installed with. |
| `--install-dir` | `$SEAMLESS_INSTALL_DIR`, else `~/.local/bin` | Directory the binaries were installed to. |
| `--mcp` | `true` | Also deregister the MCP server (`claude`/`codex mcp remove`). |

## seamlessd update {#seamlessd_update}

```bash
seamlessd update [--check] [--dry-run] [--url URL]
```

Upgrades Seamless in place to the latest published release by re-running the
canonical installer for this OS - the same script a fresh
`curl ... | sh` / `irm ... | iex` install runs, fetched from the latest GitHub
release's assets. There is deliberately no second upgrade implementation:
after verification, `update` pipes that script to `sh` (or `powershell`), so
binaries are swapped by rename, the service restarts on the new build, and
hooks are reconciled exactly as [Install & deploy](/install/) describes for a
re-run.

Before anything executes, the fetched script is verified against the
**Sigstore bundle** published alongside it - proof the bytes came out of this
repository's release workflow on a version tag, not merely from the right
host. Verification failure is fatal, with no fallback. The fetch is
HTTPS-only, including every redirect hop.

| Flag | Default | Meaning |
|---|---|---|
| `--check` | `false` | Report installed vs latest release version and exit without changing anything. |
| `--dry-run` | `false` | Print the source URL, signature status, and the equivalent hand-run one-liner, without fetching or executing. |
| `--url` | the latest release's installer asset | Override the installer URL. A custom URL carries no Sigstore bundle, so it runs TLS-only with a printed warning. |

The installer's env knobs pass through: `SEAMLESS_VERSION=0.3.0 seamlessd
update` pins a version, `SEAMLESS_INSTALL_DIR=...` retargets, exactly as the
curl installer does.

## seamlessd map-repo {#seamlessd_map_repo}

```bash
seamlessd map-repo --project SLUG [--path DIR]
```

Adds an entry to the `repo_project_map` setting, so an agent whose working
directory is under that path resolves to that project - in the hooks and in
`session_start`. This is what makes a briefing arrive scoped to the right
project without the agent passing `project` anywhere.

Mostly you will not need it: a git repo maps itself on its first session, taking
the slug from the repo root's directory name. Run `map-repo` to override that
derived slug, or to map a directory that is not a git repo.

`--project` is required. `--path` defaults to the current directory and is made
absolute. The command also ensures the project exists, so mapping a new slug
registers it. Writes straight to the database; no running daemon needed.

## seamlessd family {#seamlessd_family}

```bash
seamlessd family list
seamlessd family add <name> <slug> [<slug>...]
seamlessd family remove <name> [<slug>...]
```

Manages the `project_families` setting: named groupings whose members surface
each other's recent findings in briefings. Use it when two projects are really
one body of work and an agent in either should see what happened in the other.

Members are **project slugs, not repo paths** - resolve a repo to its slug with
`map-repo` first. `remove` with no slugs removes the whole family; with slugs it
removes just those members. `rm` is accepted as an alias for `remove`.

Adding a slug that is not yet a registered project prints a warning but
succeeds: the membership starts taking effect once an agent opens that repo and
registers it.

## seamlessd console-open {#seamlessd_console_open}

```bash
seamlessd console-open [--browser APP]
```

Opens the console in a browser, already authenticated. It renders a one-shot
self-submitting login page to a `0600` temp file and opens it; the page POSTs
the static key to the console's login endpoint, which sets the session cookie
and redirects into the console - so you land on an authenticated page without
pasting a key.

`--browser` targets a specific browser application (for example
`"Google Chrome"`, so an agent driving Chrome gets the auth cookie even when
another browser is the default). It is **macOS only** and is rejected with an
error on other platforms rather than silently opening the default browser.

It refuses to run when `mcp.api_key` is empty, or when the server does not
answer `/healthz` within two seconds - the page has nowhere to POST otherwise.
Any HTTP response counts as reachable, including a degraded 503.

## seamlessd start / stop / restart / status {#seamlessd_service}

```bash
seamlessd start      # or: stop | restart | status
```

Control the installed background service without remembering each platform's
service manager. `start`, `stop`, and `restart` act on the LaunchAgent (macOS),
the systemd `--user` unit (Linux), or the Scheduled Task (Windows); `status`
prints that manager's own state output.

These control an **already-installed** service - they do not create one. If it
was never installed, they exit with a hint to run the installer (or `make
install` from a clone) rather than a cryptic launchctl/systemctl/schtasks error.

`restart` is in-place and fast (on macOS `launchctl kickstart -k`, falling back
to a fresh load if the job was unloaded). `stop` fully stops the service - on
macOS the LaunchAgent has `KeepAlive`, so this unloads it rather than letting it
be resurrected. Idempotent no-ops - starting a running service, stopping a
stopped one - are reported as a note, not a failure.

From a clone, `make start` / `stop` / `restart` / `status` wrap these exactly.

## seamlessd version {#seamlessd_version}

```bash
seamlessd version
```

Prints the version, commit, and build date. `-v` and `--version` are aliases.

Commit and build date are link-time metadata set by the Makefile; a plain
`go build` leaves them `unknown`. The same version string appears in `/healthz`,
the MCP handshake, and the startup log - compare them when you suspect the
daemon is running older code than what you just built.
