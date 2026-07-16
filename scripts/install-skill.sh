#!/usr/bin/env bash
#
# Install a repo-bundled Claude Code skill into ~/.claude/skills/.
#
#   scripts/install-skill.sh <name>    # source: scripts/<name>-skill/SKILL.md
#
# Invoke via the make targets (install-onboard-skill, install-research-skill).
# Installing overwrites any prior copy at the destination, including the stale
# Seam v1 skills that lived at the same paths.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ok()  { printf "\033[1;32m==>\033[0m %s\n" "$1"; }
err() { printf "\033[1;31m==>\033[0m %s\n" "$1" >&2; }

NAME="${1:-}"
if [ -z "$NAME" ]; then
    err "usage: $0 <skill-name>"
    exit 1
fi

SRC_DIR="$REPO_ROOT/scripts/$NAME-skill"
DST_DIR="$HOME/.claude/skills/$NAME"

if [ ! -f "$SRC_DIR/SKILL.md" ]; then
    err "Skill source not found at $SRC_DIR/SKILL.md"
    exit 1
fi

mkdir -p "$DST_DIR"
cp "$SRC_DIR/SKILL.md" "$DST_DIR/SKILL.md"

ok "Installed /$NAME skill at $DST_DIR"
echo

case "$NAME" in
seam-onboard)
    echo "  Run  /seam-onboard  in any Claude Code session to install"
    echo "  Seamless awareness into a global or project CLAUDE.md."
    echo "  The skill removes itself after a successful onboarding."
    ;;
seam-research)
    echo "  Run  /seam-research <lab-name> <problem>  to open a research lab,"
    echo "  or let Claude activate it on its own for systematic investigations."
    echo "  The skill persists; re-run this target to update it."
    ;;
esac
