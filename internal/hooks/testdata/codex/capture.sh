#!/usr/bin/env bash
# Capture Codex hook and MCP contracts without touching the operator's CODEX_HOME.

set -euo pipefail

usage() {
	cat >&2 <<'EOF'
usage: capture.sh setup ROOT [AUTH_JSON]
       capture.sh mcp ROOT
       capture.sh exec ROOT
       capture.sh tui ROOT
       capture.sh oversize ROOT
       capture.sh clean-auth ROOT

ROOT must be a new absolute path outside the repository. setup creates an
isolated CODEX_HOME, throwaway git repository, logging hooks, and raw capture
directories. AUTH_JSON is copied with mode 0600 into that isolated home; it is
never printed or copied into a fixture directory. Run clean-auth when the live
captures finish.
EOF
	exit 2
}

die() {
	printf 'capture-codex-contract: %s\n' "$*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

require_root() {
	contract_root=${1:-}
	[[ "$contract_root" == /* ]] || die "ROOT must be an absolute path"
	[[ "$contract_root" != / ]] || die "ROOT may not be /"
	local parent repo_root
	parent=$(cd "$(dirname "$contract_root")" && pwd -P) || die "ROOT parent does not exist"
	contract_root="$parent/$(basename "$contract_root")"
	repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd -P)
	case "$contract_root" in
		"$repo_root" | "$repo_root"/*)
			die "ROOT must be outside the repository"
			;;
	esac
}

require_capture() {
	require_root "$1"
	[[ -d "$contract_root/home" ]] || die "run setup first: $contract_root/home is missing"
	[[ -d "$contract_root/repo/.git" ]] || die "run setup first: $contract_root/repo is not a git repository"
}

hook_prompt='Contract capture only. Report every SEAMLESS_CONTRACT_* value visible to you. Then spawn exactly one general-purpose subagent and ask it to return exactly SUBAGENT_CONTRACT_DONE. Wait for it. Finish with CONTRACT_CAPTURE_DONE and the sentinel values, without inspecting or changing files.'

setup_capture() {
	require_root "$1"
	local auth_source=${2:-}

	[[ ! -e "$contract_root" ]] || die "refusing to reuse existing ROOT: $contract_root"
	require_command codex
	require_command git
	require_command jq
	require_command shasum

	mkdir -p "$contract_root/home" "$contract_root/raw/exec" "$contract_root/raw/tui" \
		"$contract_root/raw/oversize" "$contract_root/repo"
	git -C "$contract_root/repo" init -q
	git -C "$contract_root/repo" config user.name 'Codex Contract Capture'
	git -C "$contract_root/repo" config user.email 'capture@example.invalid'
	printf '%s\n' 'Throwaway repository for sanitized Codex contract capture.' >"$contract_root/repo/README.md"
	git -C "$contract_root/repo" add README.md
	git -C "$contract_root/repo" commit -qm 'initialize contract fixture'

	if [[ -n "$auth_source" ]]; then
		[[ -f "$auth_source" ]] || die "AUTH_JSON is not a file: $auth_source"
		install -m 0600 "$auth_source" "$contract_root/home/auth.json"
	fi

	local hook_script="$contract_root/capture-hook.sh"
	cat >"$hook_script" <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail

event=${1:?hook event is required}
capture_root=${SEAMLESS_CODEX_CAPTURE_ROOT:?capture root is required}
frontend=${SEAMLESS_CODEX_CAPTURE_FRONTEND:?capture frontend is required}
raw_dir="$capture_root/raw/$frontend"
mkdir -p "$raw_dir"

stem=$(printf '%s' "$event" | tr '[:upper:]' '[:lower:]')
event_dir=$(mktemp -d "$raw_dir/$stem.XXXXXX")
input="$event_dir/input.json"
output="$event_dir/output.json"
tee "$input" >/dev/null

case "$event" in
	SessionStart)
		context='SEAMLESS_CONTRACT_SESSION_START=zebra-4417'
		if [[ ${SEAMLESS_CODEX_OVERSIZED_HOOK:-0} == 1 ]]; then
			context=$(awk 'BEGIN { for (i = 0; i < 3000; i++) printf "OVERSIZED_SENTINEL_%04d ", i; print "OVERSIZED_SENTINEL_END" }')
		fi
		jq -cn --arg context "$context" \
			'{hookSpecificOutput:{hookEventName:"SessionStart",additionalContext:$context}}' >"$output"
		;;
	UserPromptSubmit)
		jq -cn '{hookSpecificOutput:{hookEventName:"UserPromptSubmit",additionalContext:"SEAMLESS_CONTRACT_USER_PROMPT_SUBMIT=falcon-9928"}}' >"$output"
		;;
	SubagentStart)
		jq -cn '{hookSpecificOutput:{hookEventName:"SubagentStart",additionalContext:"SEAMLESS_CONTRACT_SUBAGENT_START=otter-3184"}}' >"$output"
		;;
	Stop)
		jq -cn '{continue:true,systemMessage:"SEAMLESS_CONTRACT_STOP=lynx-7702"}' >"$output"
		;;
	SubagentStop)
		jq -cn '{continue:true,systemMessage:"SEAMLESS_CONTRACT_SUBAGENT_STOP=ibis-2259"}' >"$output"
		;;
	*)
		printf 'unsupported event: %s\n' "$event" >&2
		exit 2
		;;
esac

cat "$output"
HOOK
	chmod 0700 "$hook_script"

	local quoted_hook
	printf -v quoted_hook '%q' "$hook_script"
	jq -n \
		--arg session_start "$quoted_hook SessionStart" \
		--arg prompt "$quoted_hook UserPromptSubmit" \
		--arg stop "$quoted_hook Stop" \
		--arg subagent_start "$quoted_hook SubagentStart" \
		--arg subagent_stop "$quoted_hook SubagentStop" \
		'{
			description:"Seamless Codex contract capture; temporary and isolated",
			hooks:{
				SessionStart:[{hooks:[{type:"command",command:$session_start,commandWindows:"cmd.exe /C exit 97",timeout:10}]}],
				UserPromptSubmit:[{hooks:[{type:"command",command:$prompt,command_windows:"cmd.exe /C exit 97",timeout:10}]}],
				Stop:[{hooks:[{type:"command",command:$stop,timeout:10}]}],
				SubagentStart:[{hooks:[{type:"command",command:$subagent_start,commandWindows:"cmd.exe /C exit 97",timeout:10}]}],
				SubagentStop:[{hooks:[{type:"command",command:$subagent_stop,command_windows:"cmd.exe /C exit 97",timeout:10}]}]
			}
		}' >"$contract_root/home/hooks.json"

	cat >"$contract_root/home/config.toml" <<'TOML'
[features]
hooks = true
multi_agent = true
TOML

	{
		codex --version
		printf 'executable=%s\n' "$(command -v codex)"
		printf 'executable_sha256='
		shasum -a 256 "$(command -v codex)" | awk '{print $1}'
		printf 'platform=%s/%s\n' "$(uname -s)" "$(uname -m)"
	} >"$contract_root/metadata.raw.txt"

	printf 'capture root: %s\n' "$contract_root"
	printf 'auth copied: %s\n' "$([[ -f "$contract_root/home/auth.json" ]] && printf yes || printf no)"
}

write_mcp_config() {
	local target=$1
	local transport=$2
	local enabled=$3
	mkdir -p "$target"

	if [[ "$transport" == stdio ]]; then
		cat >"$target/config.toml" <<EOF
[mcp_servers.seamless]
command = "/opt/seam/bin/seam"
args = ["mcp-proxy", "--config", "/Users/dev/.config/seamless/seamless.yaml"]
env = { SEAMLESS_FIXTURE_ALPHA = "red", SEAMLESS_FIXTURE_BETA = "blue" }
env_vars = ["PATH", { name = "SEAMLESS_RUNTIME", source = "local" }]
cwd = "/Users/dev/myrepo"
enabled = $enabled
startup_timeout_sec = 12.5
tool_timeout_sec = 45
enabled_tools = ["recall", "memory_read"]
disabled_tools = ["memory_delete"]
EOF
	else
		cat >"$target/config.toml" <<EOF
[mcp_servers.seamless]
url = "https://mcp.example.invalid/api/mcp"
bearer_token_env_var = "SEAMLESS_FIXTURE_TOKEN"
http_headers = { "X-Fixture" = "contract" }
env_http_headers = { "X-Runtime-Token" = "SEAMLESS_RUNTIME_TOKEN" }
enabled = $enabled
startup_timeout_sec = 12.5
tool_timeout_sec = 45
enabled_tools = ["recall", "memory_read"]
disabled_tools = ["memory_delete"]
EOF
	fi
}

capture_mcp() {
	require_root "$1"
	require_command codex
	local out="$contract_root/mcp"
	mkdir -p "$out"

	local transport state enabled home
	for transport in stdio streamable-http; do
		for state in enabled disabled; do
			enabled=true
			[[ "$state" == enabled ]] || enabled=false
			home="$contract_root/mcp-homes/$transport-$state"
			write_mcp_config "$home" "${transport%-http}" "$enabled"
			CODEX_HOME="$home" codex mcp get seamless --json >"$out/$transport-$state.json"
		done
	done
}

run_exec_capture() {
	require_capture "$1"
	[[ -f "$contract_root/home/auth.json" ]] || die "isolated auth is missing; rerun setup with AUTH_JSON"
	export CODEX_HOME="$contract_root/home"
	export SEAMLESS_CODEX_CAPTURE_ROOT="$contract_root"
	export SEAMLESS_CODEX_CAPTURE_FRONTEND=exec
	codex exec --dangerously-bypass-hook-trust --color never -s read-only -C "$contract_root/repo" "$hook_prompt"
}

run_tui_capture() {
	require_capture "$1"
	[[ -f "$contract_root/home/auth.json" ]] || die "isolated auth is missing; rerun setup with AUTH_JSON"
	export CODEX_HOME="$contract_root/home"
	export SEAMLESS_CODEX_CAPTURE_ROOT="$contract_root"
	export SEAMLESS_CODEX_CAPTURE_FRONTEND=tui
	codex --dangerously-bypass-hook-trust --no-alt-screen -s read-only -C "$contract_root/repo" "$hook_prompt"
}

run_oversize_capture() {
	require_capture "$1"
	[[ -f "$contract_root/home/auth.json" ]] || die "isolated auth is missing; rerun setup with AUTH_JSON"
	export CODEX_HOME="$contract_root/home"
	export SEAMLESS_CODEX_CAPTURE_ROOT="$contract_root"
	export SEAMLESS_CODEX_CAPTURE_FRONTEND=oversize
	export SEAMLESS_CODEX_OVERSIZED_HOOK=1
	codex exec --dangerously-bypass-hook-trust --color never -s read-only -C "$contract_root/repo" \
		'Report whether SessionStart context contains OVERSIZED_SENTINEL_END and whether it names a saved full hook output path. Do not inspect the path. Reply exactly CONTRACT_OVERSIZE_DONE with those two facts.'
}

clean_auth() {
	require_root "$1"
	local auth="$contract_root/home/auth.json"
	if [[ -f "$auth" ]]; then
		rm -f -- "$auth"
		printf 'removed isolated auth copy: %s\n' "$auth"
	else
		printf 'isolated auth copy already absent: %s\n' "$auth"
	fi
}

[[ $# -ge 2 ]] || usage
command_name=$1
shift

case "$command_name" in
	setup)
		[[ $# -le 2 ]] || usage
		setup_capture "$@"
		;;
	mcp)
		[[ $# -eq 1 ]] || usage
		capture_mcp "$1"
		;;
	exec)
		[[ $# -eq 1 ]] || usage
		run_exec_capture "$1"
		;;
	tui)
		[[ $# -eq 1 ]] || usage
		run_tui_capture "$1"
		;;
	oversize)
		[[ $# -eq 1 ]] || usage
		run_oversize_capture "$1"
		;;
	clean-auth)
		[[ $# -eq 1 ]] || usage
		clean_auth "$1"
		;;
	*)
		usage
		;;
esac
