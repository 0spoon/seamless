# Codex CLI contract fixtures

These fixtures pin the external Codex contracts used by Seamless's hook adapter,
installer, doctor, and MCP registration logic. Each capture is versioned; never
replace an older directory when Codex changes.

## Version matrix

| Codex | Capture | Coverage | Status |
|---|---|---|---|
| 0.144.5 | `v0.144.5/` | Live `codex exec` SessionStart, UserPromptSubmit, Stop, a sanitized rollout, and the then-current schemas | Historical baseline |
| 0.144.6 | `v0.144.6/` | Live exec + TUI payloads and outputs for SessionStart, UserPromptSubmit, Stop, SubagentStart, and SubagentStop; MCP JSON shapes; released schemas; oversized-output observation | Current contract |

The 0.144.6 release tag `rust-v0.144.6` resolves to source commit
`5d1fbf26c43abc65a203928b2e31561cb039e06d`. The vendored schemas are exact
copies from that commit's `codex-rs/hooks/schema/generated/` directory. See
`v0.144.6/capture.json` for binary and platform provenance.

## Reproducible capture

`capture.sh` creates every mutable Codex artifact under a new caller-selected
absolute path. It never reads or writes the active Codex home unless an auth file
is explicitly passed as the second `setup` argument; that file is copied with
mode 0600 into the isolated home and removed by `clean-auth`. Raw output must stay
outside the repository until it has been inspected and sanitized.

```bash
capture_parent=$(mktemp -d "${TMPDIR:-/tmp}/seamless-codex-contract.XXXXXX")
capture_root="$capture_parent/run"
./internal/hooks/testdata/codex/capture.sh setup "$capture_root" "$HOME/.codex/auth.json"
./internal/hooks/testdata/codex/capture.sh mcp "$capture_root"
./internal/hooks/testdata/codex/capture.sh exec "$capture_root"
./internal/hooks/testdata/codex/capture.sh tui "$capture_root"
./internal/hooks/testdata/codex/capture.sh oversize "$capture_root"
./internal/hooks/testdata/codex/capture.sh clean-auth "$capture_root"
```

The TUI asks whether to trust the throwaway repository. Choosing yes changes only
the isolated config. After the sentinel turn completes, enter `/exit`. Both live
runs use `--dangerously-bypass-hook-trust` solely for the generated logging hook;
the harness does not create or interpret Codex's private `hooks.state` hashes.

Before committing a recapture:

1. Confirm `clean-auth` succeeded and no `auth.json`, token, real repository path,
   or non-sentinel transcript text is in the candidate files.
2. Rewrite the throwaway root to `/Users/dev/myrepo`, the isolated Codex home to
   `/Users/dev/.codex`, and UUIDv7 values to stable fixture IDs.
3. Keep the model/version and wire field names verbatim. Format JSON with `jq`.
4. Copy schemas from the exact release commit, update `capture.json`, and run the
   fixture integrity and adapter tests.

## 0.144.6 findings

- Exec and TUI emitted the same fields for all five events. The live frontend
  difference was `permission_mode`: `bypassPermissions` for `codex exec` and
  `default` for the TUI capture.
- `SubagentStart.transcript_path` named the child rollout. `SubagentStop` named
  the parent rollout in `transcript_path` and the child in
  `agent_transcript_path`. Those paths and rollout layouts are diagnostic and
  unstable; Stop's `last_assistant_message` remains the primary harvest source.
- Both `commandWindows` and its `command_windows` alias were accepted by the
  live macOS binary while their POSIX `command` ran. Selection of the Windows
  override is pinned by the released source and existing syntax tests, but this
  capture did not execute a Windows binary.
- The released implementation uses an approximate 2,500-token per-entry hook
  output limit. The live oversized sentinel became a 10,107-byte model-visible
  head/tail preview containing a full-output path; `v0.144.6/oversize.json`
  records the bounded observation without committing the raw 3,000-marker body
  or its temporary path.
- `codex mcp get seamless --json` exposes enabled state, a nullable
  `disabled_reason`, transport-specific data, enabled/disabled tool lists, and
  timeouts. Stdio includes ordered args, env, inherited env-var descriptors, and
  cwd. Streamable HTTP includes URL, bearer-token env-var name, literal headers,
  and env-backed header names.

The output fixtures are the exact JSON emitted by the logging hook. The input
fixtures are sanitized copies of real stdin payloads. `rollout-meta.json` keeps
only non-conversational session metadata needed to distinguish exec from TUI.
