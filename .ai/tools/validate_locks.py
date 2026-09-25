#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Validate .ai lock files and the memory bank.

Checks:
  - .ai/skills.lock.toml / .ai/mcp/servers.lock.toml: schema, unique names,
    40-char revision pins, source host allowlist, license allowlist.
  - .ai/memory/*.md: required files exist, start with frontmatter carrying
    an `updated:` date, and contain a top-level heading.

Stdlib only. Exit 1 on any failure.
"""

import re
import sys
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

ALLOWED_HOSTS = ("github.com",)
ALLOWED_LICENSES = {
    "MIT",
    "Apache-2.0",
    "BSD-2-Clause",
    "BSD-3-Clause",
    "ISC",
    "CC0-1.0",
}
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")

MEMORY_FILES = ("projectbrief.md", "activeContext.md", "progress.md", "decisions.md")


def err(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)


def check_lock(path: Path, table: str) -> int:
    failures = 0
    if not path.exists():
        err(f"{path}: missing")
        return 1

    data = tomllib.loads(path.read_text(encoding="utf-8"))
    entries = data.get(table, [])
    names: set[str] = set()

    for i, entry in enumerate(entries):
        where = f"{path.name}[{table}][{i}]"
        name = entry.get("name", "")
        source = entry.get("source", "")
        revision = entry.get("revision", "")
        license_id = entry.get("license", "")

        if not name:
            err(f"{where}: missing 'name'")
            failures += 1
        elif name in names:
            err(f"{where}: duplicate name '{name}'")
            failures += 1
        else:
            names.add(name)

        if not source or not source.startswith(ALLOWED_HOSTS):
            err(f"{where}: source '{source}' not on allowlist {ALLOWED_HOSTS}")
            failures += 1

        if not SHA_RE.match(revision):
            err(f"{where}: revision must be a full 40-char lowercase sha")
            failures += 1

        if license_id not in ALLOWED_LICENSES:
            err(f"{where}: license '{license_id}' not in allowlist {sorted(ALLOWED_LICENSES)}")
            failures += 1

    print(f"{path.name}: {len(entries)} {table} entries OK")
    return failures


def check_memory() -> int:
    failures = 0
    mem_dir = ROOT / "memory"

    for name in MEMORY_FILES:
        path = mem_dir / name
        if not path.exists():
            err(f".ai/memory/{name}: missing")
            failures += 1
            continue

        text = path.read_text(encoding="utf-8")
        if not text.startswith("---\n"):
            err(f".ai/memory/{name}: must start with YAML frontmatter")
            failures += 1
            continue

        try:
            frontmatter = text.split("---\n", 2)[1]
        except IndexError:
            err(f".ai/memory/{name}: malformed frontmatter")
            failures += 1
            continue

        updated = None
        for line in frontmatter.splitlines():
            if line.startswith("updated:"):
                updated = line.split(":", 1)[1].strip().strip('"').strip("'")
        if not updated or not DATE_RE.match(updated):
            err(f".ai/memory/{name}: frontmatter needs 'updated: YYYY-MM-DD'")
            failures += 1

        if not re.search(r"^# ", text, re.MULTILINE):
            err(f".ai/memory/{name}: missing top-level '# ' heading")
            failures += 1

    if failures == 0:
        print(f"memory bank: {len(MEMORY_FILES)} files OK")
    return failures


def main() -> int:
    total = 0
    total += check_lock(ROOT / "skills.lock.toml", "skill")
    total += check_lock(ROOT / "mcp" / "servers.lock.toml", "server")
    total += check_memory()
    if total:
        print(f"validate-locks: FAILED ({total} problem(s))", file=sys.stderr)
        return 1
    print("validate-locks: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
