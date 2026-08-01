#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_dir/.." && pwd)
cd "$repository_root"

version=${1:-}
output=${2:-"$repository_root/publication"}
if [[ -z "$version" || $# -gt 2 ]]; then
  printf 'usage: scripts/publication.sh VERSION [NEW_OUTPUT_DIRECTORY]\n' >&2
  exit 2
fi
if [[ -z "${MEMORA_RELEASE_SIGNING_KEY_FILE:-}" || -z "${MEMORA_RELEASE_SIGNER_KEY_ID:-}" ]]; then
  printf 'publication: MEMORA_RELEASE_SIGNING_KEY_FILE and MEMORA_RELEASE_SIGNER_KEY_ID are required\n' >&2
  exit 1
fi
if ! git diff --quiet || ! git diff --cached --quiet; then
  printf 'publication: tracked worktree changes must be committed before building\n' >&2
  exit 1
fi
commit=$(git rev-parse HEAD)
source_epoch=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct "$commit")}
source_root=$(mktemp -d "${TMPDIR:-/tmp}/memora-publication-source.XXXXXX")
cleanup() {
  rm -rf -- "$source_root"
}
trap cleanup EXIT HUP INT TERM
git archive --format=tar "$commit" | tar -xf - -C "$source_root"

go run ./cmd/build-publication \
  --repository "$source_root" \
  --output "$output" \
  --version "$version" \
  --commit "$commit" \
  --source-date-epoch "$source_epoch" \
  --signing-key "$MEMORA_RELEASE_SIGNING_KEY_FILE" \
  --signer-key-id "$MEMORA_RELEASE_SIGNER_KEY_ID"
