#!/usr/bin/env bash
#
# Remove the seam-onboard Claude Code skill from ~/.claude/skills/.
# Counterpart to scripts/install-onboard-skill.sh. Safe to run even
# when the skill is not installed (e.g. it already self-removed after
# a successful /seam-onboard run).

set -euo pipefail

DST_DIR="$HOME/.claude/skills/seam-onboard"

ok()   { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
warn() { printf "\033[1;33m==>\033[0m %s\n" "$1"; }

if [ -d "$DST_DIR" ]; then
    rm -rf "$DST_DIR"
    ok "Removed /seam-onboard skill at $DST_DIR"
else
    warn "/seam-onboard skill not installed at $DST_DIR (nothing to do)"
fi
