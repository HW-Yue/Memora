#!/bin/sh
set -eu

release_dir=${1:-}
version=${2:-}
version=${version#v}
target_arch=${3:-$(uname -m)}

fail() {
  printf 'release smoke: %s\n' "$1" >&2
  exit 1
}

[ -n "$release_dir" ] && [ -n "$version" ] && [ "$#" -le 3 ] ||
  fail "usage: scripts/smoke-release.sh RELEASE_DIRECTORY VERSION [ARCH]"
case "$target_arch" in
  arm64|aarch64) target_arch=arm64 ;;
  amd64|x86_64) target_arch=amd64 ;;
  *) fail "unsupported architecture $target_arch" ;;
esac
case "$release_dir" in /*) ;; *) fail "release directory must be absolute" ;; esac

asset="memora_${version}_darwin_${target_arch}.tar.gz"
archive="$release_dir/$asset"
checksums="$release_dir/checksums.txt"
[ -f "$archive" ] && [ -f "$checksums" ] || fail "release files are incomplete"
expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$checksums")
[ -n "$expected" ] || fail "checksum manifest does not contain $asset"
actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
[ "$actual" = "$expected" ] || fail "checksum verification failed for $asset"
listing=$(tar -tzf "$archive" | LC_ALL=C sort)
expected_listing=$(printf 'COMMERCIAL-LICENSE.md\nLICENSE\nREADME.md\nmemora')
[ "$listing" = "$expected_listing" ] || fail "release archive has an unexpected layout"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/memora-release-smoke.XXXXXX")
binary="$work_dir/memora"
data_dir="$work_dir/data"
running=false
cleanup() {
  if [ "$running" = true ]; then
    "$binary" daemon stop --data-dir "$data_dir" >/dev/null 2>&1 || true
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT HUP INT TERM
tar -xzf "$archive" -C "$work_dir" memora
chmod 755 "$binary"

if ! metadata=$("$binary" version --json 2>/dev/null); then
  if command -v spctl >/dev/null 2>&1 &&
     ! spctl --assess --type execute "$binary" >/dev/null 2>&1; then
    fail "macOS Gatekeeper blocked the verified binary; inspect Privacy & Security"
  fi
  fail "release binary could not start"
fi
printf '%s' "$metadata" | grep -F '"version":"'"$version"'"' >/dev/null 2>&1 ||
  fail "release binary version does not match $version"
"$binary" init --data-dir "$data_dir" >/dev/null
"$binary" daemon start --data-dir "$data_dir" >/dev/null
running=true
"$binary" daemon ping --data-dir "$data_dir" >/dev/null
"$binary" doctor --data-dir "$data_dir" >/dev/null
"$binary" daemon stop --data-dir "$data_dir" >/dev/null
running=false
printf 'Memora %s darwin/%s release smoke passed\n' "$version" "$target_arch"
