---
title: Install & deploy
description: The install layout, the launchd service, upgrading, uninstalling, and the security posture you are accepting.
---

The [Quickstart](/quickstart/) gets Seamless running in one command. This page is
what that command did, and what to do when you want to steer it yourself.

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

This is the path for using Seamless (as opposed to working on it). It needs
`curl` and `tar`; no Go toolchain is involved. In order, it:

1. resolves the latest release and downloads the archive for your platform
   (macOS and Linux, amd64 and arm64), **verifying its SHA-256** against the
   release's `checksums.txt` before unpacking anything;
2. installs `seamlessd` and `seam` into `~/.local/bin`;
3. runs `seamlessd install-hooks`, which generates the bearer key into
   `~/.config/seamless/seamless.yaml` on first run, installs the Claude Code
   hooks, and registers the MCP server;
4. installs and starts the per-user service - launchd on macOS, systemd
   `--user` on Linux - and polls `/healthz` until the daemon actually answers.

Re-running it upgrades in place: binaries are swapped by rename (safe while the
daemon holds them open), the service restarts on the new build, and your config,
your hooks, and `~/.seamless` are left alone. It is [one shell
script](https://thereisnospoon.org/install) with no dependencies to audit.

| Override | Effect |
|---|---|
| `SEAMLESS_VERSION=0.3.0` | install that version instead of the latest |
| `SEAMLESS_INSTALL_DIR=~/bin` | put the binaries somewhere else |
| `SEAMLESS_NO_HOOKS=1` | skip the Claude Code hooks and MCP registration |
| `SEAMLESS_NO_SERVICE=1` | install the binaries only; run `seamlessd serve` yourself |
| `SEAMLESS_ALLOW_ROOT=1` | permit running as root (single-user containers) |

Set them ahead of the shell, not the curl:
`curl -fsSL https://thereisnospoon.org/install | SEAMLESS_VERSION=0.3.0 sh`.

Everything here is per-user by construction - `~/.local/bin`, `~/.config`,
`~/.seamless`, a user service - so run it as yourself. Under `curl | sudo sh` it
would all land in root's home where your agents will never look, which is why
the script refuses root unless you insist.

## Install from a clone

```bash
make install                    # -> ~/.local/bin + ~/.config/seamless/seamless.yaml
make install PREFIX=/opt/seam   # custom prefix (binaries land in $PREFIX/bin)
make uninstall                  # remove service + binaries (config kept)
```

`make install` is the same destination from your own build, and it is macOS-only
(it renders the launchd plist from `deploy/launchd/`). It snapshots the binaries
and config to stable locations, then points launchd **and** the Claude Code hooks
at the copies. Nothing live resolves through your working tree, so `make build`,
a branch switch, and a moved or cleaned repo cannot change what the running
daemon and every agent's hooks execute. Swapping them is `make install`,
deliberately.

The config lands in `~/.config/seamless/`, one of the paths Seamless already
searches ahead of `./seamless.yaml`, so the hooks resolve it from any directory.
It is seeded **only when absent** - an install never clobbers a config holding
your bearer key. Delete it to re-seed.

The other two routes end up in the same place with less done for you:
`go install github.com/0spoon/seamless/cmd/...@latest` needs Go 1.25+, and the
[GitHub releases](https://github.com/0spoon/seamless/releases) carry the same
prebuilt archives the installer fetches. From a bare binary, `seamlessd serve`
covers the essentials - first run seeds the config - and `seamlessd install-hooks`
wires Claude Code; what you take on yourself is the service.

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

On **macOS** it is a user LaunchAgent labelled `org.thereisnospoon.seamless` in
`~/Library/LaunchAgents/`, logging to `~/.seamless/seamlessd.log`. From a clone,
the Makefile wraps launchctl:

```bash
make service-status    # is it loaded, what pid, last exit
make logs              # follow ~/.seamless/seamlessd.log
make restart-service   # restart in place
make stop-service      # unload (KeepAlive will not resurrect an unloaded job)
```

Without a clone, that is `launchctl print gui/$(id -u)/org.thereisnospoon.seamless`,
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

## Upgrading

Installed with the one-command installer? Re-run it - that *is* the upgrade:

```bash
curl -fsSL https://thereisnospoon.org/install | sh
```

From a clone:

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

From a clone, `make uninstall` removes the service and the binaries. By hand it
is three lines - stop the service, remove its definition, remove the binaries:

```bash
# macOS
launchctl bootout gui/$(id -u)/org.thereisnospoon.seamless
rm ~/Library/LaunchAgents/org.thereisnospoon.seamless.plist
rm ~/.local/bin/seamlessd ~/.local/bin/seam

# Linux
systemctl --user disable --now seamless
rm ~/.config/systemd/user/seamless.service
rm ~/.local/bin/seamlessd ~/.local/bin/seam
```

Claude Code keeps its own registrations: `claude mcp remove seamless --scope user`
drops the MCP server, and the hooks come out of `~/.claude/settings.json` (the
install backed up the original next to it).

None of this touches `~/.seamless` or your config. Your memories and notes are
markdown files; the uninstall of a program should not delete your knowledge.
Remove the directory by hand if you actually mean it - and see
[Storage](/reference/storage/) first for what is in there.

## Security posture

What you are accepting when you run this:

- **One static bearer key** guards `/api/mcp` and the console. Not JWT, not
  OAuth, no user accounts. It is a single-user local tool and the key is in your
  config file with `0600` permissions.
- **Loopback bind** by default (`127.0.0.1:8081`). Nothing off your machine can
  reach it.
- **SSRF guards on capture.** `capture_url` is the one tool that makes an
  outbound request on an agent's behalf, and its destination ports are restricted
  to `capture.allowed_ports` (80 and 443 by default) - never "any port".
- **No telemetry.** Nothing phones home.
- **The installer trusts HTTPS and a checksum.** `curl | sh` runs whatever the
  site serves, so the honest description is: you are trusting this project's
  GitHub Pages and its releases. The script verifies each archive's SHA-256
  against the release `checksums.txt` before unpacking, which catches a corrupt
  or swapped asset - not a compromised release. Read it first, or skip it and
  use `go install`; both land in the same place.

The key and loopback are a matched pair. A static bearer key is adequate
*because* the listener is on loopback; it would not be adequate on a public
interface. If you widen `addr` to a routable address, the key becomes the only
thing between the internet and your entire knowledge store - so don't. Put it
behind a tunnel (Tailscale, SSH forwarding, Cloudflare Tunnel) and leave the bind
on loopback.

See [Configuration](/reference/configuration/) for every key.
