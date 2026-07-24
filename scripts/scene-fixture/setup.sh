#!/usr/bin/env bash
# setup.sh -- stand up the whole terminal-scenes recording fixture in one command
# (plan:terminal-scenes, step 1). It creates, under a single throwaway base dir:
#
#   myapp/            the demo git repo the agent works in (make-myapp.sh)
#   data/             a seeded, THROWAWAY Seamless data dir (demoseed -scenes)
#   seamless.yaml     config for that instance (non-live port, own key)
#   home-with/        with-side Claude config dir: Seamless hooks + MCP server
#   home-without/     without-side Claude config dir: bare, no Seamless
#
# It never touches the live ~/.seamless or the real ~/.claude: the seeded
# instance serves on a non-live port (like the console-shots recipe), and each
# side runs claude with its own CLAUDE_CONFIG_DIR.
#
# Usage:
#   scripts/scene-fixture/setup.sh [--base DIR] [--port N] [--race] [--no-verify] [--no-build]
#
# After it runs, follow the printed recipe: serve the instance, then run the two
# claude sessions (with / without) against myapp with the same prompt.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
base="${TMPDIR:-/tmp}/seamless-scenes"
port=8099
race=""
verify=1
build=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base) base="$2"; shift 2;;
    --port) port="$2"; shift 2;;
    --race) race="-race"; shift;;
    --no-verify) verify=0; shift;;
    --no-build) build=0; shift;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) echo "setup: unknown arg $1" >&2; exit 2;;
  esac
done

base=$(mkdir -p "$base" && cd "$base" && pwd)
myapp="$base/myapp"
data="$base/data"
cfg="$base/seamless.yaml"
keyfile="$base/key.txt"
home_with="$base/home-with"
home_without="$base/home-without"
cc_with="$home_with/.claude"
cc_without="$home_without/.claude"
seamlessd="$repo_root/bin/seamlessd"
seam="$repo_root/bin/seam"

echo "==> base: $base"

# 1. Build the binaries (idempotent; fast when up to date).
if [[ "$build" == "1" ]]; then
  echo "==> building seamlessd + seam"
  make -C "$repo_root" build >/dev/null
else
  echo "==> skipping build (--no-build); using existing bin/"
fi
if [[ ! -x "$seamlessd" || ! -x "$seam" ]]; then
  echo "setup: $seamlessd / $seam missing; run 'make build' first (or drop --no-build)" >&2
  exit 1
fi

# 2. Scaffold the demo repo.
echo "==> scaffolding myapp repo"
"$repo_root/scripts/scene-fixture/make-myapp.sh" "$myapp" >/dev/null

# 3. Throwaway config + key for the seeded instance.
if [[ -f "$keyfile" ]]; then key=$(cat "$keyfile"); else key=$(openssl rand -hex 32); echo "$key" >"$keyfile"; fi
cat >"$cfg" <<EOF
# Throwaway config for the terminal-scenes fixture. NOT a live instance.
addr: "127.0.0.1:$port"
data_dir: "$data"
mcp:
  api_key: "$key"
EOF

# 4. Seed the minimal scene state (fresh each run).
echo "==> seeding scene fixture${race:+ (race variant)}"
rm -rf "$data"
mkdir -p "$data"
( cd "$repo_root" && go run ./cmd/demoseed -scenes -data "$data" -repo "$myapp" $race )

# 5a. With-side: install Seamless hooks + register the MCP server, all into the
#     throwaway CLAUDE_CONFIG_DIR so the real ~/.claude is untouched.
#     --client claude is load-bearing: without it install-hooks detects clients,
#     and on a machine with Codex it would prompt interactively (TTY) or wire
#     the LIVE ~/.codex at the fixture's throwaway daemon (non-TTY).
echo "==> wiring with-side harness ($cc_with)"
rm -rf "$cc_with" && mkdir -p "$cc_with"
SEAMLESS_CONFIG="$cfg" CLAUDE_CONFIG_DIR="$cc_with" \
  "$seamlessd" install-hooks --client claude --settings "$cc_with/settings.json" \
    --url "http://127.0.0.1:$port" --seam "$seam" | sed 's/^/    /'

# 5b. Without-side: a bare config dir -- no hooks, no MCP, vanilla Claude Code.
echo "==> wiring without-side harness ($cc_without)"
rm -rf "$cc_without" && mkdir -p "$cc_without"

# 6. Optional self-check: serve briefly and confirm the with-side briefing.
verify_ok=""
if [[ "$verify" == "1" ]]; then
  echo "==> verifying the with-side briefing"
  SEAMLESS_CONFIG="$cfg" "$seamlessd" serve >"$base/serve-verify.log" 2>&1 &
  vpid=$!
  trap '[[ -n "${vpid:-}" ]] && kill "$vpid" 2>/dev/null || true' EXIT
  for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
  briefing=$(curl -s -X POST "http://127.0.0.1:$port/api/hooks/session-start" \
    -H "Authorization: Bearer $key" -H "Content-Type: application/json" \
    -d "{\"session_id\":\"setup-verify\",\"cwd\":\"$myapp\",\"source\":\"startup\"}" \
    | python3 -c 'import sys,json; print(json.load(sys.stdin).get("hookSpecificOutput",{}).get("additionalContext",""))' 2>/dev/null || true)
  kill "$vpid" 2>/dev/null || true; wait "$vpid" 2>/dev/null || true; vpid=""
  if grep -q -- "- auth-refresh -- " <<<"$briefing" && grep -q "edge-cache-gotcha" <<<"$briefing" && grep -q "(18h)" <<<"$briefing"; then
    verify_ok="yes"
    echo "    OK: briefing has the auth-refresh plan line, edge-cache-gotcha, and the 18h finding"
  else
    echo "    WARNING: briefing did not contain all expected markers; see $base/serve-verify.log" >&2
    printf '%s\n' "$briefing" | sed 's/^/    | /'
  fi
fi

cat <<EOF

================================================================================
Fixture ready.${verify_ok:+  (self-check passed)}

1. Serve the seeded instance (leave this running in a terminal):

     SEAMLESS_CONFIG=$cfg \\
       $seamlessd serve

2. Record the WITH side (Seamless installed) -- run from the demo repo:

     cd $myapp
     CLAUDE_CONFIG_DIR=$cc_with claude

3. Record the WITHOUT side (vanilla) -- same repo, same prompt:

     cd $myapp
     CLAUDE_CONFIG_DIR=$cc_without claude

   Scene prompts (same on both sides):
     scene 1  "continue where we left off"
     scene 2  "the HTML responses are slow -- add caching"
     scene 3  "pick up the next step of the plan"   (re-seed with --race first)

4. Closing beat (with side, after the session ends):

     ls $data/memory/myapp/

Re-seed between takes so leases and findings stay fresh:
     scripts/scene-fixture/setup.sh --base $base${race:+ --race}

Transcripts land in \$CLAUDE_CONFIG_DIR/projects/<slug>/*.jsonl on each side.
================================================================================
EOF
