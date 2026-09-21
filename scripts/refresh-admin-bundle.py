#!/usr/bin/env python3
"""Regenerate the Admin bundle's frozen asset manifest from the files on disk.

The front end is a frozen bundle: every asset's path, content type, size and
sha256 is written into `internal/adminui/bundle.go`, and the Go side refuses to
serve a file that does not match. Editing any of those files therefore means
regenerating this table — and doing that by hand is how a hash goes stale and the
whole Admin stops loading.

    scripts/refresh-admin-bundle.py            # rewrite the table
    scripts/refresh-admin-bundle.py --check    # report drift, change nothing
"""
import hashlib
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DIST = ROOT / "internal" / "adminui" / "dist"
BUNDLE = ROOT / "internal" / "adminui" / "bundle.go"

CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
}
START = "var frozenAssets = []assetSpec{"
END = "\n}\n"


def asset_path(relative: str) -> str:
    return "/" if relative == "index.html" else "/" + relative


def collect():
    entries = []
    for path in sorted(DIST.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(DIST).as_posix()
        content = path.read_bytes()
        entries.append({
            "file": f"dist/{relative}",
            "path": asset_path(relative),
            "content_type": CONTENT_TYPES.get(path.suffix, "application/octet-stream"),
            "hash": hashlib.sha256(content).hexdigest(),
            "size": len(content),
        })
    return entries


def render(entries):
    lines = [START]
    for index, entry in enumerate(entries):
        lines.append("\t{")
        lines.append(f'\t\tfile: "{entry["file"]}", path: "{entry["path"]}", '
                     f'contentType: "{entry["content_type"]}",')
        lines.append(f'\t\thash: "{entry["hash"]}", size: {entry["size"]},')
        lines.append("\t}," if index + 1 < len(entries) else "\t},")
    lines.append("}")
    return "\n".join(lines) + "\n"


def main():
    check = "--check" in sys.argv
    entries = collect()
    source = BUNDLE.read_text()
    start = source.index(START)
    end = source.index(END, start) + len(END)
    updated = source[:start] + render(entries) + source[end:]
    if check:
        if updated != source:
            print("admin bundle manifest is stale; run scripts/refresh-admin-bundle.py")
            return 1
        print(f"admin bundle manifest matches {len(entries)} assets")
        return 0
    BUNDLE.write_text(updated)
    print(f"admin bundle manifest rewritten with {len(entries)} assets")
    return 0


sys.exit(main())
