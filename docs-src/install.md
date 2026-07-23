---
title: Install & deploy
description: The install layout, the per-user service (launchd, systemd, or a Scheduled Task), upgrading, uninstalling, and the security posture you are accepting.
---

Installing Seamless is one command: a script downloads a release archive
containing the `seamlessd` daemon and `seam` CLI for macOS, Linux, or Windows,
verifies its SHA-256, and registers the daemon as a per-user service - launchd,
systemd, or a Scheduled Task - bound to loopback. No Docker, database server,
or Go toolchain. The [Quickstart](/quickstart/) runs that command and moves on;
this page is what it did, and what to do when you want to steer it yourself.

## One instance per machine

There is **one instance**: port `8081`, data dir `~/.seamless`. One daemon, one
database, one set of files.

Both are config keys, not fixed facts - set `addr:` and `data_dir:` in
`~/.config/seamless/seamless.yaml` (or the `SEAMLESS_ADDR` / `SEAMLESS_DATA_DIR`
env overrides) and restart the service. The config is the single source of truth
for the bind address: the installer and the Makefile both read the port back out
of it, so the health check follows your change rather than assuming `8081`.
Nothing bakes the address into the service - which is why editing `addr:` works
instead of being silently overridden.

## Install in one command

```bash
curl -fsSL https://thereisnospoon.org/install | sh
```

