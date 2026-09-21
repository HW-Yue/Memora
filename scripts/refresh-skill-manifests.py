#!/usr/bin/env python3
"""Refresh the adapter manifests from the files on disk.

The manifests record a digest per file plus three named digests, and the gate
fails when any of them is stale. Doing it by hand means remembering which three,
which is how a copy ends up synced without its digest — so this runs as part of
`sync-skill.sh --repo` instead of being a step someone has to recall.

Usage: refresh-skill-manifests.py <repository root>
"""

import hashlib
import json
import pathlib
import sys

# Every file the Skill ships. A new file belongs here and in sync-skill.sh.
CANONICAL = [
    "SKILL.md",
    "contract.json",
    "host-contract.json",
    "references/product-manual.md",
    "agents/openai.yaml",
    "scripts/install.sh",
    "scripts/check.sh",
    "scripts/jev_select.py",
]

ADAPTERS = [
    ("adapters/codex", ".agents/skills/memora/"),
    ("adapters/claude-code", ".claude/skills/memora/"),
]


def digest_of(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def refresh(root, adapter, prefix):
    manifest_path = root / adapter / "manifest.json"
    manifest = json.loads(manifest_path.read_text())

    def digest(relative):
        return digest_of(root / adapter / relative)

    manifest["canonical_digest"] = digest(prefix + "SKILL.md")
    manifest["protocol_digest"] = digest(prefix + "contract.json")
    manifest["task_contract_digest"] = digest(prefix + "host-contract.json")

    for relative in CANONICAL:
        path = prefix + relative
        target = root / adapter / path
        if not target.exists():
            continue
        mode = "0755" if relative.endswith((".sh", ".py")) else "0644"
        entry = next((item for item in manifest["files"] if item["path"] == path), None)
        if entry is None:
            manifest["files"].append({"path": path, "sha256": digest_of(target), "mode": mode})
        else:
            entry["sha256"] = digest_of(target)
    manifest["files"].sort(key=lambda item: item["path"])
    manifest_path.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n")


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: refresh-skill-manifests.py <repository root>")
    root = pathlib.Path(sys.argv[1])
    for adapter, prefix in ADAPTERS:
        refresh(root, adapter, prefix)


if __name__ == "__main__":
    main()
