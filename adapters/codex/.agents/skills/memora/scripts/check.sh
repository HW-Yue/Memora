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
  printf '{"status":"ready","version":%s}\n' "$version"
  exit 0
fi

printf '{"status":"unhealthy","install_url":"%s"}\n' "$install_url"
