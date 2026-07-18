# Codex CLI hook-contract fixtures

Captured live from **codex-cli 0.144.5** (macOS, 2026-07-17) for `plan:codex-support`
step 1. These are the ground truth the Codex payload adapter and Stop-harvest
parser are built and tested against. Do not hand-edit the `*.input.json` values;
recapture instead (see "How these were captured").

## What is here

| File | What it is |
|---|---|
| `session-start.input.json` | Real stdin payload piped to a `SessionStart` command hook (exec run) |
| `user-prompt-submit.input.json` | Real stdin payload piped to a `UserPromptSubmit` command hook |
| `stop.input.json` | Real stdin payload piped to a `Stop` command hook |
| `rollout.jsonl` | A full-turn Codex rollout session file (`~/.codex/sessions/**/rollout-*.jsonl`), trimmed + path-sanitized. Source of the tail-harvest parser tests |
| `schema/*.schema.json` | Codex's own canonical JSON Schemas for the command hook inputs/outputs (vendored from `openai/codex` `codex-rs/hooks/schema/generated`) |

Machine-specific absolute paths were rewritten to `/Users/dev/...`; session UUIDs,
field names, and structure are verbatim.

## The contract (what the adapter must normalize)

**`hooks.json` shape** (`$CODEX_HOME/hooks.json`, or `.codex/hooks.json` per repo):
event names nest under a top-level `hooks` key, and the file struct is
`deny_unknown_fields` -- event names at the top level are rejected.

```json
{
  "description": "optional",
  "hooks": {
    "SessionStart":     [ { "hooks": [ { "type": "command", "command": "<shell string>" } ] } ],
    "UserPromptSubmit": [ { "hooks": [ { "type": "command", "command": "..." } ] } ],
    "Stop":             [ { "hooks": [ { "type": "command", "command": "..." } ] } ]
  }
}
```

A command handler is `{ "type": "command", "command": <shell string>,
"commandWindows"? (alias "command_windows"), "timeout"? (seconds, NOT `timeoutSec`),
"async"? (unsupported -- skipped), "statusMessage"? }`. Matcher group is
`{ "matcher"?, "hooks": [...] }`. Hook commands are **shell strings**, not exec-form
argv (opposite of what Seamless just did for CC in 504982c).

**Payload field names vs Claude Code** (this is the whole reason for an adapter):

| Concept | Claude Code | Codex |
|---|---|---|
| the submitted prompt | `user_prompt` | `prompt` |
| SessionStart trigger | `source`: startup/resume/clear/compact | `source`: **same** enum |
| last agent text at end | (not in payload; parse transcript) | `last_assistant_message` **in the Stop payload** |
| permission mode values | plan/default/acceptEdits/... | default/acceptEdits/plan/dontAsk/bypassPermissions |

Shared with CC (no change downstream): `session_id`, `cwd`, `transcript_path`,
`hook_event_name`, `model`, plus `turn_id` on turn-scoped events. The response
envelope is the **same** CC-style shape -- `{"hookSpecificOutput":
{"hookEventName": "...", "additionalContext": "..."}}` injects model-visible
context on SessionStart and UserPromptSubmit. Stop has no `hookSpecificOutput`
(it can only `continue`/`decision:block`/`systemMessage`), so Stop cannot inject.

## Verified behavior (resolves the design-note open questions)

- **`additionalContext` reaches the model in BOTH `codex exec` (headless) and the
  TUI.** Unlike `claude -p` (which skips UserPromptSubmit -- see
  `headless-cc-p-skips-userpromptsubmit-hook`), Codex `exec` delivers *both*
  SessionStart and UserPromptSubmit `additionalContext`. Proof: injected context
  is recorded in the rollout as `role:"developer"` `input_text` messages, and the
  model echoed the injected sentinel values in both modes.
- **No `SessionEnd` event fires in 0.144.5.** A registered `SessionEnd` command
  hook never runs (the installed binary embeds no `session-end.command.input`
  schema). Session end must be reaper-driven off `Stop` (design decision D5).
  Note: repo `main` *does* ship a `session-end.command.input` schema, so a future
  Codex may emit it -- treat SessionEnd as "not available in 0.144.5", not "never".
- **Stop fires every turn** with `stop_hook_active` and `last_assistant_message`.
  The harvester can read `last_assistant_message` straight from the Stop payload;
  the rollout tail-parse (below) is only the fallback when it is absent.
- **Rollout tail-parse target:** last agent message = the last `event_msg` whose
  `payload.type` is `task_complete` (`payload.last_agent_message`) or
  `agent_message` (`payload.message`, `phase:"final_answer"`). `session_meta`
  (first line) carries `source` (`exec` vs `cli`) and `originator`
  (`codex_exec` vs `codex-tui`) to tell the front-end apart.
- **Hook trust gate:** Codex only runs hooks whose `trusted_hash` is recorded (or
  in the interactive TUI trust review). Headless automation must pass
  `--dangerously-bypass-hook-trust` (a global flag, "intended only for automation
  that already vets hook sources") or pre-seed `hooks.state."<key>".trusted_hash`.
  The Seamless installer / E2E path must account for this.

## How these were captured

Isolated `CODEX_HOME` (never touched the user's real `~/.codex`) with a logging
hook that tees each event's stdin to a file and emits a sentinel
`additionalContext`, then `codex exec ... --dangerously-bypass-hook-trust` and a
pty-driven TUI run, both asking the model to echo the sentinel. See the session
memory `codex-hook-contract-0-144-5` for the full method and findings.
