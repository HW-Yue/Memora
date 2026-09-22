#!/usr/bin/env bash
# Keep every copy of the Skill the same bytes.
#
# The Skill exists in several places at once: the canonical copy in this
# repository, one per adapter (which the gate checks), and whatever a user's
# machine actually has installed. They drifted once — an installed copy from
# before the archive and recall work was still on disk, so agents could not use
# features that had shipped — and nothing noticed. This script is the one command
# that syncs the whole chain, and `--check` is how drift becomes visible.
#
#   scripts/sync-skill.sh --check                 # report drift, change nothing
#   scripts/sync-skill.sh                         # sync the repository's copies
#   scripts/sync-skill.sh --install ~/.agents/skills/memora
#   scripts/sync-skill.sh --publish ~/Developer/memora-skill/memora
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
canonical=$root/skills/memora
# The files every copy carries. The constants are listed; everything under
# references/ is enumerated, because a new reference that is not copied is a
# pointer into a file the reader does not have.
files=(SKILL.md contract.json host-contract.json agents/openai.yaml scripts/install.sh scripts/check.sh scripts/jev_select.py)
for reference in "$canonical"/references/*.md; do
  [ -e "$reference" ] || continue
  files+=("${reference#"$canonical/"}")
done
adapters=(adapters/codex/.agents/skills/memora adapters/claude-code/.claude/skills/memora)

mode=repo
target=""
case "${1:---repo}" in
  --check) mode=check ;;
  --repo) mode=repo ;;
  --install) mode=install; target=${2:?--install needs a directory} ;;
  --publish) mode=publish; target=${2:?--publish needs the distribution skill directory} ;;
  *) printf 'usage: sync-skill.sh [--check | --repo | --install <dir> | --publish <dir>]\n' >&2; exit 2 ;;
esac

drift=0
report() { drift=$((drift + 1)); printf '  drift: %s\n' "$1"; }

copy_into() {
  local destination=$1
  for file in "${files[@]}"; do
    mkdir -p "$destination/$(dirname "$file")"
    cp "$canonical/$file" "$destination/$file"
  done
  chmod +x "$destination/scripts/"*.sh
}

compare_with() {
  local destination=$1 label=$2
  for file in "${files[@]}"; do
    if [[ ! -e "$destination/$file" ]]; then
      report "$label is missing $file"
    elif ! cmp -s "$canonical/$file" "$destination/$file"; then
      report "$label differs in $file"
    fi
  done
}

# The adapters carry the same files, and the gate checks them, so a mismatch here
# is a repository bug rather than a user's stale install.
case "$mode" in
  check)
    for adapter in "${adapters[@]}"; do compare_with "$root/$adapter" "$adapter"; done
    if [[ -n "$target" ]]; then compare_with "$target" "$target"; fi
    if (( drift > 0 )); then
      printf 'sync-skill: %d difference(s)\n' "$drift" >&2
      exit 1
    fi
    printf 'sync-skill: every copy matches\n'
    ;;
  repo)
    for adapter in "${adapters[@]}"; do copy_into "$root/$adapter"; done
    if command -v python3 >/dev/null 2>&1; then
      python3 "$root/scripts/refresh-skill-manifests.py" "$root"
    else
      printf 'sync-skill: python3 missing, manifests not refreshed\n' >&2
    fi
    printf 'sync-skill: adapters and their manifests updated\n'
    ;;
  install)
    copy_into "$target"
    printf 'sync-skill: installed into %s\n' "$target"
    ;;
  publish)
    copy_into "$target"
    printf 'sync-skill: published into %s; commit that repository separately\n' "$target"
    ;;
esac
