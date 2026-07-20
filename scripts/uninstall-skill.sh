#!/usr/bin/env bash
#
# Remove a repo-bundled Seamless skill from Claude Code, Codex, or both.
#
#   scripts/uninstall-skill.sh <name> [claude|codex|all]
#
# Counterpart to scripts/install-skill.sh. Safe to run when the skill is not
# installed (e.g. seam-onboard already self-removed after a successful run).

set -euo pipefail

ok()   { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
warn() { printf "\033[1;33m==>\033[0m %s\n" "$1"; }
err()  { printf "\033[1;31m==>\033[0m %s\n" "$1" >&2; }

NAME="${1:-}"
if [ -z "$NAME" ]; then
    err "usage: $0 <skill-name>"
    exit 1
fi
# The name lands in rm -rf "$skills/$NAME"; a separator or dot-prefixed name
# could point that at a directory outside the skill root.
case "$NAME" in
*/* | *\\* | .*)
    err "invalid skill name $NAME: must be a plain directory name"
    exit 1
    ;;
esac

CLIENT="${2:-${CLIENT:-claude}}"

uninstall_one() {
    client=$1
    case "$client" in
    claude) skills="$HOME/.claude/skills" ;;
    codex) skills="${CODEX_HOME:-$HOME/.codex}/skills" ;;
    *)
        err "unknown client $client: valid values are claude, codex, all"
        exit 1
        ;;
    esac
    dst="$skills/$NAME"
    if [ -d "$dst" ]; then
        rm -rf "$dst"
        ok "Removed $NAME skill at $dst"
    else
        warn "$NAME skill not installed at $dst (nothing to do)"
    fi
    if [ "$NAME" = seam-onboard ]; then
        rm -f "$skills/.seam-onboard-delivered"
    fi
}

case "$CLIENT" in
claude | codex) uninstall_one "$CLIENT" ;;
all)
    uninstall_one claude
    uninstall_one codex
    ;;
*)
    err "unknown client $CLIENT: valid values are claude, codex, all"
    exit 1
    ;;
esac
