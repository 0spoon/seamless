# seamlessd CLI

> The daemon and operator CLI - serve, doctor, import, install-hooks, uninstall, update, map-repo, family, console-open, start/stop/restart/status, and version.

`seamlessd` is both the server and the operator CLI. `serve` runs the daemon;
every other subcommand is a one-shot that opens the same config and database
directly, without going through a running server. That means most of them work
whether or not the daemon is up - and that `map-repo` and `family` write state
the running daemon reads.

Each subcommand parses its own flags. None of them take positional arguments
except `family`, which takes only positionals.

For the keys every command below resolves, see
[Configuration](https://thereisnospoon.org/docs/reference/configuration/).

## seamlessd serve {#seamlessd_serve}

```bash
seamlessd serve [--addr HOST:PORT]
```

Starts the HTTP server and blocks until SIGINT or SIGTERM, then shuts down
gracefully. `--addr` overrides the configured bind address (default
`127.0.0.1:8081`).

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

Server-side self-checks. Each line reports `ok`, `warn`, or `fail`; **only a
`fail` exits non-zero** - warnings are informational. Checks stop early if
config or the database cannot be loaded at all.

| Check | What it reports |
|---|---|
| `binary` | The version that ran. |
| `config` | Which file it loaded, or that it fell back to defaults + env. |
| `data_dir` | The resolved data directory. |
| `mcp.api_key` | Set, or a warning that `/api/mcp` will reject everything. |
| `llm` | The provider, or a warning that its credential is missing. |
| `embedder` | Probes the embedder with a real embed call. Unreachable, unconfigured, or provider `anthropic` (no embeddings API) is a warning: recall degrades to FTS. |
| `database` | Path, schema version, and table count. Opens and migrates if needed. |
| `mcp_tools` | Fails if the number of registered tools disagrees with the expected count - catches a tool written but never wired in. |
| `claude CLI runtime` / `claude app runtime` | Each discoverable Claude Code runtime's self-reported version, separately: the PATH CLI and, on macOS, every runtime the desktop app has retained - they can differ, and collapsing them would hide exactly that skew. No discoverable runtime means no lines. |
| `hooks` | Claude Code definitions compared with today's desired profile. |
| `claude desktop mcp` | The chat surface's `claude_desktop_config.json` entry compared with the desired stdio bridge. Absent is an **info** line naming the opt-in command, never a nag; an exact entry reports OK while stating that the running app's loaded state is unverifiable (the app reads the config at startup). No lines when neither the app nor a desktop config exists. |
| `codex CLI runtime` / `codex app runtime` | Each discoverable Codex runtime's self-reported version, separately, on the same principle. |
| `codex hooks` | Codex current, stale, and missing definitions, including command targets. |
| `codex hook trust` | Always warns that trust is unverified and directs you to Codex `/hooks`; no private trust state is read. |
| `codex hook activity` | Last observed SessionStart/UserPromptSubmit event, if any; evidence only, not proof of current trust. |
| `codex mcp` | Exact enabled stdio bridge state from `codex mcp get seamless --json`, plus executable/config target existence. |
| `gardener` | The ticker configuration, or a warning that it is disabled. |

The definition checks compare current desired state, not mere existence. The
shared classifier recognizes exact current definitions, marked stale entries,
and only unmistakable legacy Seamless shapes; arbitrary foreign hooks survive.
Codex trust is a separate fact because Codex exposes no supported query for the
current trust decision. A recent hook observation cannot make a changed command
healthy.

Reach for it after changing config, after an upgrade, or as the first step when
recall has quietly gone lexical.

## seamlessd import {#seamlessd_import}

```bash
seamlessd import [--from DIR] [--skip LIST] [--embed=false]
```

Imports a Seam v1 data directory into this instance. Memory and note files are
written and indexed under the v2 data directory; trials, sessions, and tool-call
events are inserted into the database.

| Flag | Default | Meaning |
|---|---|---|
| `--from` | `~/.seam` | v1 data directory to import from. A leading `~` expands. |
| `--skip` | `briefings` | Comma-separated storage projects to skip. |
| `--embed` | `true` | Embed imported items for cosine search, using the configured provider. |

**It is idempotent by id**, so re-running imports only what is new - which makes
a delta re-import safe after the first pass. It honours SIGINT/SIGTERM, and
prints a report even when the import ends in an error. With `--embed` on and no
usable embedder, it warns and imports without vectors rather than failing.

## seamlessd install-hooks {#seamlessd_install_hooks}

```bash
seamlessd install-hooks [--client claude|codex|claude-desktop|all|detect] [--settings PATH] [--codex-hooks PATH] [--desktop-config PATH] [--url BASE] [--seam PATH] [--mcp=false] [--skills=false]
```

Wires the selected install target(s) to Seamless: merges the hook entries into
each hook client's file (Claude Code `settings.json`, Codex `hooks.json`),
registers the MCP server (via the client's CLI, or for the Claude app chat
surface by editing `claude_desktop_config.json` directly), and installs the
embedded `seam-onboard` and `seam-research` skills into each hook client's
skill home. The `claude-desktop` target is the chat surface's MCP bridge only -
it has no hooks and no skills - so selecting only it together with
`--mcp=false` is an error rather than a silent no-op.

| Flag | Default | Meaning |
|---|---|---|
| `--client` | `detect` | Which target(s) to wire: `claude`, `codex`, `claude-desktop`, a comma list of those (`claude,claude-desktop`), `all` (every target this platform can host), or `detect` (the targets present on this machine). With the flag omitted on a terminal, a multi-select menu prompts, defaulting to the detected set; non-interactive runs detect without prompting. With nothing detected, `detect` is an error, never a silent Claude Code default. |
| `--settings` | `~/.claude/settings.json` | Target Claude Code settings file, created if absent. Point it at a project-scoped `.claude/settings.json` to scope the hooks to one repo. |
| `--codex-hooks` | `$CODEX_HOME/hooks.json`, else `~/.codex/hooks.json` | Target Codex hooks file, created if absent. |
| `--desktop-config` | the app's per-OS location | Claude app `claude_desktop_config.json` to register the chat-surface bridge in (macOS `~/Library/Application Support/Claude/`, Windows `%APPDATA%\Claude\`). |
| `--url` | derived from the config addr | Base URL of the daemon. |
| `--seam` | sibling of this binary, else `seam` on PATH | Path to the `seam` CLI baked into the command hooks. |
| `--mcp` | `true` | Register the MCP server via the client CLI (`claude mcp add-json --scope user` / `codex mcp add`). |
| `--skills` | `true` | Install the embedded skills for each wired client. A failure here degrades to a warning - skills must not cost the daemon bootstrap. |

It generates `mcp.api_key` on a true first run under the same rule as `serve`,
and refuses to run when an existing config leaves the key empty, since the key
is what the hooks authenticate with. The loaded config path is made absolute
and passed to command hooks as `--config`, so they resolve config from any
working directory. A `--seam` binary that cannot be found is a printed warning,
not an error - the hooks would fail at fire time, so it says so now.

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
[Claude app chat setup](https://thereisnospoon.org/docs/claude-app/).

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
for subagents; the [hooks reference](https://thereisnospoon.org/docs/reference/hooks/) has both tables.

## seamlessd uninstall {#seamlessd_uninstall}

```bash
seamlessd uninstall [--client claude|codex|claude-desktop|all|detect] [--dry-run] [--yes] [--purge]
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
hooks are reconciled exactly as [Install & deploy](https://thereisnospoon.org/docs/install/) describes for a
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
