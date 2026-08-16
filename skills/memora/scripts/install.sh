#!/bin/sh
set -eu

version=""
install_dir="${HOME}/.local/bin"
data_dir=""
source_dir=""
target_os=$(uname -s | tr '[:upper:]' '[:lower:]')
target_arch=$(uname -m)
authorized=false
release_base=${MEMORA_RELEASE_BASE:-https://github.com/HW-Yue/Memora/releases/download}

fail() {
  printf 'memora installer: %s\n' "$1" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes) authorized=true; shift ;;
    --version) [ "$#" -ge 2 ] || fail "--version requires a value"; version=${2#v}; shift 2 ;;
    --install-dir) [ "$#" -ge 2 ] || fail "--install-dir requires a path"; install_dir=$2; shift 2 ;;
    --data-dir) [ "$#" -ge 2 ] || fail "--data-dir requires a path"; data_dir=$2; shift 2 ;;
    --source-dir) [ "$#" -ge 2 ] || fail "--source-dir requires a path"; source_dir=$2; shift 2 ;;
    --os) [ "$#" -ge 2 ] || fail "--os requires a value"; target_os=$2; shift 2 ;;
    --arch) [ "$#" -ge 2 ] || fail "--arch requires a value"; target_arch=$2; shift 2 ;;
    *) fail "unknown option $1" ;;
  esac
done

[ "$authorized" = true ] || fail "installation requires explicit user authorization; rerun with --yes after approval"
if [ -z "$version" ]; then
  latest_body=""
  if command -v curl >/dev/null 2>&1; then
    latest_body=$(curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
      "https://api.github.com/repos/HW-Yue/Memora/releases/latest" 2>/dev/null || true)
  fi
  version=$(printf '%s' "$latest_body" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p' |
    head -1)
  [ -n "$version" ] || fail "cannot resolve the latest Memora release from GitHub; reconnect or pass --version to pin one"
  version=${version#v}
fi
[ "$target_os" = darwin ] || fail "v0 bootstrap supports only macOS"
case "$target_arch" in
  arm64|aarch64) target_arch=arm64 ;;
  amd64|x86_64) target_arch=amd64 ;;
  *) fail "unsupported architecture $target_arch" ;;
esac
case "$install_dir" in /*) ;; *) fail "--install-dir must be an absolute path" ;; esac
if [ -n "$data_dir" ]; then
  case "$data_dir" in /*) ;; *) fail "--data-dir must be an absolute path" ;; esac
fi
if [ -n "$source_dir" ]; then
  case "$source_dir" in /*) ;; *) fail "--source-dir must be an absolute path" ;; esac
fi
case "$release_base" in https://*) ;; *) fail "release base must use HTTPS" ;; esac

mkdir -p "$install_dir"
work_dir=$(mktemp -d "$install_dir/.memora-install.XXXXXX")
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT HUP INT TERM
target="$install_dir/memora"

current_version=""
if [ -x "$target" ]; then
  current_version=$($target version --json 2>/dev/null || true)
fi
if printf '%s' "$current_version" | grep -F '"version":"'"$version"'"' >/dev/null 2>&1; then
  printf 'Memora %s is already installed at %s\n' "$version" "$target"
else
  asset="memora_${version}_darwin_${target_arch}.tar.gz"
  archive="$work_dir/$asset"
  checksums="$work_dir/checksums.txt"
  release_url="$release_base/v${version}"
  release_available=true
  if ! command -v curl >/dev/null 2>&1 ||
     ! curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$archive" "$release_url/$asset" ||
     ! curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$checksums" "$release_url/checksums.txt"; then
    release_available=false
  fi

  staged="$work_dir/memora"
  if [ "$release_available" = true ]; then
    expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$checksums")
    [ -n "$expected" ] || fail "release checksum manifest does not contain $asset"
    actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
    [ "$actual" = "$expected" ] || fail "checksum verification failed for $asset"
    listing=$(tar -tzf "$archive" | LC_ALL=C sort)
    expected_listing=$(printf 'COMMERCIAL-LICENSE.md\nLICENSE\nREADME.md\nmemora')
    [ "$listing" = "$expected_listing" ] || fail "release archive has an unexpected layout"
    tar -xzf "$archive" -C "$work_dir" memora
    [ ! -L "$staged" ] && [ -f "$staged" ] && [ -x "$staged" ] ||
      fail "release archive does not contain a regular executable memora"
  else
    command -v go >/dev/null 2>&1 || fail "release unavailable and Go is not installed; reconnect or install Go and retry"
    if [ -n "$source_dir" ]; then
      [ -f "$source_dir/go.mod" ] || fail "source directory does not contain go.mod"
      (
        cd "$source_dir"
        go build -trimpath -ldflags "-X main.version=$version -X main.commit=source -X main.builtAt=source" -o "$staged" ./cmd/memora
      )
    else
      mkdir -p "$work_dir/go-bin"
      GOBIN="$work_dir/go-bin" go install "github.com/HW-Yue/Memora/cmd/memora@v${version}"
      cp "$work_dir/go-bin/memora" "$staged"
    fi
    chmod 755 "$staged"
  fi

  staged_error="$work_dir/staged-version.stderr"
  if ! staged_version=$("$staged" version --json 2>"$staged_error"); then
    if command -v spctl >/dev/null 2>&1 &&
       ! spctl --assess --type execute "$staged" >/dev/null 2>&1; then
      fail "macOS Gatekeeper blocked the verified Memora binary; allow it in System Settings > Privacy & Security, then retry"
    fi
    fail "staged binary could not run; verify macOS compatibility and retry"
  fi
  printf '%s' "$staged_version" | grep -F '"version":"'"$version"'"' >/dev/null 2>&1 || fail "staged binary version does not match $version"
  chmod 755 "$staged"
  mv -f "$staged" "$target"
  if [ "$release_available" = true ]; then
    printf 'installed Memora %s from verified release at %s\n' "$version" "$target"
  else
    printf 'built Memora %s from source at %s\n' "$version" "$target"
  fi
fi

if [ -n "$data_dir" ]; then
  "$target" init --data-dir "$data_dir"
  if ! "$target" daemon ping --data-dir "$data_dir" >/dev/null 2>&1; then
    "$target" daemon start --data-dir "$data_dir" >/dev/null
  fi
  "$target" doctor --data-dir "$data_dir"
else
  "$target" init
  if ! "$target" daemon ping >/dev/null 2>&1; then
    "$target" daemon start >/dev/null
  fi
  "$target" doctor
fi
