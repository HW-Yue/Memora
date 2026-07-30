#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "$script_dir/.." && pwd)
cd "$repository_root"

publication=${1:-}
version=${2:-}
arch=${3:-$(uname -m)}
output=${4:-}

if [[ -z "$publication" || -z "$version" || -z "$output" || $# -gt 4 ]]; then
  printf 'usage: scripts/clean-machine-acceptance.sh PUBLICATION VERSION ARCH NEW_OUTPUT_DIRECTORY\n' >&2
  exit 2
fi
case "$publication" in /*) ;; *) printf 'clean-machine acceptance: publication must be absolute\n' >&2; exit 2 ;; esac
case "$output" in /*) ;; *) printf 'clean-machine acceptance: output must be absolute\n' >&2; exit 2 ;; esac

go run ./cmd/run-clean-machine-acceptance \
  --publication "$publication" \
  --version "${version#v}" \
  --arch "$arch" \
  --output "$output"
