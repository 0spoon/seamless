# A local mirror of the image the Glama MCP directory builds for Seamless
# (https://glama.ai/mcp/servers/0spoon/seamless), so `build.sh` and
# `entrypoint.sh` can be verified before anyone clicks Build over there.
#
# Glama does not consume this file. Its admin page GENERATES a Dockerfile from a
# form -- debian:trixie-slim, node, python, a git clone into /app, then the
# build steps and CMD arguments the form collects. This mirrors that shape (see
# README.md for the exact field values) minus node and python, which Seamless
# does not use.
#
# The one difference that matters: Glama's CMD wraps ours in its own stdio
# proxy, `mcp-proxy -- /usr/local/bin/seamless-mcp`. Here the entrypoint is CMD
# directly, so `docker run -i` speaks the same stdio JSON-RPC that proxy would.
#
#   docker build -f deploy/glama/Dockerfile -t seamless-mcp .
#
# This is NOT how Seamless is meant to run. Seamless is a local-first daemon
# that lives next to your agent and owns ~/.seamless; here the data dir is an
# empty directory that dies with the container.

FROM debian:trixie-slim

# Glama's base layer installs these before the clone; build.sh needs curl for
# the Go toolchain and ca-certificates to verify it.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Glama clones the repo at a pinned commit; the local mirror uses the working
# tree, which is the point -- it tests what is about to be pushed.
COPY . .

RUN sh deploy/glama/build.sh

CMD ["/usr/local/bin/seamless-mcp"]
