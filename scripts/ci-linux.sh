#!/usr/bin/env bash
# Run the cgo half of the gate on Linux, from whatever machine you are on.
#
# The host gate cannot cover this: sqlite-vec is C, and a missing header or a
# Linux-only compile problem only appears when something compiles it there. That
# is exactly how a build that worked on macOS failed on Linux — the vector
# module included a sqlite3.h the host had and the container did not. The ubuntu
# CI job runs the same thing, so this is the local way to see it before a PR.
#
# Needs Docker and network access to fetch the module cache on a cold image.
set -euo pipefail

image=${MEMORA_LINUX_GO_IMAGE:-golang:1.26-bookworm}
tags=${MEMORA_BUILD_TAGS:-sqlite_fts5}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

exec docker run --rm -v "$root":/src -w /src -e CGO_ENABLED=1 \
  "$image" bash -c "
    set -euo pipefail
    go build -tags '$tags' -o /tmp/memora-linux ./cmd/memora
    # Tests that open real database files are the ones that exercise cgo.
    go test -tags '$tags' ./internal/sqlstore/ ./internal/msql/... ./sdk/memora/
  "
