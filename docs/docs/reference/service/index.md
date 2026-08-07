# The service & where things live

> Everything OS-specific in one place - the per-user service (launchd, systemd, or a Scheduled Task), its native controls and logs, and every path Seamless touches.

The installer registers `seamlessd` as a per-user service, so the daemon
survives reboots without you supervising it. It runs as **your** user, not
root: it reads your config, writes your files, and should die with your login
session, not the machine.

There is **one instance per machine**: port `8081`, data dir `~/.seamless` -
one daemon, one database, one set of files. Both are config keys, not fixed
facts: set `addr:` and `data_dir:` in `~/.config/seamless/seamless.yaml` (or
the `SEAMLESS_ADDR` / `SEAMLESS_DATA_DIR` env overrides) and restart the
service. The config is the single source of truth for the bind address - the
installer and the Makefile both read the port back out of it, so their health
checks follow your change rather than assuming `8081`, and nothing bakes the
address into the service itself.

## Control it from anywhere

Whatever the platform, one set of verbs controls the service - they resolve
your OS's service manager for you:

```bash
seamlessd start       # start | stop | restart | status
make start            # the same, from a clone (start | stop | restart | status)
```

These act on the already-installed service and print a hint if it was never
installed. The platform-native commands below are what they wrap - you need
them only when you want to talk to the service manager directly.

## The service on your OS

**macOS:**

A user LaunchAgent labelled `org.thereisnospoon.seamless` in
`~/Library/LaunchAgents/`, logging to `~/.seamless/seamlessd.log` (`make logs`
follows it from a clone). The universal verbs wrap:

```bash
launchctl print gui/$(id -u)/org.thereisnospoon.seamless        # state
launchctl kickstart -k gui/$(id -u)/org.thereisnospoon.seamless # restart
launchctl bootout gui/$(id -u)/org.thereisnospoon.seamless      # stop
```


**Linux:**

The installer writes a systemd user unit to
`~/.config/systemd/user/seamless.service` and enables lingering, so the daemon
starts at boot rather than at your next login:

```bash
systemctl --user status seamless      # state, pid, last exit
journalctl --user -u seamless -f      # follow the log
systemctl --user restart seamless
systemctl --user stop seamless
```

No systemd user session (some containers, WSL1)? The installer says so and
skips the step; run `seamlessd serve` under whatever supervises processes
there.


**Windows:**

An at-logon Scheduled Task named `Seamless`, running as you (`LogonType
Interactive`, no admin), logging to `~/.seamless/seamlessd.log`. The task
action is a bare exec - `seamlessd.exe serve --config <path> --log-file
<path>` - because a task cannot carry the `SEAMLESS_CONFIG` env prefix a plist
or systemd unit does; the two flags pass exactly what that prefix would have:

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

Windows wiring ships in every release and its hook command forms are
unit-tested, but it has fewer live-verified runs than macOS and Linux; the
[Codex compatibility matrix](https://thereisnospoon.org/docs/reference/codex-compatibility/) records exactly
which combinations have been observed working.


## Where things live {#where-things-live}

Every `~` path resolves under `%USERPROFILE%` on Windows - the daemon searches
the same relative locations on every OS.

| What | Where | Notes |
|---|---|---|
| Binaries | `~/.local/bin/seamlessd`, `~/.local/bin/seam` | `SEAMLESS_INSTALL_DIR` retargets them - see [Install & deploy](https://thereisnospoon.org/docs/install/) |
| Config + bearer key | `~/.config/seamless/seamless.yaml` | mode `0600`; every key in [Configuration](https://thereisnospoon.org/docs/reference/configuration/) |
| Knowledge + database | `~/.seamless/` | markdown memories and notes, plus `seam.db` - see [Storage](https://thereisnospoon.org/docs/reference/storage/) |
| Daemon log | `~/.seamless/seamlessd.log` (macOS, Windows); journald (Linux) | `make logs` from a clone, `journalctl --user -u seamless -f` on Linux |
| Claude Code hooks | `~/.claude/settings.json` | exactly what is written: [Hooks](https://thereisnospoon.org/docs/reference/hooks/) |
| Codex hooks | `${CODEX_HOME:-~/.codex}/hooks.json` | same reference, [shell-string forms](https://thereisnospoon.org/docs/reference/hooks/#the-codex-profile-is-shell-strings) |
| Skills | `~/.claude/skills/`, `${CODEX_HOME:-~/.codex}/skills/` | `seam-onboard` always; `seam-research` while its [optional feature](https://thereisnospoon.org/docs/reference/console/#optional-features) is on. Delivered per client |
| Claude app chat MCP | `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS); `%APPDATA%\Claude\claude_desktop_config.json` (Windows) | the chat surface's only artifact - [Claude app chat setup](https://thereisnospoon.org/docs/claude-app/) |

## Removing the service by hand {#removing-the-service-by-hand}

[Update & uninstall](https://thereisnospoon.org/docs/updating/) covers `seamlessd uninstall`, which removes
the service along with everything else. For a bare binary you never registered
a service for - or a machine you are cleaning by hand - the native teardown is:

```bash
launchctl bootout gui/$(id -u)/org.thereisnospoon.seamless   # macOS
systemctl --user disable --now seamless                      # Linux
```

```powershell
Unregister-ScheduledTask -TaskName Seamless                  # Windows
```
