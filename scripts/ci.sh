#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_dir/.." && pwd)
cd "$repository_root"

go_command=${MEMORA_CI_GO:-go}
gofmt_command=${MEMORA_CI_GOFMT:-gofmt}
stages=(format vet unit race integration e2e cross-build)

usage() {
  printf 'usage: scripts/ci.sh [--list | --stage <name>]\n' >&2
}

list_stages() {
  printf '%s\n' "${stages[@]}"
}

run_stage() {
  stage=$1
  printf 'ci: %s\n' "$stage"
  case "$stage" in
    format)
      unformatted=$($gofmt_command -l cmd internal tests)
      if [[ -n "$unformatted" ]]; then
        printf 'unformatted Go files:\n%s\n' "$unformatted" >&2
        return 1
      fi
      ;;
    vet)
      "$go_command" vet ./...
      ;;
    unit)
      "$go_command" test ./...
      ;;
    race)
      "$go_command" test -race ./...
      ;;
    integration)
      "$go_command" test -tags=integration ./...
      ;;
    e2e)
      "$go_command" test -tags=e2e ./...
      ;;
    cross-build)
      cross_build_dir=$(mktemp -d)
      trap 'rm -rf -- "$cross_build_dir"' EXIT
      CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$go_command" build -trimpath \
        -o "$cross_build_dir/memora-arm64" ./cmd/memora
      CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 "$go_command" build -trimpath \
        -o "$cross_build_dir/memora-amd64" ./cmd/memora
      ;;
    *)
      printf 'ci: unknown stage %q\n' "$stage" >&2
      return 2
      ;;
  esac
}

case ${1:-} in
  --list)
    [[ $# -eq 1 ]] || { usage; exit 2; }
    list_stages
    ;;
  --stage)
    [[ $# -eq 2 ]] || { usage; exit 2; }
    run_stage "$2"
    ;;
  "")
    for stage in "${stages[@]}"; do
      run_stage "$stage"
    done
    ;;
  *)
    usage
    exit 2
    ;;
esac
