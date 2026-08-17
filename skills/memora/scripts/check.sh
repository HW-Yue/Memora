#!/bin/sh
set -eu

# 不固定版本：安装器默认取最新 Release，这里给出的入口必须跟着走，
# 否则用户会被指向一个早已过时的 tag。
release_url="https://github.com/HW-Yue/Memora/releases/latest"

if ! command -v memora >/dev/null 2>&1; then
  printf '{"status":"missing","release_url":"%s","default_binary":"~/.local/bin/memora"}\n' "$release_url"
  exit 0
fi

if version=$(memora version --json 2>/dev/null); then
  printf '{"status":"ready","version":%s}\n' "$version"
  exit 0
fi

printf '{"status":"unhealthy","release_url":"%s"}\n' "$release_url"
