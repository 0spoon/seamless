# Codex compatibility matrix

> Versioned, platform-specific evidence for the Codex hooks, MCP transports, trust gate, output limit, and the contract-recapture procedure.

This matrix records what Seamless has **observed**, what is pinned only by a
released schema or source, and what remains unverified. It is an evidence ledger,
not a claim that an older Codex release is a supported minimum. The current
fixture version is the one named by `currentCodexFixtureVersion` in
`internal/hooks/adapter.go`.

## Maintained matrix

| Codex runtime / frontend / platform | Hook events captured | Schemas | Frontends | Trust behavior | `mcp get --json` |
|---|---|---|---|---|---|
| 0.144.5 / Darwin arm64 | SessionStart, UserPromptSubmit, Stop | Parent-event schemas retained with the capture; SessionEnd came from then-current source and did not fire | Exec payloads committed; model visibility observed in exec and TUI, but no TUI payload set was retained | Untrusted command hooks were skipped; bypass flag observed. Historical private-state details are evidence only, not an integration API | Not captured |
| 0.144.6 / Darwin arm64 | SessionStart, UserPromptSubmit, Stop, SubagentStart, SubagentStop | Input and output schemas copied from release commit `5d1fbf26c43abc65a203928b2e31561cb039e06d` and SHA-256 pinned | Live exec and TUI inputs/outputs for all five events; wire fields match, with `permission_mode` differing as recorded | Current definitions require review through `/hooks`; the harness uses `--dangerously-bypass-hook-trust`. No supported trust-state query is assumed | Enabled/disabled stdio and Streamable HTTP shapes captured |
| 0.145.0-alpha.18 / Codex.app 26.715.52143 / Darwin arm64 | SessionStart, UserPromptSubmit, and Stop live in Local app chats; SubagentStart/SubagentStop not app-tested | Bundled runtime contains the five current event and input/output contracts | Real Local chats captured in a global app workspace and a repo workspace; briefing, prompt recall, and Stop harvest were model-visible | `/hooks` is not intercepted in the app and directs the user to the CLI; hook execution is observed, but trust state remains uninspectable | Bundled runtime and PATH CLI read the same exact enabled stdio registration; app calls to project_list, session_start, memory_read, notes_create, and notes_read succeeded |
| 0.144.6 / Windows amd64 and arm64 | No live hook run | Same released schemas; Seamless's Windows command syntax is unit-tested | Not live-verified | Not live-verified | Not live-verified |

| Codex runtime / frontend / platform | `commandWindows` | Direct Streamable HTTP | Upstream hook-output behavior | Seamless ceiling | Live Windows status |
|---|---|---|---|---|---|
| 0.144.5 / Darwin arm64 | Schema/source evidence only | Supported by upstream configuration; not fixture-captured | Not captured | Not present in the historical integration | No |
| 0.144.6 / Darwin arm64 | Both `commandWindows` and `command_windows` parsed by the live macOS binary; Windows selection itself is source-only | Both enabled and disabled config shapes captured through `mcp get --json`; no direct-HTTP tool call was part of the capture | Approximate 2,500-token per-entry spill observed: a head/tail model preview plus a temporary full-output path | 2,400 estimated tokens, applied before response serialization and telemetry | No |
| 0.145.0-alpha.18 / Codex.app 26.715.52143 / Darwin arm64 | Not app-tested | Direct HTTP not app-tested; the installed stdio bridge completed a live read/write/read round trip | SessionStart/UserPromptSubmit context and Stop.last_assistant_message observed in real app chats | Same platform-independent Seamless cap; oversize behavior not app-tested | No |
| 0.144.6 / Windows amd64 and arm64 | Generated command syntax and quoting are tested; execution is not | Capability is in the cross-platform released configuration contract; not live-verified on Windows | Source/schema only | Same platform-independent Seamless cap; no live Windows observation | **Not yet run** |

The absence of a live Windows row is intentional and visible. Cross-compiling a
Windows binary, parsing `command_windows`, or testing quoting on macOS is not the
same evidence as running Codex on Windows.

The macOS Local app row is deliberately narrower than a general desktop-support
claim. It proves repo-scoped briefing/recall, the installed stdio MCP bridge, an
explicitly bound read/write/read note round trip, and Stop harvest. The tested
machine also had a PATH Codex CLI, so app-only setup is still unverified. App
subagents, managed-worktree scope, oversize output, idle reaping, uninstall
preservation, Windows, and WSL remain open. The live app did not expose `/hooks`,
so execution evidence cannot be promoted into a claim that the current hook
definition is trusted.

## What Seamless relies on

- The installed profile contains exactly the five current hook events above.
  Contract tests derive that list from the canonical Codex hook profile rather
  than maintaining a second count by hand.
- `Stop.last_assistant_message` is the primary findings source. Transcript paths
  are useful diagnostic evidence, but Codex's rollout JSONL layout is an
  **unstable fallback contract** and may change without compatibility notice.
- Codex supports both stdio and Streamable HTTP MCP. Seamless installs the stdio
  `seam mcp-proxy` policy so the bearer key remains in Seamless's 0600 config;
  direct HTTP is a supported manual alternative.
- Hook trust is a Codex decision over the current definition. Seamless neither
  parses nor writes private trust hashes. `seamlessd doctor` can verify the
  definition and show recent execution evidence, but it reports trust as
  unverified and directs the operator to `/hooks`.

## Refreshing the matrix for a Codex release

Never overwrite an old version directory. The reproducible harness and the
sanitization checklist live in
[`internal/hooks/testdata/codex/`](https://github.com/0spoon/seamless/tree/main/internal/hooks/testdata/codex).
For each new release:

1. Install the exact Codex release binary and record its version, release tag,
   source revision, platform/architecture, and binary/archive SHA-256 values.
2. Create a fresh absolute capture root **outside this repository**. Run
   `capture.sh setup`; pass an auth file only if live turns are required. The
   harness creates an isolated `CODEX_HOME` and a throwaway git repository.
3. Run the `mcp`, `exec`, `tui`, and `oversize` capture phases. Use only the
   synthetic sentinel prompt. Run `clean-auth` immediately afterward.
4. Copy the exact released hook schemas from that release revision. Create a new
   `v<version>/` fixture directory, update `capture.json`, and sanitize paths,
   IDs, timestamps, and all conversational text before anything enters git.
5. Compare exec and TUI field sets, subagent parent/child transcript roles,
   `mcp get --json` for both transports and enabled states, both Windows command
   aliases, trust behavior, and the observed spill boundary. A real Windows run
   must be recorded separately; source and syntax tests do not count as live.
6. Update this page and the fixture README, advance
   `currentCodexFixtureVersion`, then run `go test ./internal/hooks`, `make docs`,
   and the full `make check` gate.

Do not commit raw auth, full temporary hook output, private hook-trust state, an
operator path, or real transcript text. Do not promote rollout JSONL parsing to
the primary harvest path even if its current shape happens to remain unchanged.

Primary contracts: [Codex hooks](https://learn.chatgpt.com/docs/hooks),
[Codex MCP](https://learn.chatgpt.com/docs/extend/mcp), and
[UUIDv7](https://www.rfc-editor.org/rfc/rfc9562.html#section-5.7).
