#!/usr/bin/env bash
#
# Install a repo-bundled Seamless skill for Claude Code, Codex, or both.
#
#   scripts/install-skill.sh <name> [claude|codex|all]
#   CLIENT=codex scripts/install-skill.sh <name>
#
# Invoke via the make targets (install-onboard-skill, install-research-skill).
# Installing refreshes the maintained package at each selected client home.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ok()  { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
err() { printf "\033[1;31m==>\033[0m %s\n" "$1" >&2; }

NAME="${1:-}"
if [ -z "$NAME" ]; then
    err "usage: $0 <skill-name>"
    exit 1
fi

CLIENT="${2:-${CLIENT:-claude}}"
SRC_DIR="$REPO_ROOT/internal/skills/assets/$NAME"

if [ ! -f "$SRC_DIR/SKILL.md" ]; then
    err "Skill source not found at $SRC_DIR/SKILL.md"
    exit 1
fi

install_one() {
    client=$1
    case "$client" in
    claude)
        skills="$HOME/.claude/skills"
        invoke="/$NAME"
        ;;
    codex)
        skills="${CODEX_HOME:-$HOME/.codex}/skills"
        invoke="\$$NAME"
        ;;
    *)
        err "unknown client $client: valid values are claude, codex, all"
        exit 1
        ;;
    esac

    dst="$skills/$NAME"
    mkdir -p "$dst"
    cp -R "$SRC_DIR/." "$dst/"
    if [ "$NAME" = seam-onboard ]; then
        : >"$skills/.seam-onboard-delivered"
    fi
    ok "Installed $invoke skill at $dst"
}

case "$CLIENT" in
claude | codex) install_one "$CLIENT" ;;
all)
    install_one claude
    install_one codex
    ;;
*)
    err "unknown client $CLIENT: valid values are claude, codex, all"
    exit 1
    ;;
esac
echo

case "$NAME" in
seam-onboard)
    echo "  Run  /seam-onboard  in Claude Code or  \$seam-onboard  in Codex"
    echo "  to install awareness into global or project instructions."
    echo "  The skill removes itself after a successful onboarding."
    ;;
seam-research)
    echo "  Run  /seam-research  in Claude Code or  \$seam-research  in Codex"
    echo "  to open or resume a systematic research lab."
    echo "  The skill persists; re-run this target to update it."
    ;;
esac
