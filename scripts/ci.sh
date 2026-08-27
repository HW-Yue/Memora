#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_dir/.." && pwd)
cd "$repository_root"

go_command=${MEMORA_CI_GO:-go}
gofmt_command=${MEMORA_CI_GOFMT:-gofmt}
staticcheck_version=v0.7.0
errcheck_version=v1.20.0
ineffassign_version=v0.1.0
stages=(format vet lint unit race integration e2e cross-build)

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
    lint)
      # Three checkers, pinned by version so a release of one cannot turn a
      # green branch red on its own. There is no baseline or exemption file:
      # every finding was fixed when the stage was introduced, so anything this
      # reports is new.
      #
      # The toolchain is pinned to the version go.mod targets, and pinned the
      # same way here as the workflow pins it (GOTOOLCHAIN: local). Without
      # this, `go run` on a developer machine quietly downloads whatever newer
      # toolchain a tool asks for and the stage passes locally while failing in
      # CI, which is exactly how this stage shipped broken the first time.
      module_go=$("$go_command" list -m -f '{{.GoVersion}}')
      export GOTOOLCHAIN="go${module_go}"
      #
      # staticcheck's style group is off. ST1005 in particular wants
      # lower-case error strings, and this product's errors are user-facing
      # text naming domain objects — "Row", "Tree", "Page index generation".
      # That is a deliberate departure from the Go convention, not an oversight.
      "$go_command" run honnef.co/go/tools/cmd/staticcheck@"$staticcheck_version" \
        -checks 'all,-ST1000,-ST1003,-ST1005,-ST1020,-ST1021,-ST1022' ./...
      # Tests are excluded from errcheck: a test that ignores an error fails
      # loudly on the next assertion anyway, and t.Cleanup closures would
      # otherwise need wrapping for no gain.
      "$go_command" run github.com/kisielk/errcheck@"$errcheck_version" -ignoretests ./...
      "$go_command" run github.com/gordonklaus/ineffassign@"$ineffassign_version" ./...
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