On Windows, run the [PowerShell installer](https://thereisnospoon.org/install.ps1)
instead - it does the same steps with a Scheduled Task in place of launchd/systemd:

```powershell
irm https://thereisnospoon.org/install.ps1 | iex
```

This is the path for using Seamless (as opposed to working on it). The POSIX
script needs `curl` and `tar`; the PowerShell one needs nothing beyond Windows
itself. No Go toolchain is involved. In order, it:

1. resolves the latest release and downloads the archive for your platform
   (macOS, Linux, and Windows; amd64 and arm64), **verifying its SHA-256**
   against the release's `checksums.txt` before unpacking anything; when
   `cosign` is available it also verifies that manifest's keyless signature
   against this repository's release workflow identity;
2. installs `seamlessd` and `seam` into `~/.local/bin`;
3. runs `seamlessd install-hooks`, which generates the bearer key into
   `~/.config/seamless/seamless.yaml` on first run, detects Claude Code,
   Codex, and the Claude app chat surface, and installs that set's hooks, MCP
   registrations, and skills;
4. installs and starts the per-user service - launchd on macOS, systemd
   `--user` on Linux, an at-logon Scheduled Task on Windows - and polls
   `/healthz` until the daemon actually answers.

Step 3 detects three install targets - **Claude Code**, **Codex**, and the
**Claude app chat surface** (`claude-desktop`, the app's `mcpServers` bridge;
it has no hooks or skills) - and wires the detected set. That one selection
drives hooks, MCP registrations, and the maintained `seam-onboard` /
`seam-research` skills together. On a terminal the run confirms the selection
with a multi-select menu - answers are numbers or names, comma-separated
(`1,3`), defaulting to the detected set; headless, the detected set is wired
as-is. With nothing detected, a run on a terminal warns and asks whether to
install at all (defaulting to no), and a headless run aborts - the installer
never silently wires a client that is not there. Set `SEAMLESS_CLIENT` to make
the choice explicit: one target, a comma list, or `all` (every target the
platform can host - the chat surface exists only where the Claude app runs, so
`all` never fails on Linux over it). See [Codex local setup](/codex-cli/) for
the shared app/CLI/IDE profile and Codex's trust gate, and
[Claude app chat setup](/claude-app/) for what the chat surface does and does
not get.

On every OS, Claude's copies live under `$HOME/.claude/skills`; Codex's live
under `$CODEX_HOME/skills` when set, otherwise `$HOME/.codex/skills` (the same
paths are `%USERPROFILE%`-relative on Windows). Invoke `/seam-onboard` in Claude
Code or `$seam-onboard` in Codex. The skill asks before adding its marked block
to global/project `CLAUDE.md` or `AGENTS.md`; it never silently edits either.

The Windows installer is per-user by the same principle as the others: it runs
as **you**, never elevates, and registers the Scheduled Task under your own
account (`LogonType Interactive`), so a single signed-in user is all it needs and
no administrator prompt ever appears. `~/.config/seamless` and `~/.seamless`
resolve under `%USERPROFILE%`, exactly the paths the daemon already searches.

Re-running it upgrades in place: binaries are swapped by rename (safe while the
daemon holds them open), the service restarts on the new build, and your config
and `~/.seamless` are preserved. The selected clients are reconciled to those
new stable paths: owned stale hooks and the Codex stdio registration are
repaired, current definitions are untouched, foreign hooks are preserved, and
the recurring skill is refreshed. It is [one shell
script](https://thereisnospoon.org/install) with no dependencies to audit.

| Override | Effect |
|---|---|
| `SEAMLESS_VERSION=0.3.0` | install that version instead of the latest |
| `SEAMLESS_INSTALL_DIR=~/bin` | put the binaries somewhere else |
| `SEAMLESS_CLIENT=claude\|codex\|claude-desktop\|all` | choose which target(s) to wire instead of auto-detection; comma lists work (`claude,claude-desktop`) |
| `SEAMLESS_NO_HOOKS=1` | skip agent hooks, MCP registration, and skills |
| `SEAMLESS_NO_ONBOARD_SKILL=1` | skip the selected client(s)' one-shot onboarding skill |
| `SEAMLESS_NO_RESEARCH_SKILL=1` | skip the selected client(s)' recurring research skill |
| `SEAMLESS_NO_SERVICE=1` | install the binaries only; run `seamlessd serve` yourself |
| `SEAMLESS_ALLOW_ROOT=1` | permit running as root (single-user containers) |

Set them ahead of the shell, not the curl:
`curl -fsSL https://thereisnospoon.org/install | SEAMLESS_VERSION=0.3.0 sh`. On
Windows the same knobs are environment variables you set before the pipe -
`$env:SEAMLESS_VERSION='0.3.0'; irm https://thereisnospoon.org/install.ps1 | iex`
- with the one exception of `SEAMLESS_ALLOW_ROOT`, which is POSIX-only (the
Windows task is per-user by construction, so there is no root case to allow).

Everything here is per-user by construction - `~/.local/bin`, `~/.config`,
`~/.seamless`, a user service - so run it as yourself. Under `curl | sudo sh` it
would all land in root's home where your agents will never look, which is why
the script refuses root unless you insist.

## Homebrew

```bash
brew install 0spoon/tap/seamless
```

Every release publishes a cask to the `0spoon/homebrew-tap` tap, on macOS and
Linux alike. It delivers the **binaries only** - `seamlessd` and `seam` on your
PATH, with the Gatekeeper quarantine attribute stripped on macOS (the release
binaries are unsigned). It does not wire clients or register a service, so
finish with the two commands the cask's caveats print:

```bash
seamlessd install-hooks   # bearer key on first run, hooks, MCP, skills
seamlessd serve           # or set up the service yourself
```

`brew upgrade` moves you to the latest release. The hooks keep working - they
resolve `seam` through brew's stable bin path - but restart the daemon
yourself so it runs the new build.

## Install from a clone

```bash
make install                    # -> ~/.local/bin + ~/.config/seamless/seamless.yaml
make install PREFIX=/opt/seam   # custom prefix (binaries land in $PREFIX/bin)
make uninstall                  # remove service, hooks, MCP, skills + binaries (data kept)
```

`make install` is the same destination from your own build, and it is macOS-only
(it renders the launchd plist from `deploy/launchd/`). It snapshots the binaries
and config to stable locations, then points launchd **and** the selected clients'
hooks/MCP definitions at the copies. Nothing live resolves through your working
tree, so `make build`, a branch switch, and a moved or cleaned repo cannot change
what the running daemon and every agent's hooks execute. Swapping them is `make
install`, deliberately.

The config lands in `~/.config/seamless/`, one of the paths Seamless already
searches ahead of `./seamless.yaml`, so the hooks resolve it from any directory.
It is seeded **only when absent** - an install never clobbers a config holding
your bearer key. Delete it to re-seed.

The remaining routes end up in the same place with less done for you:
`go install github.com/0spoon/seamless/cmd/...@latest` needs Go 1.25+, and the
[GitHub releases](https://github.com/0spoon/seamless/releases) carry the same
prebuilt archives the installer fetches. From a bare binary, `seamlessd serve`
covers the essentials - first run seeds the config - and `seamlessd install-hooks`
wires the detected Claude Code/Codex clients; what you take on yourself is the
service.

## Iterating on Seamless itself

`make install` is also the edit-test loop. When the rendered plist is unchanged -
the common case - it skips the launchd bootout/bootstrap and kickstarts the
service in place, so its marginal cost over `make build` is two file copies.

```bash
make build      # compile only; nothing live changes
make install    # ...and now it does
make logs       # follow ~/.seamless/seamlessd.log
```

That split is the point. `make build` on a half-finished edit is free, because
the daemon and hooks keep running the last thing you installed. Rebuilding is not
deploying.

## The service

It runs as **your** user, not root: it reads your config, writes your files, and
should die with your login session, not the machine.

Whatever the platform, one set of verbs controls it - they resolve your OS's
service manager for you:

```bash
seamlessd start       # start | stop | restart | status
make start            # the same, from a clone (start | stop | restart | status)
```

These act on the already-installed service and print a hint if it was never
installed. The platform-native commands below are what they wrap.

On **macOS** it is a user LaunchAgent labelled `org.thereisnospoon.seamless` in
`~/Library/LaunchAgents/`, logging to `~/.seamless/seamlessd.log`; `make logs`
follows that log. Underneath, the verbs above wrap
`launchctl print gui/$(id -u)/org.thereisnospoon.seamless`,
`launchctl kickstart -k gui/$(id -u)/org.thereisnospoon.seamless`, and
`launchctl bootout gui/$(id -u)/org.thereisnospoon.seamless`.

On **Linux** the installer writes a systemd user unit to
`~/.config/systemd/user/seamless.service` and enables lingering, so the daemon
starts at boot rather than at your next login:

```bash
systemctl --user status seamless      # state, pid, last exit
journalctl --user -u seamless -f      # follow the log
systemctl --user restart seamless
systemctl --user stop seamless
```

No systemd user session (some containers, WSL1)? The installer says so and skips
the step; run `seamlessd serve` under whatever supervises processes there.

On **Windows** it is an at-logon Scheduled Task named `Seamless`, running as you
(`LogonType Interactive`, no admin), logging to `~/.seamless/seamlessd.log`. The
task action is a bare exec - `seamlessd.exe serve --config <path> --log-file
<path>` - because a task cannot carry the `SEAMLESS_CONFIG` env prefix a plist or
systemd unit does; the two flags pass exactly what that prefix would have:

```powershell
Get-ScheduledTask Seamless | Get-ScheduledTaskInfo   # state, last run, last result
Get-Content ~/.seamless/seamlessd.log -Wait          # follow the log
Restart-ScheduledTask Seamless                        # stop + start
Stop-ScheduledTask Seamless                           # stop (it restarts at next logon)
```

It restarts on failure and never hits the default execution time limit, so it
behaves like launchd's `KeepAlive`. Because it triggers at logon, it runs while
you are signed in and stops when you sign out - a single-user desktop, which is
the shape Seamless is built for.

## Upgrading

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

From a clone, `make update` builds first and then runs that same command against
your installed copy (`make update CHECK=1` only reports). Note that both
`seamlessd update` and `make update` install the latest *release*, which may be
older than your clone's HEAD - to deploy the build from your working tree instead:

```bash
git pull
make check             # everything green before you swap the running daemon
make install           # swap it
make doctor            # confirm config + DB after the swap
```

Migrations apply automatically at startup - there is no separate migrate step.
Run `make doctor` afterwards anyway: it is the cheapest way to learn that the new
build disagrees with your config before an agent does.

`/healthz` reports the running build. If a change seems not to have taken effect,
check it before you debug anything else - a stale daemon still serving the old
binary looks exactly like a bug in the new one.

## Uninstalling

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
original backup sits next to each file. If you would rather do it by hand - a
bare binary you never installed a service for, say - the service teardown is
`launchctl bootout` / `systemctl --user disable --now` /
`Unregister-ScheduledTask -TaskName Seamless`, and `claude mcp remove seamless`
or `codex mcp remove seamless` drops that client's MCP registration. The chat
surface has no CLI: delete the `seamless` entry under `mcpServers` in the
Claude app (Settings > Developer > Edit Config) and restart it.

## Security posture

What you are accepting when you run this:

- **One static bearer key** guards `/api/mcp` and the console. Not JWT, not
  OAuth, no user accounts. It is a single-user local tool and the key is in your
  config file with `0600` permissions.
- **Default agent registrations do not copy that key.** Claude Code calls
  `seam mcp-headers` through `headersHelper`; Codex launches `seam mcp-proxy`.
  Both read the 0600 Seamless config at connection time, and neither puts the
  bearer value in client config or subprocess argv. A manual Codex direct-HTTP
  registration with `http_headers` does copy it into `config.toml`; use that
  tradeoff deliberately.
- **Loopback bind** by default (`127.0.0.1:8081`). Nothing off your machine can
  reach it.
- **SSRF guards on capture.** `capture_url` is the one tool that makes an
  outbound request on an agent's behalf, and its destination ports are restricted
  to `capture.allowed_ports` (80 and 443 by default) - never "any port".
- **No product telemetry.** Seamless sends no usage or analytics data. The
  outbound traffic is: calls to a configured OpenAI or Anthropic provider (use
  Ollama to keep model calls local), URLs an agent explicitly asks
  `capture_url` to fetch, and the GitHub release check and download that an
  explicit `seamlessd update` performs.
- **Release authenticity has two layers.** Every installer verifies the
  archive's SHA-256 against `checksums.txt`. When `cosign` is installed it also
  verifies the manifest's keyless signature against this repository's release
  workflow identity; without cosign it warns clearly and continues with checksum
  integrity only. `seamlessd update` separately verifies the fetched installer
  script's Sigstore bundle in-process before executing it. `curl | sh` still
  means trusting the bytes served by the site, so read the script first if that
  boundary is not acceptable; `go install` lands in the same place.

The key and loopback are a matched pair. A static bearer key is adequate
*because* the listener is on loopback; it would not be adequate on a public
interface. If you widen `addr` to a routable address, the key becomes the only
thing between the internet and your entire knowledge store - so don't. Put it
behind a tunnel (Tailscale, SSH forwarding, Cloudflare Tunnel) and leave the bind
on loopback.

See [Configuration](/reference/configuration/) for every key.
