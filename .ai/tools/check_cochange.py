#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Co-change tripwire — pointers move when their facts move (#75).

The co-change rule (session-bootstrap) says the PR that changes an
underlying fact updates the corresponding pointer in the same PR. This
tool enforces the index half mechanically: when a file-anchored path
registered in .ai/index.yaml is added, modified, renamed, or deleted in
a commit range, .ai/index.yaml must appear in the same range.

Runs in CI against the PR base; locally it needs a base ref and exits 0
with a note when none is given.

Usage: check_cochange.py [--root DIR] [--base REF]
Exit 0 when consistent (or no base), 1 on tripwire hits.
"""

import argparse
import subprocess
import sys
from pathlib import Path

INDEX = ".ai/index.yaml"


def index_file_anchors(root: Path) -> set[str]:
    """file-anchored (non-directory) paths registered in the index"""
    anchors: set[str] = set()
    for section in ("read_order", "read_on_demand"):
        lines = (root / INDEX).read_text(encoding="utf-8").splitlines()
        in_section = False
        for line in lines:
            if line.startswith(f"{section}:"):
                in_section = True
                continue
            if in_section:
                stripped = line.strip()
                if stripped.startswith("- ") and not stripped.endswith("/"):
                    anchors.add(stripped[2:].strip())
                elif stripped and ":" in stripped:
                    _, _, value = stripped.partition(":")
                    value = value.strip()
                    if value and not value.endswith("/") and \
                            not value.startswith("http"):
                        anchors.add(value)
                elif stripped and not line.startswith(" "):
                    in_section = False
    return anchors


def changed_paths(root: Path, base: str) -> set[str]:
    out = subprocess.run(
        ["git", "diff", "--name-only", f"{base}..HEAD"],
        capture_output=True, text=True, cwd=root, timeout=60)
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip())
    return {line.strip() for line in out.stdout.splitlines() if line.strip()}


def check(root: Path, base: str) -> list[str]:
    anchors = index_file_anchors(root)
    changed = changed_paths(root, base)
    moved = {a for a in anchors
             if a in changed or any(c.startswith(a.rstrip("/") + "/")
                                    for c in changed)}
    problems = []
    if moved and INDEX not in changed:
        problems.append(
            f"{INDEX} unchanged while its anchors move in the same range: "
            + ", ".join(sorted(moved))
            + " — update the pointers in the same PR (co-change rule)")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("--base", default=None,
                        help="git ref to diff against (required to enforce)")
    args = parser.parse_args(argv)
    if not args.base:
        print("co-change: no --base given, skipped (CI passes the base)")
        return 0
    try:
        problems = check(Path(args.root), args.base)
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    for problem in problems:
        print(f"ERROR: {problem}", file=sys.stderr)
    if problems:
        return 1
    print("co-change: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
