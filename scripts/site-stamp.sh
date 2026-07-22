#!/bin/sh
# site-stamp: stamp a content-hash cache-buster onto the landing page's static
# asset references, so a changed site.css/site.js gets a fresh URL instead of
# serving stale behind the CDN edge cache.
#
# Why this exists: thereisnospoon.org is GitHub Pages behind Cloudflare, which
# edge-caches everything under static/ for hours (max-age=14400) while passing
# the HTML through (cf-cache-status DYNAMIC). So a deploy updates the HTML at
# once but keeps serving the pre-deploy site.css/site.js until the TTL lapses --
# which is exactly how the OS-switch shipped rendering broken (unstyled toggle,
# both install commands showing) even though the origin was already correct.
# A per-content ?v=<hash> makes a changed asset a different URL, which the edge
# has never cached, so the fix goes live with the HTML.
#
# Run this after editing docs/static/site.css or site.js, then commit the
# restamped page. site-check enforces that the stamped hash matches the file.

set -eu

# Every local script/stylesheet the hand-written pages load by name. Fonts and
# images are omitted: they change by getting a new filename, not by mutating in
# place.
PAGES="docs/index.html docs/compare/index.html"
DIR=docs/static
ASSETS="site.css site.js scenes.js scenes-player.js webmcp.js"

# First 8 hex of the file's sha256. sha256sum on Linux/CI, shasum on macOS.
sha8() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -c1-8
	else
		shasum -a 256 "$1" | cut -c1-8
	fi
}

for asset in $ASSETS; do
	h=$(sha8 "$DIR/$asset")
	esc=$(printf '%s' "$asset" | sed 's/\./\\./g')
	for page in $PAGES; do
		tmp=$(mktemp)
		# Rewrite static/<asset> or static/<asset>?v=xxxx -> static/<asset>?v=<h>.
		# Matches ../static/ references too: the pattern is unanchored.
		sed -E "s|(static/$esc)(\?v=[0-9a-f]+)?|\1?v=$h|g" "$page" >"$tmp"
		mv "$tmp" "$page"
	done
	echo "site-stamp: static/$asset?v=$h"
done
