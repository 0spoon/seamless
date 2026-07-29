#!/bin/sh
# Build step for the Glama directory image.
#
# Glama does not accept a Dockerfile: its admin page generates one from a form,
# and the only hooks are a JSON array of build steps and a JSON array of CMD
# arguments. Both are painful places to put shell, so the whole build lives
# here and the form holds one step: `sh deploy/glama/build.sh`.
#
# The generated image is debian:trixie-slim with node, python, and the repo
# cloned into /app -- no Go toolchain -- so this installs one, builds both
# binaries into /usr/local/bin, and installs the entrypoint beside them.
set -eu

# Track go.mod rather than pinning a second copy of the version: `toolchain`
# when present (it is the exact toolchain the project builds with), else the
# `go` directive, which is also a valid release to download.
go_version="$(sed -n 's/^toolchain go//p' go.mod | head -1)"
if [ -z "$go_version" ]; then
    go_version="$(sed -n 's/^go //p' go.mod | head -1)"
fi

# Go names its release tarballs with the same architecture strings dpkg prints
# (amd64, arm64), so this follows whatever Glama builds on.
arch="$(dpkg --print-architecture)"

echo "glama build: go ${go_version} for linux/${arch}" >&2
curl -fsSL "https://go.dev/dl/go${go_version}.linux-${arch}.tar.gz" | tar -C /usr/local -xz
PATH="/usr/local/go/bin:${PATH}"
export PATH

# The clone is a git checkout, but of a single commit with no tags, so the
# Makefile's `git describe` stamp is unavailable. server.json carries the
# release version and already moves with each release, so read it there instead
# of reporting a version the directory would show as stale.
version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' server.json | head -1)"
echo "glama build: seamless ${version}" >&2

# CGO stays off, as everywhere else in this project (modernc.org/sqlite is pure
# Go), so the binaries carry no runtime library dependencies.
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=${version}" -o /usr/local/bin/seamlessd ./cmd/seamlessd
CGO_ENABLED=0 go build -trimpath -o /usr/local/bin/seam ./cmd/seam

install -m 0755 deploy/glama/entrypoint.sh /usr/local/bin/seamless-mcp

# The toolchain, its build cache, and the module cache are build-time only, and
# all three land in the same image layer Glama publishes for users to deploy.
# Dropping them takes the image from ~1GB to roughly the two static binaries.
go clean -cache -modcache
rm -rf /usr/local/go

seamlessd version >&2
