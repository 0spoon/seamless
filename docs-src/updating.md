---
title: Update & uninstall
description: The lifecycle journeys that are one command on every OS - seamlessd update swaps in the latest release, seamlessd uninstall reverses the install, and adding a client later is one more install-hooks run.
---

The install is one command, and so is everything after it. `seamlessd`
carries its own lifecycle: update, uninstall, and client wiring are the same
commands on macOS, Linux, and Windows, resolving your platform's service
manager and paths for you. The OS-specific detail all lives on
[The service & where things live](/reference/service/).

## Update

Seamless is early in its development cycle: releases with improvements and bug
fixes land often. A daemon installed from a release checks for them by itself
and tells you when one is out ([automatic update checks](#automatic-update-checks));
updating is then one command.

`seamlessd update` is the one command, on every OS. It upgrades in place to the
newest release by running that release's own installer for you - so there is a
single upgrade path to trust, not a second copy of the download-and-swap logic
that could drift from the installer:

```bash
seamlessd update --check   # report installed vs the newest release, change nothing
seamlessd update --dry-run # print what it would install, from where, and how; change nothing
seamlessd update           # install the newest release, confirm it, roll back if it fails
```

"Newest" is the highest version on GitHub's release list, never GitHub's
"latest" (a backport can make that an older one). It honors the installer's
knobs, and a knob you set wins: `SEAMLESS_VERSION=0.7.1 seamlessd update` pins a
release, older ones too, and every run passes the version it installs to the
installer explicitly.

Under the hood it re-reads the target release from GitHub, downloads that
release's installer script (the PowerShell one on Windows) and `checksums.txt`
with the Sigstore bundles the release workflow signed them with, and verifies
both in-process against the release workflow's identity on that release's exact
tag - so an older signed installer or manifest cannot pass as the one you asked
for. It then runs the installer pinned to the verified `checksums.txt`
(`SEAMLESS_CHECKSUMS_SHA256`). That is the same script as doing it by hand,
minus the signature checks:

```bash
curl -fsSL https://github.com/arctop/seamless/releases/download/v0.7.3/install | SEAMLESS_VERSION=0.7.3 sh
```

On an install the installer made, it also backs the instance up to
`~/.seamless/backups/pre-update-v<old>-<time>.tar.gz` (the newest two kept),
counts the update only once the new release answers `/healthz` as a freshly
started daemon that is still the same one ten seconds later, and otherwise
rolls back: the service stops, the database is put back from the backup if the
new release had already migrated it (the migrated file stays beside it as
`seam.db.pre-rollback-<id>`), and the release you had is reinstalled and
confirmed. Memories and notes are never rolled back. Each run is recorded in
`~/.seamless/update/` (`attempt.json`, `attempts.jsonl`, `logs/`). A run whose
installer failed after the new release came up reports "applied with warnings":
run `seamlessd install-hooks`. Already on the newest release, it says so and
changes nothing; pin the release to reinstall it. A source build
(`make update`), a client machine, or a release binary the service does not run
gets the verified installer without a backup, a rollback or a record, and the
output says why. Homebrew installs are refused with the `brew` command.

Your config and `~/.seamless` are preserved, binaries are swapped by rename
(safe while the daemon holds them open), and the service restarts on the new
build. The selected clients are reconciled to the new stable paths: owned
stale hooks and the Codex stdio registration are repaired, current definitions
are untouched, foreign hooks are preserved, and the recurring skill is
refreshed.

From a clone, `make update` builds first and then runs that same command against
your installed copy (`make update CHECK=1` only reports).

### Automatic update checks {#automatic-update-checks}

A daemon installed from a release checks GitHub for new releases by itself: a
few minutes after it starts, then about every 6 hours (`update.check_interval`).
It installs nothing. When a newer release is out, it tells you:

- New agent sessions get one line in their briefing, for a week after the
  release first shows up, worded as your action rather than the agent's:
  `Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for
  this session: seamlessd update`. A session on another machine of a
  [shared daemon](/guides/network-install/) is told the server can update.
- The console shows it on the Home health strip and in
  **Settings -> Updates**, with the newest release, when the daemon last
  checked, and the command for your install.
- `seamlessd doctor` reports it on its `updates` row, `seamlessd update --check`
  adds what the daemon's check is doing, and `seam version` / `seam status`
  print an extra line.

For a day after the daemon starts on a new version, new sessions hear
`Seamless updated to v0.7.3 (from v0.7.2)` with a link to the release notes,
and the console shows the same as a banner.

How you update depends on how you installed, and every notice names the right
command:

| Installed with | Update with |
|---|---|
| the installer (`curl ... \| sh` or `irm ... \| iex`) | `seamlessd update` |
| Homebrew | `brew upgrade --cask arctop/tap/seamless` |
| a clone (`make install`), `go install` or `go build` | `git pull && make install` |
| a client of a shared daemon | re-run the pairing commands `seamlessd client-config` prints on the server |

**What is sent.** One anonymous `GET` to
`https://api.github.com/repos/arctop/seamless/releases`, with a fixed
`User-Agent` (`seamlessd-update-check`), no token, no version, and no machine or
install identifier. It honors `HTTPS_PROXY` from the daemon's environment. The
answer is cached with its `ETag` in `~/.seamless/update/state.json`; a failed
check retries after 15 minutes, doubling up to the check interval, and a rate
limit is waited out. Releases still uploading their assets, drafts and
prereleases are ignored, and "newest" means the highest version, not the most
recently published.

**Turning it off.** In `seamless.yaml`:

```yaml
update:
  check: false
```

`false` means no update traffic at all, and it is final: the console shows its
toggle locked. With the key left out, **Settings -> Updates** turns checks on
and off at runtime, and `SEAMLESS_UPDATE_CHECK=false` does the same from the
service's environment. `seamlessd update --check` and `seamlessd update` work
either way - running them is you asking. The `update:` keys are new: a
seamlessd release from before them refuses a config that sets one, so setting
it ties that file to this release or newer.

Builds from source (`make install`, `go install`, `go build`) do not check unless
`update.check: true` is set, and never write the state file - a developer's
daemon, the test fixtures and CI stay off the network.

<details>
<summary>Deploying your working tree instead of a release</summary>

Both `seamlessd update` and `make update` install the newest *release*, which
may be older than your clone's HEAD. To deploy the build from your working
tree instead:

```bash
git pull
make check             # everything green before you swap the running daemon
make install           # swap it
make doctor            # confirm config + DB after the swap
```

</details>

Migrations apply automatically at startup - there is no separate migrate step.
Run `make doctor` afterwards anyway: it is the cheapest way to learn that the new
build disagrees with your config before an agent does.

`/healthz` reports the running build. If a change seems not to have taken effect,
check it before you debug anything else - a stale daemon still serving the old
binary looks exactly like a bug in the new one.

## Uninstall {#uninstall}

`seamlessd uninstall` is the one command, on every OS. It reverses the whole
install - stops and removes the per-user service, strips the Claude Code and
Codex hooks, deregisters the MCP server from both client CLIs and from the
Claude app's `claude_desktop_config.json`, removes both hook clients' installed
`seam-onboard` and `seam-research` packages/one-shot markers, and deletes the
binaries - and it is idempotent, so a second run is a clean no-op. Preview it
first with `--dry-run`:

```bash
seamlessd uninstall --dry-run   # print exactly what would be removed
seamlessd uninstall             # do it (asks to confirm on a terminal)
```

From a clone it is `make uninstall`, which builds first and then runs that same
command against your installed copy.

**Your knowledge is kept by default.** `~/.config/seamless` (your bearer key) and
`~/.seamless` (the database, and your memories and notes as markdown) are left in
place - the uninstall of a program should not delete your knowledge. Add
`--purge` (or `make uninstall PURGE=1`) only when you actually mean to delete
them; a guard refuses to purge a path that resolves to your home directory or the
filesystem root. See [Storage](/reference/storage/) for what is in there.

The hooks come out of `~/.claude/settings.json` and Codex's `hooks.json` through
the same exact classifier the installer and doctor use. Current, marked-stale,
and unmistakable legacy Seamless entries are removed; foreign entries survive,
even when their arguments happen to contain `hook <event>`. The install's
original backup sits next to each file.

If you would rather do it by hand - a bare binary you never installed a
service for, say - `claude mcp remove seamless` or `codex mcp remove seamless`
drops that client's MCP registration; the chat surface has no CLI, so delete
the `seamless` entry under `mcpServers` in the Claude app (Settings >
Developer > Edit Config) and restart it. The native service teardown commands
live with the rest of the OS-specific detail:
[Removing the service by hand](/reference/service/#removing-the-service-by-hand).

## Add or remove one client

Client wiring is independent per target, and adding one never disturbs
another - Claude's and Codex's skills live in separate homes with independent
delivery markers, and `install-hooks` touches only the target you name:

```bash
seamlessd install-hooks --client codex            # add Codex to an existing install
seamlessd install-hooks --client claude-desktop   # add the Claude app chat surface
seamlessd install-hooks                           # or re-run the interactive multi-select
```

Restart the client you added so it loads hooks, MCP, and skills; Codex
additionally gates new hook definitions behind
[/hooks approval](/codex-cli/#trust-the-hooks-once), and the Claude app reads
its config only at startup.

Removing one client's wiring while keeping Seamless installed is the manual
path: `claude mcp remove seamless` or `codex mcp remove seamless` deregisters
MCP, the managed hook entries (tagged `seamless_managed`) come out of
`~/.claude/settings.json` or `${CODEX_HOME:-~/.codex}/hooks.json`, and the
skills are directories you can delete. `seamlessd uninstall --client <name>`
also exists, but it scopes only which clients are un-wired during a **full**
uninstall - the service and binaries come out regardless.
