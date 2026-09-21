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
stages=(format vet lint unit race cgo-build)
# Optional SQLite modules are compiled in through build tags. sqlite_fts5 is the
# keyword index the recall path reads; a build without it produces a binary where
# recall silently has no index, so the tag is threaded through every stage
# rather than only the ones that link the database.
build_tags=sqlite_fts5
# The platforms this project ships on. vet and lint sweep each one so that a
# file behind a //go:build tag cannot hide from either.
supported_platforms=(linux darwin)

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
      unformatted=$($gofmt_command -l cmd internal)
      if [[ -n "$unformatted" ]]; then
        printf 'unformatted Go files:\n%s\n' "$unformatted" >&2
        return 1
      fi
      # sqlite-vec's translation unit includes a sqlite3.h that has to match the
      # SQLite library this binary links, so it is generated from the driver
      # instead of taken from the host. Re-running the generator proves the
      # checked-in copy still matches: a driver bump that skipped it would
      # otherwise leave the two describing different libraries.
      "$go_command" generate ./internal/sqlstore/vecext/
      if ! git diff --exit-code -- internal/sqlstore/vecext/include/sqlite3.h >/dev/null; then
        printf 'internal/sqlstore/vecext/include/sqlite3.h is stale; run go generate ./internal/sqlstore/vecext/\n' >&2
        return 1
      fi
      ;;
    vet)
      # Once per supported platform, not once for whichever machine happens to
      # run the stage. A file behind //go:build darwin is invisible to a Linux
      # sweep, so a host-only vet reports a clean tree while the macOS job
      # fails on code the sweep never compiled.
      #
      # CGO_ENABLED=0 is set here rather than inherited: Go turns cgo off by
      # itself for a cross-compile only while the variable is unset, so an
      # exported CGO_ENABLED=1 makes the linux sweep build the linux cgo
      # runtime with the host compiler and fail. Each stage owns its cgo
      # decision — the other half is in unit, race and cgo-build.
      for goos in "${supported_platforms[@]}"; do
        printf 'ci: vet GOOS=%s\n' "$goos"
        CGO_ENABLED=0 GOOS="$goos" "$go_command" vet -tags "$build_tags" ./...
      done
      ;;
    lint)
      # Three checkers, pinned by version so a release of one cannot turn a
      # green branch red on its own. There is no baseline or exemption file:
      # every finding was fixed when the stage was introduced, so anything this
      # reports is new.
      #
      # The toolchain is pinned to the version go.mod targets, so a checker
      # that needs a newer one fails here the same way it fails under the
      # GOTOOLCHAIN=local that actions/setup-go puts in CI's environment.
      # Without this, `go run` on a developer machine quietly downloads
      # whatever newer toolchain a tool asks for and the stage passes locally
      # while failing in CI, which is how this stage shipped broken once.
      #
      # It is set per command and deliberately not exported: every stage runs
      # in this one shell, so an export reaches `go test` too, and overriding
      # CI's GOTOOLCHAIN=local there let a test's subprocess download a
      # toolchain and take a path it must never take. That is how this pin
      # shipped broken the second time.
      lint_toolchain="go$("$go_command" list -m -f '{{.GoVersion}}')"
      #
      # staticcheck's style group is off. ST1005 in particular wants
      # lower-case error strings, and this product's errors are user-facing
      # text naming domain objects — "Row", "Tree", "Page index generation".
      # That is a deliberate departure from the Go convention, not an oversight.
      #
      # The three tools are installed once, for the machine running the stage,
      # and then invoked once per supported platform. GOOS has to reach the
      # analysis without reaching the build of the tool itself: `go run` under
      # GOOS=darwin cross-compiles the checker and the stage dies with "exec
      # format error", while the installed binaries read GOOS from the
      # environment when they load the packages to analyse.
      #
      # A subshell, so the temp directory is removed on the way out whether the
      # stage passes or fails. A RETURN trap would outlive this function — traps
      # are shell-wide — and an EXIT trap would be overwritten by cgo-build's.
      (
      lint_tool_dir=$(mktemp -d)
      trap 'rm -rf -- "$lint_tool_dir"' EXIT
      GOBIN="$lint_tool_dir" GOTOOLCHAIN="$lint_toolchain" "$go_command" install \
        honnef.co/go/tools/cmd/staticcheck@"$staticcheck_version"
      GOBIN="$lint_tool_dir" GOTOOLCHAIN="$lint_toolchain" "$go_command" install \
        github.com/kisielk/errcheck@"$errcheck_version"
      GOBIN="$lint_tool_dir" GOTOOLCHAIN="$lint_toolchain" "$go_command" install \
        github.com/gordonklaus/ineffassign@"$ineffassign_version"
      # Once per supported platform, not once for whichever machine happens to
      # run the stage: a file behind //go:build darwin is invisible to a Linux
      # sweep, and that is how two _darwin.go findings reached CI after a local
      # run reported the tree clean.
      for goos in "${supported_platforms[@]}"; do
        printf 'ci: lint GOOS=%s\n' "$goos"
        # CGO_ENABLED=0 for the same reason as the vet sweep: these read the
        # packages of another platform and must not inherit a cgo setting that
        # only makes sense on this one.
        CGO_ENABLED=0 GOOS="$goos" GOTOOLCHAIN="$lint_toolchain" "$lint_tool_dir/staticcheck" \
          -tags "$build_tags" -checks 'all,-ST1000,-ST1003,-ST1005,-ST1020,-ST1021,-ST1022' ./...
        # Tests are excluded from errcheck: a test that ignores an error fails
        # loudly on the next assertion anyway, and t.Cleanup closures would
        # otherwise need wrapping for no gain.
        CGO_ENABLED=0 GOOS="$goos" GOTOOLCHAIN="$lint_toolchain" "$lint_tool_dir/errcheck" -tags "$build_tags" -ignoretests ./...
        CGO_ENABLED=0 GOOS="$goos" GOTOOLCHAIN="$lint_toolchain" "$lint_tool_dir/ineffassign" ./...
      done
      )
      ;;
    unit)
      # CGO_ENABLED=1 rather than inherited: these stages open real database
      # files, and go-sqlite3 with CGO_ENABLED=0 links a mock that cannot.
      CGO_ENABLED=1 "$go_command" test -tags "$build_tags" ./...
      ;;
    race)
      CGO_ENABLED=1 "$go_command" test -tags "$build_tags" -race ./...
      ;;
    cgo-build)
      # go-sqlite3 with CGO_ENABLED=0 links static_mock.go and cannot open a
      # database. Cross-compiling sqlite3 needs a C cross-compiler we do not
      # ship, so this stage builds the host triple and actually opens a file.
      if [[ "${CGO_ENABLED:-1}" == "0" ]]; then
        printf 'ci: cgo-build refuses CGO_ENABLED=0; go-sqlite3 would link a mock that cannot open a database\n' >&2
        return 1
      fi
      (
      work=$(mktemp -d)
      trap 'rm -rf -- "$work"' EXIT
      export CGO_ENABLED=1
      export CGO_CFLAGS="${CGO_CFLAGS:--Wno-deprecated-declarations}"
      "$go_command" build -tags "$build_tags" -trimpath -o "$work/memora" ./cmd/memora
      data="$work/instance"
      "$work/memora" init --data-dir "$data"
      "$work/memora" daemon start --data-dir "$data"
      "$work/memora" doctor --data-dir "$data"
      "$work/memora" daemon stop --data-dir "$data"
      )
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
