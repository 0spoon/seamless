---
title: Install & deploy
description: Dev vs release layouts, the launchd service, upgrading, uninstalling, and the security posture you are accepting.
---

The [Quickstart](/quickstart/) builds and runs Seamless in the foreground. This
page is what you do once you want it running for real.

## One instance per machine

Both install layouts drive **the same single instance**: port `8081`, data dir
`~/.seamless`. Only one is active at a time — installing prod replaces dev, and
vice versa.

This is the single most common source of confusion, so it is worth stating
plainly: there is no "dev instance" and "prod instance" side by side. There is
one daemon, one database, one set of files, and two ways of deciding which
binary and config it runs from.

## Dev layout

Runs the service and hooks straight from the working tree, so a rebuild takes
effect on the next restart.

```bash
make install-service   # launchd service -> ./bin/seamlessd + ./seamless.yaml
make install-hooks     # hooks -> ./bin/seam
make dev               # rebuild + restart in place
```

Fast to iterate. The catch: `make build`, a branch switch, or moving the repo
changes what the live service and the global SessionStart hook actually execute.
That is fine while you are working on Seamless itself and a trap otherwise —
every agent on the machine is running the hook from your working tree.

## Release layout

Snapshots the binaries and config to stable locations independent of the working
tree, then points launchd and the hooks at the copies. Survives rebuilds, branch
switches, and a moved or cleaned repo.

```bash
make install-prod                    # -> ~/.local/bin + ~/.config/seamless/seamless.yaml
make install-prod PREFIX=/opt/seam   # custom prefix (binaries land in $PREFIX/bin)
make uninstall-prod                  # remove prod service + binaries (config kept)
```

`install-prod` copies `seamless.yaml` **only when the destination is absent**, so
it never clobbers an edited prod config. Delete the copy to re-seed from the
repo. It lands in `~/.config/seamless/`, one of the paths `seam` already
searches, so the hooks resolve config from any directory.

To go back to dev: `make install-service && make install-hooks`.

## The launchd service

The service is a macOS user LaunchAgent labelled
`org.thereisnospoon.seamless`, rendered from `deploy/launchd/` into
`~/Library/LaunchAgents/`.

```bash
make service-status    # is it loaded, what pid, last exit
make logs              # follow ~/.seamless/seamlessd.log
make restart-service   # restart in place
make stop-service      # unload (KeepAlive will not resurrect an unloaded job)
```

## Upgrading

```bash
git pull
make check             # everything green before you swap the running daemon
make install-prod      # or: make dev, for the dev layout
make doctor            # confirm config + DB after the swap
```

Migrations apply automatically at startup — there is no separate migrate step.
Run `make doctor` afterwards anyway: it is the cheapest way to learn that the new
build disagrees with your config before an agent does.

`/healthz` reports the running build. If a change seems not to have taken effect,
check it before you debug anything else — a stale daemon still serving the old
binary looks exactly like a bug in the new one.

## Uninstalling

```bash
make uninstall-prod          # or: make uninstall-service
```

Neither touches `~/.seamless`. Your memories and notes are markdown files; the
uninstall of a program should not delete your knowledge. Remove the directory by
hand if you actually mean it — and see [Storage](/reference/storage/) first for
what is in there.

## Security posture

What you are accepting when you run this:

- **One static bearer key** guards `/api/mcp` and the console. Not JWT, not
  OAuth, no user accounts. It is a single-user local tool and the key is in your
  config file with `0600` permissions.
- **Loopback bind** by default (`127.0.0.1:8081`). Nothing off your machine can
  reach it.
- **SSRF guards on capture.** `capture_url` is the one tool that makes an
  outbound request on an agent's behalf, and its destination ports are restricted
  to `capture.allowed_ports` (80 and 443 by default) — never "any port".
- **No telemetry.** Nothing phones home.

The key and loopback are a matched pair. A static bearer key is adequate
*because* the listener is on loopback; it would not be adequate on a public
interface. If you widen `addr` to a routable address, the key becomes the only
thing between the internet and your entire knowledge store — so don't. Put it
behind a tunnel (Tailscale, SSH forwarding, Cloudflare Tunnel) and leave the bind
on loopback.

See [Configuration](/reference/configuration/) for every key.
