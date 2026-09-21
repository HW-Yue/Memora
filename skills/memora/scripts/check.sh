#!/bin/sh
set -eu

# 发布链路尚未移植到 SQLite 基座（.github/workflows/release.yml 是拒绝发布的桩），
# 所以这里指向**源码安装**，而不是一个没有产物的 Release 页。等发布链路落地后，
# 这个入口要跟着换成真实 Release（否则用户会被指向 404）。
install_url="https://github.com/HW-Yue/Memora#install"

if ! command -v memora >/dev/null 2>&1; then
  printf '{"status":"missing","install_url":"%s","default_binary":"~/.local/bin/memora"}\n' "$install_url"
  exit 0
fi

if version=$(memora version --json 2>/dev/null); then
  # The CLI's own version says nothing about the daemon serving the instance, and
  # one instance has exactly one daemon — so ask it too. A daemon that cannot
  # answer is not agreement, it is an unknown, which is why the comparison is a
  # refusal to say "ready" rather than a warning.
  instance=$(memora daemon status --json 2>/dev/null || true)
  state=ready
  case "$instance" in
    *'"skewed":true'*) state=skewed ;;
    "") state=unknown ;;
  esac
  if [ -z "$instance" ]; then
    printf '{"status":"%s","version":%s,"install_url":"%s"}\n' "$state" "$version" "$install_url"
  else
    printf '{"status":"%s","version":%s,"instance":%s}\n' "$state" "$version" "$instance"
  fi
  exit 0
fi

printf '{"status":"unhealthy","install_url":"%s"}\n' "$install_url"
