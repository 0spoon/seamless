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
4. **Runtime override (DB)** - the console's Settings form stores briefing knobs
   in the database. They win over file *and* env, apply from the next session
   start without a daemon restart, and stay until reset.

That fourth layer only covers the `briefing:` block. It exists so you can tune
what agents get injected while they are running, and it is the one place where
the config file is not the last word - check the console before concluding a
briefing setting is being ignored.

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
