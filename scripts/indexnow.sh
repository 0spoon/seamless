#!/bin/sh
# indexnow: push the site's URL list to the IndexNow-federated engines (Bing,
# Naver, Seznam, Yandex -- Google does not participate) so a deploy gets
# recrawled on our schedule instead of theirs.
#
# MANUAL BY DESIGN -- run `make indexnow` once, by hand, after a deploy has
# actually reached the live site. Never wire it into CI or a git hook: a ping
# per docs commit is exactly the over-submission the engines deprioritize, and
# a failing HTTP call turning a build red is worse than no ping.
#
# The protocol (indexnow.org): the site commits a key file served at
# https://<host>/<key>.txt; the engines fetch it once to verify the pinger
# controls the host, then accept POSTed URL lists. The key is public by
# construction -- it proves control of the host, it is not a secret. The key
# here and the committed docs/<key>.txt ship as a pair; rotate both together.
#
# --dry-run assembles and prints the payload without touching the network.

set -eu

HOST="thereisnospoon.org"
KEY="719740aa0a4dd7b7966788415eb4cda91b462676ec5acfb605b4a5dbb2b37fed"
KEY_FILE="docs/$KEY.txt"
SITEMAP="docs/sitemap.xml"
ENDPOINT="https://api.indexnow.org/indexnow"

DRY_RUN=
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

[ -f "$KEY_FILE" ] || {
	echo "ERROR: $KEY_FILE missing -- it is committed; if the key rotated, update KEY here and the file together" >&2
	exit 1
}
[ "$(cat "$KEY_FILE")" = "$KEY" ] || {
	echo "ERROR: $KEY_FILE does not contain this script's KEY -- the pair rotated out of step" >&2
	exit 1
}

# The URL list is the committed sitemap. docs-check keeps that current, so
# there is no second list here to drift.
urls=$(sed -n 's|.*<loc>\(.*\)</loc>.*|\1|p' "$SITEMAP")
[ -n "$urls" ] || { echo "ERROR: no <loc> entries in $SITEMAP" >&2; exit 1; }
count=$(printf '%s\n' "$urls" | wc -l | tr -d ' ')

list=$(printf '%s\n' "$urls" | sed 's|.*|"&"|' | paste -s -d, -)
payload=$(printf '{"host":"%s","key":"%s","keyLocation":"https://%s/%s.txt","urlList":[%s]}' \
	"$HOST" "$KEY" "$HOST" "$KEY" "$list")

if [ -n "$DRY_RUN" ]; then
	echo "indexnow: dry run -- $count URLs, payload not sent:"
	printf '%s\n' "$payload"
	exit 0
fi

# The engines validate the key by fetching it from the live host, so a ping
# before the deploy lands would fail their verification. Make the same fetch
# they will, first.
live=$(curl -fsS --max-time 10 "https://$HOST/$KEY.txt" 2>/dev/null) || {
	echo "ERROR: https://$HOST/$KEY.txt is not served yet -- deploy (push) first, then re-run" >&2
	exit 1
}
[ "$live" = "$KEY" ] || {
	echo "ERROR: the live key file does not match this script's KEY" >&2
	exit 1
}

body=$(mktemp)
trap 'rm -f "$body"' EXIT
status=$(printf '%s' "$payload" | curl -sS -o "$body" -w '%{http_code}' \
	-H 'Content-Type: application/json; charset=utf-8' \
	--data-binary @- "$ENDPOINT")
case "$status" in
200 | 202)
	echo "indexnow: accepted ($status) -- $count URLs submitted for $HOST"
	;;
*)
	echo "ERROR: indexnow returned HTTP $status:" >&2
	cat "$body" >&2
	exit 1
	;;
esac
