#!/usr/bin/env bash
#
# Remove a repo-bundled Seamless skill from Claude Code, Codex, or both.
#
#   scripts/uninstall-skill.sh <name> [claude|codex|all|detect]
#
# Counterpart to scripts/install-skill.sh. Safe to run when the skill is not
# installed (e.g. seam-onboard already self-removed after a successful run).
# detect (the default) resolves to the clients present on this machine, the
# same selection docs/install makes; with neither found there is nothing to
# remove from, so it exits cleanly. The Claude app chat surface loads no
# skills, so it is deliberately not a client here (see install-skill.sh).

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

CLIENT="${2:-${CLIENT:-detect}}"
if [ "$CLIENT" = detect ]; then
    DETECT_CLAUDE=0
    DETECT_CODEX=0
    if command -v claude >/dev/null 2>&1 || [ -d "$HOME/.claude" ]; then DETECT_CLAUDE=1; fi
    if command -v codex >/dev/null 2>&1 || [ -d "${CODEX_HOME:-$HOME/.codex}" ]; then DETECT_CODEX=1; fi
    case "$DETECT_CLAUDE:$DETECT_CODEX" in
    1:1) CLIENT=all ;;
    0:1) CLIENT=codex ;;
    1:0) CLIENT=claude ;;
    *)
        ok "neither Claude Code nor Codex detected; nothing to remove"
        exit 0
        ;;
    esac
fi

uninstall_one() {
    client=$1
    case "$client" in
    claude) skills="$HOME/.claude/skills" ;;
    codex) skills="${CODEX_HOME:-$HOME/.codex}/skills" ;;
    *)
        err "unknown client $client: valid values are claude, codex, all, detect"
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
    err "unknown client $CLIENT: valid values are claude, codex, all, detect"
    exit 1
    ;;
esac
