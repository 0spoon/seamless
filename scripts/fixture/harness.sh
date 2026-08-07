#!/usr/bin/env bash
# harness.sh -- stand up the shared myapp + seeded-Seamless fixture, in one of
# two modes (plan:seambench step 2; the generalized scene-fixture/setup.sh).
# It creates, under a single throwaway base dir, everything a take needs:
#
#   --mode record   the terminal-scenes RECORDING fixture: one myapp repo, one
#                   seeded throwaway instance, a with-side and a without-side
#                   Claude config dir, and the printed interactive recipe --
#                   exactly what setup.sh used to stand up.
#   --mode bench    the agent-scenario BENCHMARK fixture: N named condition
#                   arms, each with its own myapp copy and (for Seamless-ful
#                   arms) its own seeded instance on its own port, emitting a
#                   per-arm env file that cmd/seambench consumes.
#
# Arm profiles (bench):
#   vanilla    bare Claude config dir -- no Seamless anywhere. Model-only control.
#   mechanism  everything `install-hooks` wires by default today: hooks
#              (SessionStart briefing + SubagentStart), MCP registration incl.
#              the initialize server instructions, and the default-installed
#              seam-onboard/seam-research skill files in the arm's skill home.
#   full       mechanism + the /seam-onboard CLAUDE.md awareness block
#              pre-written into that arm's myapp copy ONLY -- the vanilla and
#              mechanism arms' myapp stays Seamless-free
#              (memory scene-demo-repo-must-be-seamless-free).
#
# Nothing here touches the live ~/.seamless, ~/.claude, or ~/.codex: the seeded
# instances serve on non-live ports with their own keys, every side runs claude
# with its own CLAUDE_CONFIG_DIR, and install-hooks runs pinned to
# --client claude under the side's own throwaway HOME (see wire_claude_side).
#
# Usage:
#   scripts/fixture/harness.sh --mode record [--base DIR] [--port N] [--model ID] [--race] [--no-verify] [--no-build]
#   scripts/fixture/harness.sh --mode bench  [--conditions LIST] [--base DIR] [--port N] [--model ID] [--race] [--no-verify] [--no-build]
#
# --conditions (bench only) is a comma list of name[:profile[:client]] arms;
# profile defaults to the name and must be vanilla|mechanism|full, client
# defaults to claude (a codex arm is design-only until codex exec can run
# unattended -- hook trust and MCP approval are interactive-only). The default
# list is vanilla,mechanism,full. --port is the first port; each Seamless-ful
# arm takes the next one.
#
# --model pins the Claude model every side runs (written into each side's
# settings.json, so interactive record takes and headless bench runs alike use
# it). Default claude-opus-5: fixture runs must compare Seamless conditions,
# never model drift.
set -euo pipefail

# Optional features ship OFF, and a fixture instance is a NEW installation -- no
# trials yet, so the grandfather migration seeds nothing. Both halves of the
# fixture depend on research labs and trials being exposed: seambench grades on
# trial_query being called and trial.recorded being emitted, and the branding
# scenes/screenshots assert a seeded failed trial and show the Labs/Trials
# screens. So every fixture instance turns the feature on explicitly, in BOTH
# modes, two ways:
#
#   1. write_config writes `features: research: true` into each throwaway
#      seamless.yaml. This is the load-bearing one: cmd/seambench's arm runner
#      scrubs the whole SEAMLESS_* space from the daemon environment (arm.go
#      scrubEnv) and sets back only SEAMLESS_CONFIG, and the record recipe has
#      the operator start `seamlessd serve` by hand in another terminal. An
#      exported variable reaches neither.
#   2. The export below, which covers this script's own children (install-hooks,
#      the demoseed seeder, the briefing self-check daemon) regardless of which
#      config each resolves.
#
# The env var lives on the daemon side only -- never in the myapp working tree
# (memory scene-demo-repo-must-be-seamless-free).
export SEAMLESS_FEATURES_RESEARCH=1

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
mode=""
base=""
port=8099
race=""
verify=1
build=1
conditions="vanilla,mechanism,full"
conditions_set=0
model="claude-opus-5"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) mode="$2"; shift 2;;
    --base) base="$2"; shift 2;;
    --port) port="$2"; shift 2;;
    --model) model="$2"; shift 2;;
    --race) race="-race"; shift;;
    --no-verify) verify=0; shift;;
    --no-build) build=0; shift;;
    --conditions) conditions="$2"; conditions_set=1; shift 2;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) echo "harness: unknown arg $1" >&2; exit 2;;
  esac
