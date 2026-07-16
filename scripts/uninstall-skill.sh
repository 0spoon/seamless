#!/usr/bin/env bash
#
# Remove a repo-bundled Claude Code skill from ~/.claude/skills/.
#
#   scripts/uninstall-skill.sh <name>
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

DST_DIR="$HOME/.claude/skills/$NAME"

if [ -d "$DST_DIR" ]; then
    rm -rf "$DST_DIR"
    ok "Removed /$NAME skill at $DST_DIR"
else
    warn "/$NAME skill not installed at $DST_DIR (nothing to do)"
fi
