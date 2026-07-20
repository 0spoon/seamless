---
title: Codex CLI setup
description: Wire OpenAI's Codex CLI into Seamless - install-hooks --client codex for the three ambient hooks, the mcp-proxy stdio bridge for tools, and the reaper-driven session lifecycle.
---

Codex is the second client Seamless supports directly. Wiring it up is the same
two independent halves as [Claude Code](/claude-code/) - **the MCP endpoint**
(what an agent can call) and **the hooks** (what happens without an agent calling
anything) - and one command installs both. Where Codex differs from Claude Code
is in the details, and those details are what this page is about.

## Install the hooks and register MCP

```bash
seamlessd install-hooks --client codex
```

`--client codex` is the switch. Without it, `install-hooks` targets Claude Code
(the default, unchanged). The Codex profile:

1. **Merges three hooks** - SessionStart, UserPromptSubmit, Stop - into
   `~/.codex/hooks.json` (or `$CODEX_HOME/hooks.json` when that is set), with the
   same guarantees as the Claude Code installer: unknown entries are preserved,
   each managed group carries a `seamless_managed` marker, and the file is backed
   up once before the first change.
2. **Registers the MCP server** by shelling out to Codex itself:
   `codex mcp add seamless -- <abs seam> mcp-proxy --config <abs seamless.yaml>`.
   That records a stdio server in `~/.codex/config.toml`; the
   [mcp-proxy bridge](#why-mcp-goes-through-a-bridge) is why it is stdio and not a
   direct HTTP URL. An already-present entry is tolerated, not clobbered.

The default (`--client claude`, or no flag) is byte-for-byte what it always was,
so nothing about your Claude Code setup changes when you add Codex. Use
`--client all` to install both in one pass.

## The three hooks, and what they inject

Codex installs three hooks against six for Claude Code. It has no plan-mode
capture (Codex has no plan mode to capture) and **no SessionEnd** - Codex 0.144.5
does not emit that event, so the lifecycle closes differently (see
[below](#no-sessionend-the-reaper-closes-sessions)).

| Event | Injects | Effect |
|---|---|---|
| `SessionStart` | `<seam-briefing>` | Resolves the agent's cwd to its project and creates or resumes the ambient `cx/{prefix}-{digest}` session. |
| `UserPromptSubmit` | `<seam-recall>` on a match | Heartbeats the session and matches the prompt against your memories. |
| `Stop` | nothing | Heartbeats the session and harvests findings from the turn's final message. Fires at every turn end. |

Codex delivers a hook's `additionalContext` to the model on both SessionStart and
UserPromptSubmit, in **both** the interactive TUI and headless `codex exec` -
there is no headless recall-injection gap of the kind `claude -p` has. The
briefing and recall blocks reach the model either way.

All three are **command** hooks - `seam hook <event> --config <yaml> --client
codex` - never http. Codex only runs command and `mcp_tool` hooks, so an http
hook would be silently skipped; and every command hook is subject to Codex's
trust gate, covered next. See [the hooks reference](/reference/hooks/) for the
full per-client table and transports.

### Trust the hooks once

Codex will not run a command hook until it is **trusted**. Untrusted hooks are
silently skipped - no error, the briefing and recall simply never fire. Two ways
to clear the gate:

- **Interactive** (the normal path): start `codex` in a TUI and approve the
  Seamless hooks at the startup trust review. Codex records the trust in
  `config.toml` and later `codex exec` runs honor it.
- **Headless automation**: pass `--dangerously-bypass-hook-trust`. As the flag
  name says, it is for automation that already vets its hook sources.

If a Codex session opens with no briefing, an untrusted hook is the first thing
to check - it is the Codex-specific version of "silence is the failure mode".

## Why MCP goes through a bridge

Seamless serves MCP over streamable HTTP. Codex speaks MCP to a server it
launches over **stdio**, not by POSTing to a URL you hand it. `seam mcp-proxy`
bridges the two: Codex launches it as a stdio server, and it forwards each
JSON-RPC frame to the daemon's `/api/mcp` endpoint with the bearer key from your
config, preserving the `Mcp-Session-Id` across calls so session binding keeps
working.

The bridge is transport-thin - no tool knowledge, no caching - and it is what the
installer registers. It exists so the key stays in `~/.config/seamless/seamless.yaml`
and never has to be copied into `~/.codex/config.toml` or exported into Codex's
environment.

One Codex-specific wrinkle for headless use: `codex exec` **cancels every MCP
tool call client-side** unless you also pass `--dangerously-bypass-approvals-and-sandbox`.
This is a separate gate from hook trust - `approval_policy=never` does not lift
it. Interactive `codex` lets you approve tool calls as they happen, which is the
safe path. Fully headless automation of the Seamless loop needs **both**
`--dangerously-bypass-hook-trust` (for the hooks) and
`--dangerously-bypass-approvals-and-sandbox` (for the tool calls).

### Registering MCP by hand

If you prefer a direct HTTP server over the stdio bridge - or `install-hooks`
could not find the `codex` CLI - add it to `~/.codex/config.toml` yourself:

```toml
[mcp_servers.seamless]
url = "http://127.0.0.1:8081/api/mcp"
http_headers = { Authorization = "Bearer <mcp.api_key>" }
```

The key is `mcp.api_key` from your config (`grep api_key
~/.config/seamless/seamless.yaml`). This is the plain alternative to the proxy,
at the cost of duplicating the key into Codex's TOML - which is exactly what the
bridge exists to avoid. `codex mcp add seamless --url http://127.0.0.1:8081/api/mcp
--bearer-token-env-var SEAMLESS_MCP_API_KEY` is a third route, but it reads the
key from an environment variable that Codex's own process must have set when it
launches - fragile to arrange reliably, which is the other reason the bridge is
the default.

## No SessionEnd: the reaper closes sessions

Claude Code harvests findings and completes its session on SessionEnd. Codex
0.144.5 emits no such event, so the lifecycle is built around **Stop** and the
**idle reaper** instead:

- **Every turn**, the Stop hook heartbeats the session and re-harvests the
  findings from that turn's final assistant message. The harvest is idempotent -
  an empty turn leaves the prior findings intact - so the findings converge on
  the last substantive turn's summary.
- **The session is never explicitly closed.** It sits active until
  `gardener.session_idle_minutes` of silence, at which point the idle reaper marks
  it `expired`. A Codex session therefore only ever reaches `expired`, never
  `completed`.

Those expired-but-harvested findings still surface in the next agent's briefing:
the briefing assembler includes expired ambient sessions, not just completed
ones, precisely so Codex's harvest is not invisible. The practical consequence is
that a Codex session's findings appear a few minutes after the last turn (once the
reaper runs), rather than the instant the window closes.

## Verify

```bash
seamlessd doctor    # config, database, tool count, hooks - now Codex-aware
```

`doctor` inspects `~/.codex/hooks.json` and reports the Codex MCP registration
alongside the Claude Code checks. It never fails a machine that has no Codex
install - a missing Codex is reported as a single OK line, and a present-but-
unconfigured Codex is a warning, not an error.

Then start a Codex session in a mapped repo and look for the injected
`<seam-briefing>` block, exactly as you would for Claude Code. If it is there, the
loop is closed: the hook resolved your cwd to a project, the daemon assembled a
briefing, and Codex started already knowing what you know. If it is **not** there,
the hooks fail open by design - start with `seamlessd doctor` and the trust gate,
then the [Troubleshooting](/guides/troubleshooting/) guide.
