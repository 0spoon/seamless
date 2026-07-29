# syntax=docker/dockerfile:1

# The image the Glama MCP directory builds and runs to introspect Seamless
# (https://glama.ai/mcp/servers/0spoon/seamless). Glama needs a container that
# starts and answers introspection before it will assign a quality score; that
# score is a listing requirement for punkpeye/awesome-mcp-servers.
#
# This is NOT how Seamless is meant to run. Seamless is a local-first daemon
# that lives next to your agent and owns ~/.seamless; here the data dir is an
# empty throwaway volume that dies with the container.
#
# Glama's runner speaks stdio, so the image starts the daemon on container
# loopback and hands stdio to `seam mcp-proxy` -- the same bridge Codex and
# Claude Desktop use. Build context is the repository root:
#
#   docker build -f deploy/glama/Dockerfile -t seamless-mcp .
#
# This file is pasted verbatim into Glama's Dockerfile admin page, so it is
# deliberately self-contained: the entrypoint is written inline rather than
# COPYd, and the only thing it needs from the build context is the Go source
# tree that is already on GitHub. Nothing here may depend on a file that exists
# only in a working tree.

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# CGO stays off, as everywhere else in this project (modernc.org/sqlite is pure
# Go), so both binaries are static and the runtime stage needs no toolchain.
#
# The build context is a source tree, not a git clone, so the Makefile's
# `git describe` stamp cannot be reused. Fall back to the version in
# server.json -- the registry manifest that already moves with each release --
# rather than a constant that would report a stale version in the directory.
ARG VERSION=
RUN VERSION="${VERSION:-$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' server.json | head -1)}" \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=${VERSION}" -o /out/seamlessd ./cmd/seamlessd \
 && CGO_ENABLED=0 go build -trimpath -o /out/seam ./cmd/seam

FROM alpine:3.22

RUN adduser -D -u 10001 seamless \
 && mkdir -p /data \
 && chown seamless:seamless /data

COPY --from=build /out/seamlessd /usr/local/bin/seamlessd
COPY --from=build /out/seam /usr/local/bin/seam

# Bring seamlessd up on container loopback, then hand stdio to the MCP bridge.
# Two invariants:
#
#   - stdout is the MCP channel. Only bridged JSON-RPC frames may reach it, so
#     the daemon's stdout is redirected to stderr (it logs to stderr already,
#     but a single stray Println would corrupt the stream for the client).
#   - no config file exists in the image, so the key arrives as env. That also
#     keeps config.EnsureAPIKey's first-run generation out of the picture: it
#     only generates when the key is empty AND no config file was found.
RUN <<'ENTRYPOINT' cat > /usr/local/bin/seamless-mcp && chmod +x /usr/local/bin/seamless-mcp
#!/bin/sh
set -eu

: "${SEAMLESS_ADDR:=127.0.0.1:8081}"
: "${SEAMLESS_DATA_DIR:=/data}"
export SEAMLESS_ADDR SEAMLESS_DATA_DIR

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
ENTRYPOINT

USER seamless
WORKDIR /data

# No config file is shipped: config.Load's search order ends at ./seamless.yaml,
# and an empty /data keeps it there, so every setting below arrives as env
# (env > file > defaults) and nothing is written outside the volume.
ENV HOME=/home/seamless \
    SEAMLESS_ADDR=127.0.0.1:8081 \
    SEAMLESS_DATA_DIR=/data

VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/seamless-mcp"]
