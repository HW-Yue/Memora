#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_dir/.." && pwd)
cd "$repository_root"

version=${1:-}
output=${2:-"$repository_root/dist"}
if [[ -z "$version" || $# -gt 2 ]]; then
  printf 'usage: scripts/release.sh VERSION [NEW_OUTPUT_DIRECTORY]\n' >&2
  exit 2
fi
if ! git diff --quiet || ! git diff --cached --quiet; then
  printf 'release: tracked worktree changes must be committed before building\n' >&2
  exit 1
fi
commit=$(git rev-parse HEAD)
source_epoch=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct "$commit")}
source_root=$(mktemp -d "${TMPDIR:-/tmp}/memora-release-source.XXXXXX")
cleanup() {
  rm -rf -- "$source_root"
}
trap cleanup EXIT HUP INT TERM
git archive --format=tar "$commit" | tar -xf - -C "$source_root"

go run ./cmd/build-release \
  --repository "$source_root" \
  --output "$output" \
  --version "$version" \
  --commit "$commit" \
  --source-date-epoch "$source_epoch"
