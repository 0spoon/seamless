---
title: Install & deploy
description: The install layout, the launchd service, upgrading, uninstalling, and the security posture you are accepting.
---

The [Quickstart](/quickstart/) builds and runs Seamless in the foreground. This
page is what you do once you want it running for real.

## One instance per machine

There is **one instance**: port `8081`, data dir `~/.seamless`. One daemon, one
database, one set of files.

Both are config keys, not fixed facts - set `addr:` and `data_dir:` in
`~/.config/seamless/seamless.yaml` (or the `SEAMLESS_ADDR` / `SEAMLESS_DATA_DIR`
env overrides) and re-run `make install`. The config is the single source of
truth for the bind address: the Makefile reads the port back out of it, so the
health check and `make help` follow your change rather than assuming `8081`.
Nothing bakes the address into the service - which is why editing `addr:` works
instead of being silently overridden.

## Install

```bash
make install                    # -> ~/.local/bin + ~/.config/seamless/seamless.yaml
make install PREFIX=/opt/seam   # custom prefix (binaries land in $PREFIX/bin)
make uninstall                  # remove service + binaries (config kept)
```

`make install` snapshots the binaries and config to stable locations, then points
launchd **and** the Claude Code hooks at the copies. Nothing live resolves
through your working tree, so `make build`, a branch switch, and a moved or
cleaned repo cannot change what the running daemon and every agent's hooks
execute. Swapping them is `make install`, deliberately.

The config lands in `~/.config/seamless/`, one of the paths Seamless already
searches ahead of `./seamless.yaml`, so the hooks resolve it from any directory.
It is seeded **only when absent** - an install never clobbers a config holding
your bearer key. Delete it to re-seed.

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

```bash
make uninstall
```

Neither touches `~/.seamless`. Your memories and notes are markdown files; the
uninstall of a program should not delete your knowledge. Remove the directory by
hand if you actually mean it - and see [Storage](/reference/storage/) first for
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
  to `capture.allowed_ports` (80 and 443 by default) - never "any port".
- **No telemetry.** Nothing phones home.

The key and loopback are a matched pair. A static bearer key is adequate
*because* the listener is on loopback; it would not be adequate on a public
interface. If you widen `addr` to a routable address, the key becomes the only
thing between the internet and your entire knowledge store - so don't. Put it
behind a tunnel (Tailscale, SSH forwarding, Cloudflare Tunnel) and leave the bind
on loopback.

See [Configuration](/reference/configuration/) for every key.
