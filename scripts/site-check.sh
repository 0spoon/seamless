#!/bin/sh
# site-check: assert the hand-written landing page still tells the truth.
#
# Why this exists: docs-check gates docs-src/ -> docs/docs/ and nothing else.
# The landing page is hand-written -- docsgen never emits it, no test reads it,
# no target diffs it -- so it can contradict the product indefinitely with the
# whole gate green. That is not hypothetical. The curl|sh installer landed in
# d1e926b with README.md, docs-src/, and the regenerated docs/docs/ all correct
# and `make check` green, while the hero pill went on telling every visitor to
# run `go install github.com/0spoon/seamless/cmd/...@latest`. The front door
# advertised the old front door for a day.
#
# Four assertions, each one a thing a machine can actually verify:
#
#   1. the hero pills run $INSTALL_CMD and $WIN_INSTALL_CMD, not other routes
#   2. every surface that teaches installing teaches the SAME two commands
#   3. every `seamlessd <sub>` the page names in a command context is real
#   4. each copy button copies the command it visibly shows
#
# Deliberately not a prose linter: claims like "one binary, no ceremony" are the
# author's problem. Commands are checkable, so these are checked.

set -eu

PAGE=docs/index.html
MAIN=cmd/seamlessd/main.go

# The canonical install commands, and the single place they are written down.
# They are marketing claims as much as commands, so changing one should be
# deliberate and should drag every surface along: edit it here, and this check
# names every file that still disagrees.
INSTALL_CMD='curl -fsSL https://thereisnospoon.org/install | sh'
WIN_INSTALL_CMD='irm https://thereisnospoon.org/install.ps1 | iex'

# Every doc whose job includes telling a new user how to install.
SURFACES="$PAGE README.md docs-src/quickstart.md docs-src/install.md"

fail=0
err() {
	printf 'site-check: %s\n' "$1" >&2
	fail=1
}

# 1. The hero pills are the first commands anyone sees. One per OS, and each
#    must be the real one -- no more, no less.
pills=$(grep 'class="install-pill"' "$PAGE" | grep -oE 'data-copy="[^"]*"' |
	sed 's/^data-copy="//; s/"$//')
if [ -z "$pills" ]; then
	err "no install-pill with a data-copy found in $PAGE (did the hero markup change?)"
else
	printf '%s\n' "$pills" | grep -qxF "$INSTALL_CMD" ||
		err "no hero install pill runs [$INSTALL_CMD]"
	printf '%s\n' "$pills" | grep -qxF "$WIN_INSTALL_CMD" ||
		err "no hero install pill runs [$WIN_INSTALL_CMD]"
	stray=$(printf '%s\n' "$pills" | grep -vxF "$INSTALL_CMD" | grep -vxF "$WIN_INSTALL_CMD" || true)
	[ -z "$stray" ] || err "hero install pill runs unexpected command [$stray]"
fi

# 2. Nobody should have to wonder which install command is the current one --
#    on either OS.
for f in $SURFACES; do
	grep -qF "$INSTALL_CMD" "$f" ||
		err "$f never teaches the install command [$INSTALL_CMD]"
	grep -qF "$WIN_INSTALL_CMD" "$f" ||
		err "$f never teaches the Windows install command [$WIN_INSTALL_CMD]"
done

# 3. Commands the page names must exist. Scoped to command contexts -- copy
#    buttons and <code> spans -- because prose says things like "the seamlessd
#    binary", and the second word of a sentence is not a subcommand.
known=$(sed -n '/switch cmd {/,/^	default:/p' "$MAIN" |
	grep -oE '"[a-z][a-z-]*"' | tr -d '"' | sort -u)
named=$({
	grep -oE 'data-copy="[^"]*"' "$PAGE" | sed 's/^data-copy="//; s/"$//'
	grep -oE '<code[^>]*>[^<]*</code>' "$PAGE" | sed 's/<code[^>]*>//; s|</code>||'
} | grep -E '^seamlessd ' | awk '{print $2}' | sort -u)
for c in $named; do
	printf '%s\n' "$known" | grep -qx -- "$c" ||
		err "$PAGE names [seamlessd $c], which $MAIN does not dispatch"
done

# 4. A copy button reads its data-copy attribute, never the text beside it, so
#    the two drift silently and independently: the page shows one command and
#    the clipboard gets another. Nobody proof-reads an attribute.
drift=$(grep -oE 'data-copy="[^"]*">[^<]*<' "$PAGE" |
	sed 's/^data-copy="//; s/<$//' |
	awk -F'">' '$1 != $2 { print "  shows [" $2 "] but copies [" $1 "]" }')
if [ -n "$drift" ]; then
	err "copy buttons do not copy what they show:"
	printf '%s\n' "$drift" >&2
fi

[ "$fail" -eq 0 ] || exit 1
echo "site-check: $PAGE agrees with the installer and the CLI"
