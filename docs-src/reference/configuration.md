---
title: Configuration
description: Every configuration key, its type and default, plus the annotated example file and the four layers that resolve them.
generate: config
---

Seamless reads a single YAML file. Every key also has a `SEAMLESS_*` environment
override.

## Where the config comes from

The file is looked up in this order, first hit wins:

1. `$SEAMLESS_CONFIG`
2. `~/.config/seamless/seamless.yaml`
3. `./seamless.yaml`

## Precedence

Four layers resolve each key. Later layers win:

1. **Defaults** - the built-in values in the table below.
2. **File** - whatever the YAML sets.
3. **Environment** - `SEAMLESS_*` overrides the file.
4. **Runtime override (DB)** - the console's Settings forms store some blocks in
   the database. They win over file *and* env, take effect without a daemon
   restart, and stay until reset.

That fourth layer covers three blocks, and only those three:

| Block | Written by | When it takes effect |
|---|---|---|
| `briefing:` | Settings → [Briefing](/reference/console/#briefing) | From the next session start. |
| `features:` | Settings → [Features](/reference/console/#optional-features) | Immediately in the console; an agent sees it from its next session (tool lists and briefings alike). |
| `console.level` | Settings → [Experience](/reference/console/#experience), or the Home welcome card | Immediately, in the console only. |

It exists so you can change what agents get injected - and what they can reach -
while they are running, and it is the one place where the config file is not the
last word: check the console before concluding a `briefing:`, `features:`, or
`console.level` setting is being ignored. No form ever writes your config file,
and **Reset** on each clears the stored row and hands that block back to
file/env.

Overrides can be in force without you having set them. Upgrading an
installation that already holds trial data seeds a stored `features:` override
with research on, so a feature that now ships off does not disappear from under
data you were already using. The console labels it a stored override, not your
choice; **Reset** clears it like any other.

### The console level

`console.level` (`SEAMLESS_CONSOLE_LEVEL`) is how much of the
[console](/reference/console/#choose-how-much-you-see) you see: `basic`,
`standard`, or `advanced`. It is presentation only - briefings, MCP tools,
hooks, recall, and the gardener are identical at every level - and a screen a
level leaves out of the sidebar still opens from a link. A value outside those
three stops the daemon at startup with an error naming them; an empty value
means the default, `basic`.

A fresh installation starts at `basic`. Upgrading an installation that had
already recorded sessions stores `advanced` once, as the same kind of runtime
override (labeled "set by the upgrade" in Settings → Experience), so an upgrade
never hides a screen you were using. Like any override, it wins over this key
until **Reset** hands the level back to file/env.

## Generating a key

`mcp.api_key` guards `/api/mcp` and the console. On a true first run - no
config file anywhere in the search order and no `SEAMLESS_MCP_API_KEY` in the
environment - `seamlessd serve` (or `install-hooks`) generates one and writes
it to `~/.config/seamless/seamless.yaml`, so a fresh install never handles the
key by hand. An existing config file is never edited, even when its key is
empty; set one yourself:

```bash
openssl rand -hex 32
```

The daemon still starts with an empty key, but every MCP and hook request is
rejected until one is set - `seamlessd doctor` reports it as a warning.