done

case "$mode" in
  record|bench) ;;
  "") echo "harness: --mode record|bench is required" >&2; exit 2;;
  *) echo "harness: unknown --mode $mode (record|bench)" >&2; exit 2;;
esac
if [[ "$mode" == "record" && "$conditions_set" == "1" ]]; then
  echo "harness: --conditions applies to --mode bench only" >&2
  exit 2
fi
if [[ -z "$base" ]]; then
  case "$mode" in
    record) base="${TMPDIR:-/tmp}/seamless-scenes";;
    bench) base="${TMPDIR:-/tmp}/seamless-bench";;
  esac
fi

# -P: the PHYSICAL path, symlinks resolved. Every fixture path derives from
# $base, and the agent's client reports a resolved cwd -- so a logical base (on
# macOS $TMPDIR sits under /var -> /private/var) registers the repo under a key
# no session start can match, minting a second empty project and hiding the whole
# seeded fixture from the agent. Keep -P here and in cmd/seambench's --base.
base=$(mkdir -p "$base" && cd "$base" && pwd -P)
seamlessd="$repo_root/bin/seamlessd"
seam="$repo_root/bin/seam"

echo "==> base: $base"

# Build the binaries (idempotent; fast when up to date).
if [[ "$build" == "1" ]]; then
  echo "==> building seamlessd + seam"
  make -C "$repo_root" build >/dev/null
else
  echo "==> skipping build (--no-build); using existing bin/"
fi
if [[ ! -x "$seamlessd" || ! -x "$seam" ]]; then
  echo "harness: $seamlessd / $seam missing; run 'make build' first (or drop --no-build)" >&2
  exit 1
fi

# write_config <cfg> <data> <port> <keyfile> -- throwaway config + key for one
# seeded instance. The key persists across re-seeds of the same dir so an open
# claude session's MCP registration stays valid. Echoes the key.
#
# The features block is not optional here: see the SEAMLESS_FEATURES_RESEARCH
# note at the top of this file. Any daemon started against this config -- by the
# seambench runner, by the operator, or by the self-check -- gets the research
# tools and screens the fixture's graders and scenes assume.
write_config() {
  local cfg="$1" data="$2" p="$3" keyfile="$4" key
  if [[ -f "$keyfile" ]]; then key=$(cat "$keyfile"); else key=$(openssl rand -hex 32); echo "$key" >"$keyfile"; fi
  cat >"$cfg" <<EOF
# Throwaway config for the fixture harness. NOT a live instance.
addr: "127.0.0.1:$p"
data_dir: "$data"
mcp:
  api_key: "$key"
features:
  # A fixture instance is a new installation, so the optional research feature
  # would default off; seambench's graders and the branding scenes both need it.
  research: true
EOF
  printf '%s' "$key"
}

# seed_scenes <data> <myapp> -- fresh scene state each run (leases, findings,
# and backdated ages must not go stale between takes).
seed_scenes() {
  local data="$1" myapp="$2"
  rm -rf "$data"
  mkdir -p "$data"
  ( cd "$repo_root" && go run ./cmd/demoseed -scenes -data "$data" -repo "$myapp" $race )
}

# wire_claude_side <home> <ccdir> <cfg> <port> -- install Seamless hooks, MCP
# registration, and the default skills into one side's throwaway config dir.
#     --client claude is load-bearing: without it install-hooks detects clients,
#     and on a machine with Codex it would prompt interactively (TTY) or wire
#     the LIVE ~/.codex at the fixture's throwaway daemon (non-TTY).
#     HOME is overridden to the side's throwaway home for the same guarantee:
#     the default skill install resolves ~/.claude/skills from $HOME, so
#     without the override the skills would land in the LIVE skill home -- and
#     the side's claude, reading $CLAUDE_CONFIG_DIR/skills, would never see
#     them. The SEAMLESS_NO_*_SKILL opt-outs are cleared so a user-level
#     opt-out cannot hollow out what a wired side gets by default.
wire_claude_side() {
  local home="$1" ccdir="$2" cfg="$3" p="$4"
  rm -rf "$ccdir" && mkdir -p "$ccdir"
  HOME="$home" SEAMLESS_NO_ONBOARD_SKILL= SEAMLESS_NO_RESEARCH_SKILL= \
    SEAMLESS_CONFIG="$cfg" CLAUDE_CONFIG_DIR="$ccdir" \
    "$seamlessd" install-hooks --client claude --settings "$ccdir/settings.json" \
      --url "http://127.0.0.1:$p" --seam "$seam" | sed 's/^/    /'
}

