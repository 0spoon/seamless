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
fixes land often, so update at least weekly to stay on the latest version.

`seamlessd update` is the one command, on every OS. It upgrades in place to the
latest release by re-running the canonical installer for you - so there is a
single upgrade path to trust, not a second copy of the download-and-swap logic
that could drift from the installer:

```bash
seamlessd update --check   # report installed vs latest, change nothing
seamlessd update --dry-run # print exactly what it would fetch and run
seamlessd update           # fetch the latest release and swap it in
```

It honors the same knobs as the installer, so `SEAMLESS_VERSION=0.3.0 seamlessd
update` pins a version and `SEAMLESS_INSTALL_DIR=... seamlessd update` retargets.
Under the hood it fetches the installer script (the PowerShell one on Windows)
from the latest release's assets together with the Sigstore bundle the release
workflow signed it with, verifies the signature in-process - the script must
have been produced by this repository's release workflow on a version tag, or
update refuses to run it - and then runs it. That is the same script as doing
it by hand, minus the signature check:

```bash
curl -fsSL https://thereisnospoon.org/install | sh
```

Your config and `~/.seamless` are preserved, binaries are swapped by rename
(safe while the daemon holds them open), and the service restarts on the new
build. The selected clients are reconciled to the new stable paths: owned
stale hooks and the Codex stdio registration are repaired, current definitions
are untouched, foreign hooks are preserved, and the recurring skill is
refreshed.

From a clone, `make update` builds first and then runs that same command against
your installed copy (`make update CHECK=1` only reports).

<details>
<summary>Deploying your working tree instead of a release</summary>

Both `seamlessd update` and `make update` install the latest *release*, which
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
