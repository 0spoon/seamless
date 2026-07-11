#!/usr/bin/env bash
#
# Install the seam-onboard Claude Code skill into ~/.claude/skills/.
# The skill itself is a one-shot onboarding helper: the user runs
# /seam-onboard, it interviews them and writes a Seamless-awareness block
# into a CLAUDE.md, then removes itself.
#
# Invoke via `make install-onboard-skill` from the Seamless repo when the
# user wants to (re-)install the skill. Installing overwrites any prior
# copy (including the v1 seam-onboard skill at the same path).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="$REPO_ROOT/scripts/seam-onboard-skill"
DST_DIR="$HOME/.claude/skills/seam-onboard"

info() { printf "\033[1;34m==>\033[0m %s\n" "$1"; }
ok()   { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
warn() { printf "\033[1;33m==>\033[0m %s\n" "$1"; }
err()  { printf "\033[1;31m==>\033[0m %s\n" "$1" >&2; }

if [ ! -f "$SRC_DIR/SKILL.md" ]; then
    err "Skill source not found at $SRC_DIR/SKILL.md"
    exit 1
fi

mkdir -p "$DST_DIR"
cp "$SRC_DIR/SKILL.md" "$DST_DIR/SKILL.md"

ok "Installed /seam-onboard skill at $DST_DIR"
echo
echo "  Run  /seam-onboard  in any Claude Code session to install"
echo "  Seamless awareness into a global or project CLAUDE.md."
echo "  The skill removes itself after a successful onboarding."
