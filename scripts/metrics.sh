#!/bin/sh
# metrics: the search-visibility plan's north-star numbers. The installer pulls
# binaries from GitHub Releases, so release-asset download counts track actual
# installs rather than a proxy for them -- with the standing caveat that the
# same counters also tick for CI runs, mirrors, and scanners. Read the trend as
# the signal and the absolute number as an upper bound.
#
# Read-only: two public-repo GET requests through `gh`.

set -eu

REPO="0spoon/seamless"

command -v gh >/dev/null || { echo "ERROR: gh not found (brew install gh)" >&2; exit 1; }

gh api "repos/$REPO" \
	--jq '"\(.full_name): \(.stargazers_count) stars, \(.forks_count) forks, \(.subscribers_count) watchers"'
echo

# per_page=100 covers years of releases at the current cadence; revisit if the
# release count ever approaches the page size.
gh api "repos/$REPO/releases?per_page=100" --jq '
	(.[] | "\(.tag_name)  (\((.published_at // "")[0:10]))",
	       (.assets[] | "  \(.download_count)\t\(.name)"),
	       "  = \([.assets[].download_count] | add // 0) downloads"),
	"total across all releases: \([.[].assets[].download_count] | add // 0) downloads"'
