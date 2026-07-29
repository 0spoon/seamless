#!/bin/sh
# Entrypoint for the Glama directory image: bring seamlessd up on container
# loopback, then hand stdio to the MCP bridge. Glama's runner wraps this in its
# own `mcp-proxy -- <cmd>`, which speaks stdio to its child, so the last thing
# this script does must own stdin and stdout.
#
# Two invariants:
#
#   - stdout is the MCP channel. Only bridged JSON-RPC frames may reach it, so
#     the daemon's stdout is redirected to stderr (it logs to stderr already,
#     but a single stray Println would corrupt the stream for the client).
#   - no config file exists in the image, so the key arrives as env. That also
#     keeps config.EnsureAPIKey's first-run generation out of the picture: it
#     only generates when the key is empty AND no config file was found.
set -eu

: "${SEAMLESS_ADDR:=127.0.0.1:8081}"
: "${SEAMLESS_DATA_DIR:=/data}"
export SEAMLESS_ADDR SEAMLESS_DATA_DIR

# Glama's generated image has no /data and no VOLUME, so create it here rather
# than depending on an image the form does not let us write.
mkdir -p "$SEAMLESS_DATA_DIR"

# The key authenticates the bridge to a daemon reachable only from inside this
# container, so a fresh random value per start beats any baked-in constant.
if [ -z "${SEAMLESS_MCP_API_KEY:-}" ]; then
    SEAMLESS_MCP_API_KEY="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
fi
export SEAMLESS_MCP_API_KEY

seamlessd serve >&2 &
daemon=$!

# Ready means "answers an authenticated request", which is what the bridge
# needs. A TCP probe would pass while the database and file watcher are still
# coming up, and the client's initialize would race them.
attempt=0
until seam status >/dev/null 2>&1; do
    if ! kill -0 "$daemon" 2>/dev/null; then
        echo "seamless: daemon exited during startup" >&2
        exit 1
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 150 ]; then
        echo "seamless: daemon did not become ready on ${SEAMLESS_ADDR}" >&2
        exit 1
    fi
    sleep 0.1
done

exec seam mcp-proxy