# set_model <ccdir> -- pin the Claude model for every session under this
# side's config dir. Written after install-hooks so the hook install can never
# clobber it; hooks.Install itself preserves unrelated settings keys.
set_model() {
  local ccdir="$1"
  python3 - "$ccdir/settings.json" "$model" <<'PYEOF'
import json, sys
path, model = sys.argv[1], sys.argv[2]
try:
    with open(path) as f:
        data = json.load(f)
except FileNotFoundError:
    data = {}
data["model"] = model
with open(path, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
PYEOF
}

# verify_briefing <label> <cfg> <port> <key> <cwd> <log> -- serve briefly and
# confirm the seeded briefing carries the scene markers. Non-fatal: a WARNING
# still leaves the fixture standing so the operator can inspect it.
verify_briefing() {
  local label="$1" cfg="$2" p="$3" key="$4" cwd="$5" log="$6" briefing vpid
  echo "==> verifying the $label briefing"
  SEAMLESS_CONFIG="$cfg" "$seamlessd" serve >"$log" 2>&1 &
  vpid=$!
  trap '[[ -n "${vpid:-}" ]] && kill "$vpid" 2>/dev/null || true' EXIT
  for _ in $(seq 1 40); do curl -sf "http://127.0.0.1:$p/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
  briefing=$(curl -s -X POST "http://127.0.0.1:$p/api/hooks/session-start" \
    -H "Authorization: Bearer $key" -H "Content-Type: application/json" \
    -d "{\"session_id\":\"setup-verify\",\"cwd\":\"$cwd\",\"source\":\"startup\"}" \
    | python3 -c 'import sys,json; print(json.load(sys.stdin).get("hookSpecificOutput",{}).get("additionalContext",""))' 2>/dev/null || true)
  kill "$vpid" 2>/dev/null || true; wait "$vpid" 2>/dev/null || true; vpid=""
  if grep -q -- "- auth-refresh -- " <<<"$briefing" && grep -q "edge-cache-gotcha" <<<"$briefing" && grep -q "(18h)" <<<"$briefing"; then
    echo "    OK: briefing has the auth-refresh plan line, edge-cache-gotcha, and the 18h finding"
    return 0
  fi
  echo "    WARNING: briefing did not contain all expected markers; see $log" >&2
  printf '%s\n' "$briefing" | sed 's/^/    | /'
  return 1
}

# ---------------------------------------------------------------------------
# record mode -- the terminal-scenes recording fixture (the old setup.sh flow).
# ---------------------------------------------------------------------------
run_record() {
  local myapp="$base/myapp" data="$base/data" cfg="$base/seamless.yaml" keyfile="$base/key.txt"
  local home_with="$base/home-with" home_without="$base/home-without"
  local cc_with="$home_with/.claude" cc_without="$home_without/.claude"
  local key verify_ok=""

  echo "==> scaffolding myapp repo"
  "$repo_root/scripts/fixture/make-myapp.sh" "$myapp" >/dev/null

  key=$(write_config "$cfg" "$data" "$port" "$keyfile")

  echo "==> seeding scene fixture${race:+ (race variant)}"
  seed_scenes "$data" "$myapp"

  echo "==> wiring with-side harness ($cc_with)"
  wire_claude_side "$home_with" "$cc_with" "$cfg" "$port"
  set_model "$cc_with"

  echo "==> wiring without-side harness ($cc_without)"
  rm -rf "$cc_without" && mkdir -p "$cc_without"
  set_model "$cc_without"

  if [[ "$verify" == "1" ]]; then
    verify_briefing "with-side" "$cfg" "$port" "$key" "$myapp" "$base/serve-verify.log" && verify_ok="yes" || true
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
     scripts/fixture/harness.sh --mode record --base $base${race:+ --race}

Both sides are pinned to model $model (settings.json; override with --model).
Transcripts land in \$CLAUDE_CONFIG_DIR/projects/<slug>/*.jsonl on each side.
================================================================================
EOF
}

# ---------------------------------------------------------------------------
# bench mode -- N named condition arms for cmd/seambench.
# ---------------------------------------------------------------------------

# client_config_env <client> -- the client-keyed pointer at the arm's agent
# home: which env var the runner must set, so a future codex arm changes a
# table entry here instead of the harness/runner interface.
client_config_env() {
  case "$1" in
    claude) printf 'CLAUDE_CONFIG_DIR';;
    codex) printf 'CODEX_HOME';;
    *) return 1;;
  esac
}

# write_onboard_block <ccdir> <myapp> -- the full arm's extra: pre-write the
# exact awareness block /seam-onboard would append, extracted from the arm's
# own installed skill so the harness and the skill cannot drift. It goes into
# that arm's myapp copy ONLY, as a committed CLAUDE.md, so the final run diff
# stays clean and the other arms' myapp stays Seamless-free.
write_onboard_block() {
  local ccdir="$1" myapp="$2"
  local skill_md="$ccdir/skills/seam-onboard/SKILL.md" block
  if [[ ! -f "$skill_md" ]]; then
    echo "harness: $skill_md missing; cannot write the full arm's CLAUDE.md block" >&2
    exit 1
  fi
  block=$(sed -n '/^<!-- seam-onboard:start -->$/,/^<!-- seam-onboard:end -->$/p' "$skill_md")
  if [[ -z "$block" ]] || ! grep -q "## Seamless" <<<"$block"; then
    echo "harness: could not extract the seam-onboard awareness block from $skill_md" >&2
    exit 1
  fi
  echo "    writing the /seam-onboard CLAUDE.md block into the arm's myapp"
  printf '%s\n' "$block" >"$myapp/CLAUDE.md"
  ( cd "$myapp" \
    && git add CLAUDE.md \
    && git -c user.name='myapp' -c user.email='dev@myapp.local' commit -qm 'myapp: add agent instructions' )
}

# write_arm_env <envfile> ... -- one shell-sourceable KEY="value" file per arm;
# cmd/seambench sources or parses it. SEAMBENCH_CLIENT_CONFIG_ENV names the
# client-keyed variable (claude -> CLAUDE_CONFIG_DIR, codex -> CODEX_HOME), so
# the runner points the agent at its home without per-client knowledge.
write_arm_env() {
  local envfile="$1" name="$2" profile="$3" client="$4" cfgvar="$5" ccdir="$6"
  local home="$7" myapp="$8" cfg="$9" armport="${10}" data="${11}" keyfile="${12}"
  {
    echo "# arm \"$name\" -- generated by scripts/fixture/harness.sh --mode bench"
    echo "SEAMBENCH_CONDITION=\"$name\""
    echo "SEAMBENCH_PROFILE=\"$profile\""
    echo "SEAMBENCH_CLIENT=\"$client\""
    echo "SEAMBENCH_MODEL=\"$model\""
    echo "SEAMBENCH_CLIENT_CONFIG_ENV=\"$cfgvar\""
    echo "$cfgvar=\"$ccdir\""
    echo "SEAMBENCH_HOME=\"$home\""
    echo "SEAMBENCH_MYAPP=\"$myapp\""
    if [[ "$profile" == "vanilla" ]]; then
      echo "SEAMBENCH_SEAMLESS=\"0\""
    else
      echo "SEAMBENCH_SEAMLESS=\"1\""
      echo "SEAMLESS_CONFIG=\"$cfg\""
      echo "SEAMBENCH_PORT=\"$armport\""
      echo "SEAMBENCH_URL=\"http://127.0.0.1:$armport\""
      echo "SEAMBENCH_DATA_DIR=\"$data\""
      echo "SEAMBENCH_KEY_FILE=\"$keyfile\""
    fi
  } >"$envfile"
}

run_bench() {
  local armsdir="$base/arms" next_port="$port"
  local entry name profile client extra spec summary=""
  local entries=() parsed=() seen=" "

  # Parse and validate the whole condition list first, so a bad entry fails
  # before any arm is built.
  IFS=',' read -r -a entries <<<"$conditions"
  for entry in "${entries[@]}"; do
    entry=$(printf '%s' "$entry" | tr -d '[:space:]')
    [[ -z "$entry" ]] && continue
    IFS=':' read -r name profile client extra <<<"$entry"
    if [[ -n "$extra" ]]; then
      echo "harness: bad condition \"$entry\" (want name[:profile[:client]])" >&2
      exit 2
    fi
    [[ -z "$profile" ]] && profile="$name"
    [[ -z "$client" ]] && client="claude"
    if ! [[ "$name" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
      echo "harness: bad condition name \"$name\" (lowercase letters, digits, -, _)" >&2
      exit 2
    fi
    case "$profile" in
      vanilla|mechanism|full) ;;
      *) echo "harness: condition $name: unknown profile \"$profile\" (vanilla|mechanism|full)" >&2; exit 2;;
    esac
    case "$client" in
      claude) ;;
      codex)
        echo "harness: condition $name: codex arms are design-only for now -- codex exec cannot run unattended (interactive hook trust + MCP approval; see memory codex-headless-two-gates-hooktrust-and-mcp-approval)" >&2
        exit 2;;
      *) echo "harness: condition $name: unknown client \"$client\" (claude)" >&2; exit 2;;
    esac
    case "$seen" in
      *" $name "*) echo "harness: duplicate condition name \"$name\"" >&2; exit 2;;
    esac
    seen="$seen$name "
    parsed+=("$name:$profile:$client")
  done
  if [[ ${#parsed[@]} -eq 0 ]]; then
    echo "harness: --conditions selected no arms" >&2
    exit 2
  fi

  local arm home ccdir myapp data cfg keyfile envfile cfgvar key armport
  for spec in "${parsed[@]}"; do
    IFS=':' read -r name profile client <<<"$spec"
    arm="$armsdir/$name"
    home="$arm/home"
    myapp="$arm/myapp"
    data="$arm/data"
    cfg="$arm/seamless.yaml"
    keyfile="$arm/key.txt"
    envfile="$arm/env"
    cfgvar=$(client_config_env "$client")
    case "$client" in
      codex) ccdir="$home/.codex";;
      *) ccdir="$home/.claude";;
    esac
    key=""
    armport=""

    echo ""
    echo "==> arm $name (profile=$profile, client=$client)"
    mkdir -p "$arm"
    echo "    scaffolding myapp"
    "$repo_root/scripts/fixture/make-myapp.sh" "$myapp" >/dev/null

    if [[ "$profile" == "vanilla" ]]; then
      rm -rf "$ccdir" && mkdir -p "$ccdir"
      set_model "$ccdir"
    else
      armport="$next_port"
      next_port=$((next_port + 1))
      key=$(write_config "$cfg" "$data" "$armport" "$keyfile")
      echo "    seeding scene fixture${race:+ (race variant)} (port $armport)"
      seed_scenes "$data" "$myapp"
      echo "    wiring $client harness ($ccdir)"
      wire_claude_side "$home" "$ccdir" "$cfg" "$armport"
      set_model "$ccdir"
      if [[ "$profile" == "full" ]]; then
        write_onboard_block "$ccdir" "$myapp"
      fi
      if [[ "$verify" == "1" ]]; then
        verify_briefing "$name arm" "$cfg" "$armport" "$key" "$myapp" "$arm/serve-verify.log" || true
      fi
    fi

    write_arm_env "$envfile" "$name" "$profile" "$client" "$cfgvar" "$ccdir" \
      "$home" "$myapp" "$cfg" "$armport" "$data" "$keyfile"
    if [[ "$profile" == "vanilla" ]]; then
      summary="$summary  $name  profile=$profile client=$client (no seamless)
      env $envfile
"
    else
      summary="$summary  $name  profile=$profile client=$client port=$armport
      env $envfile
"
    fi
  done

  cat <<EOF

================================================================================
Bench fixture ready: ${#parsed[@]} arm(s) under $armsdir

$summary
Each arm's env file is shell-sourceable KEY="value" lines for cmd/seambench:
the client-keyed config var it names (SEAMBENCH_CLIENT_CONFIG_ENV) points the
agent at that arm's home. Serve a Seamless-ful arm's instance manually with:

     SEAMLESS_CONFIG=<arm>/seamless.yaml $seamlessd serve

All arms are pinned to model $model (settings.json; override with --model).

Re-arm (fresh myapp, data, leases, findings):
     scripts/fixture/harness.sh --mode bench --base $base --conditions $conditions${race:+ --race}
================================================================================
EOF
}

case "$mode" in
  record) run_record;;
  bench) run_bench;;
esac
