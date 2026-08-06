#!/bin/sh
set -eu

release_url="https://github.com/HW-Yue/Memora/releases/tag/v0.1.0"

if ! command -v memora >/dev/null 2>&1; then
  printf '{"status":"missing","release_url":"%s","default_binary":"~/.local/bin/memora"}\n' "$release_url"
  exit 0
fi

if version=$(memora version --json 2>/dev/null); then
  printf '{"status":"ready","version":%s}\n' "$version"
  exit 0
fi

printf '{"status":"unhealthy","release_url":"%s"}\n' "$release_url"
