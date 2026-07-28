#!/usr/bin/env bash
# record.sh -- branding: stand up the terminal-scene RECORDING fixture and print
# the two interactive `claude` sessions to capture, then how to turn the takes
# into scene data for the landing page.
#
# This is a THIN wrapper over `scripts/fixture/harness.sh --mode record`. Every
# flag is passed straight through and none of the harness's work is repeated
# here: the fixture, the seeding, the with/without config dirs, the model pin,
# the briefing self-check, and the recording recipe are all its. If you need
# behaviour it lacks, add a flag THERE -- a second copy of that logic in this
# file is how the two drift apart.
#
# What this wrapper adds is the branding tail the harness has no business
# knowing: where each side's transcript lands and the exact distill invocation
# that turns it into docs/static/scenes.js. It reads those paths back out of the
# harness's own printed recipe, so it assumes nothing about the fixture layout.
#
# Usage:
#   scripts/branding/record.sh [--base DIR] [--port N] [--model ID]
#                              [--race] [--no-verify] [--no-build]
#
# Nothing here touches the live ~/.seamless, ~/.claude, or ~/.codex -- see the
# guarantees documented at the top of scripts/fixture/harness.sh.
#
# Full recipe (both branding flows): scripts/branding/README.md
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
harness="$repo_root/scripts/fixture/harness.sh"

# The leading comment block, minus the shebang, is this script's help.
self_help() {
  awk 'NR == 1 { next } !/^#/ { exit } { sub(/^# ?/, ""); print }' "$0"
}

for arg in "$@"; do
  case "$arg" in
    -h|--help)
      self_help
      echo "Every other flag is the harness's; see its own help for the list:"
      echo "  scripts/fixture/harness.sh --help"
      exit 0
      ;;
    --mode)
      echo "record.sh: --mode is fixed to record; drop it (bench arms are cmd/seambench)" >&2
      exit 2
      ;;
    --conditions)
      echo "record.sh: --conditions is a bench-mode flag; recording has exactly two sides" >&2
      exit 2
      ;;
  esac
done

if [[ ! -x "$harness" ]]; then
  echo "record.sh: $harness is missing or not executable" >&2
  exit 1
fi

log=$(mktemp "${TMPDIR:-/tmp}/seamless-record.XXXXXX")
trap 'rm -f "$log"' EXIT

set +e
"$harness" --mode record "$@" | tee "$log"
status=${PIPESTATUS[0]}
set -e
[[ "$status" -eq 0 ]] || exit "$status"

# Read the fixture paths back out of the harness's printed recipe rather than
# recomputing its layout: the recipe is its interface, the directory tree is not.
cc_with=$(sed -n 's/^ *CLAUDE_CONFIG_DIR=\(.*\) claude$/\1/p' "$log" | sed -n 1p)
cc_without=$(sed -n 's/^ *CLAUDE_CONFIG_DIR=\(.*\) claude$/\1/p' "$log" | sed -n 2p)
myapp=$(sed -n 's/^ *cd \(.*\)$/\1/p' "$log" | sed -n 1p)

if [[ -z "$cc_with" || -z "$cc_without" || -z "$myapp" ]]; then
  cat <<'EOF'

================================================================================
Fixture is up, but this wrapper could not read the with/without config dirs out
of the harness recipe above (its output format changed). Record from the recipe
as printed; transcripts land in $CLAUDE_CONFIG_DIR/projects/<slug>/*.jsonl.

Then distill them -- see scripts/branding/README.md.
================================================================================
EOF
  exit 0
fi

# Claude Code names a project dir after the cwd with every non-alphanumeric
# character replaced by a dash.
slug=$(printf '%s' "$myapp" | sed 's/[^a-zA-Z0-9]/-/g')

cat <<EOF

================================================================================
Branding tail -- turning the takes into scene data

Record BOTH sides of a scene with the same prompt (recipe above), then quit each
session cleanly: an interactive claude only flushes its transcript to disk on a
clean exit, so a killed session loses the take.

  with     $cc_with/projects/$slug/*.jsonl
  without  $cc_without/projects/$slug/*.jsonl
  (if the slug differs, look in $cc_with/projects/)

1. Inventory a take -- numbered, curatable steps:

     uv run scripts/branding/distill.py steps <take.jsonl>

2. Scaffold a scene spec from both takes (it keeps every step; you trim it):

     uv run scripts/branding/distill.py scaffold \\
       --id my-scene --title '...' --prompt '<the prompt, verbatim>' \\
       --pane without=<without take.jsonl> \\
       --pane with=<with take.jsonl> \\
       -o /tmp/my-scene.json

3. Build scene data and DIFF it before it goes anywhere near docs/:

     uv run scripts/branding/distill.py build /tmp/my-scene.json -o /tmp/scenes.js

The distill step is verbatim-audited: it can drop steps and mark fast-forward
spans, and it will refuse to emit a line that is not byte-identical to the take.

Full recipe, including publishing a scene: scripts/branding/README.md
================================================================================
EOF
