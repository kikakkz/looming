#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Index validator — keeps .ai/index.yaml honest (issue #48).

The index is the machine-readable map of the .ai/ control plane; agents
bootstrap from it instead of crawling the directory. This check exists
because wait-agent's equivalent integrity script passed vacuously while
its anchors were dead links: anything this parser cannot understand is
an error here, never silently skipped.

Checks:
  - the index parses under the constrained subset (scalars, lists,
    one-level maps; unknown top-level keys are rejected)
  - every read_order entry and read_on_demand value exists
    (trailing slash = directory)
  - required_rules values that reference a file must have it exist
  - authoritative_sources values are URLs or existing files

Usage: check_index.py [--root DIR] [--index FILE]
Exit 0 when the index and every anchor pass, 1 otherwise.
"""

import argparse
import sys
from pathlib import Path

URL_PREFIXES = ("http://", "https://")


class IndexError_(ValueError):
    pass


def build_model(text: str) -> dict[str, object]:
    """Parse with list/map kind resolution on the first entry."""
    sections: dict[str, object] = {}
    current: str | None = None
    kind: str | None = None
    for lineno, raw in enumerate(text.splitlines(), start=1):
        if not raw.strip() or raw.strip().startswith("#"):
            continue
        if raw.startswith((" ", "\t")):
            if current is None:
                raise IndexError_(
                    f"line {lineno}: entry before any section")
            leading = raw[: len(raw) - len(raw.lstrip())]
            if "\t" in leading or len(leading) != 2:
                raise IndexError_(
                    f"line {lineno}: entries are indented exactly two "
                    f"spaces (got {len(leading)} chars)")
            stripped = raw.strip()
            if kind is None:
                # first entry decides the section kind and container
                kind = "list" if stripped.startswith("- ") else "map"
                sections[current] = [] if kind == "list" else {}
            if stripped.startswith("- "):
                if kind == "map":
                    raise IndexError_(
                        f"line {lineno}: list entry inside map {current!r}")
                value = stripped[2:].strip()
                if not value:
                    raise IndexError_(f"line {lineno}: empty list entry")
                sections[current].append(value)  # type: ignore[union-attr]
            else:
                sub, sep, value = stripped.partition(":")
                if not sep or not sub.strip():
                    raise IndexError_(
                        f"line {lineno}: expected '- item' or 'sub: value'")
                if kind == "list":
                    raise IndexError_(
                        f"line {lineno}: map entry inside list {current!r}")
                value = value.strip()
                if not value:
                    raise IndexError_(f"line {lineno}: empty value")
                sub = sub.strip()
                if sub in sections[current]:
                    raise IndexError_(
                        f"line {lineno}: duplicate key {sub!r} in "
                        f"{current!r}")
                sections[current][sub] = value  # type: ignore[index]
            continue
        key, sep, value = raw.partition(":")
        if not sep or not key.strip():
            raise IndexError_(f"line {lineno}: not a 'key:' line")
        key = key.strip()
        if key in sections:
            raise IndexError_(f"line {lineno}: duplicate section {key!r}")
        value = value.strip()
        if value:
            sections[key] = value
            current, kind = None, None
        else:
            sections[key] = []
            current, kind = key, None  # kind decided by first entry
    return sections


def anchor_exists(root: Path, ref: str) -> bool:
    if ref.startswith(URL_PREFIXES):
        return True
    path = root / ref.rstrip("/")
    return path.is_dir() if ref.endswith("/") else path.is_file()


def check(root: Path, index: Path) -> list[str]:
    problems: list[str] = []
    try:
        model = build_model(index.read_text(encoding="utf-8"))
    except IndexError_ as exc:
        return [f"{index}: {exc}"]
    # strictness lesson from #48: unknown sections are reported, not
    # assumed healthy — a gate that passes what it cannot read is no gate
    known = {"read_order", "read_on_demand", "required_rules",
             "authoritative_sources", "directories"}
    for key in model:
        if key not in known:
            problems.append(f"unknown top-level section: {key}")
    read_order = model.get("read_order")
    if not isinstance(read_order, list) or not read_order:
        problems.append("read_order must be a non-empty list")
    else:
        for ref in read_order:
            if not anchor_exists(root, ref):
                problems.append(f"read_order anchor missing: {ref}")
    for section in ("read_on_demand", "authoritative_sources", "directories"):
        entries = model.get(section)
        if entries is None:
            continue
        if not isinstance(entries, dict):
            problems.append(f"{section} must be a key: value map")
            continue
        for sub, ref in entries.items():
            if not anchor_exists(root, ref):
                problems.append(f"{section}.{sub} anchor missing: {ref}")
    rules = model.get("required_rules")
    if rules is not None and not isinstance(rules, dict):
        problems.append("required_rules must be a key: value map")
    elif isinstance(rules, dict):
        for name, ref in rules.items():
            token = ref.split()[0] if ref.split() else ""
            if (token.endswith(".md") or token.startswith(".")) \
                    and not anchor_exists(root, token):
                problems.append(f"required_rules.{name} anchor missing: {token}")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".",
                        help="repository root anchors resolve against")
    parser.add_argument("--index", default=None,
                        help="index file (default: <root>/.ai/index.yaml)")
    args = parser.parse_args(argv)
    root = Path(args.root)
    index = Path(args.index) if args.index else root / ".ai" / "index.yaml"
    if not index.is_file():
        print(f"ERROR: index not found: {index}", file=sys.stderr)
        return 1
    problems = check(root, index)
    if problems:
        for problem in problems:
            print(f"ERROR: {problem}", file=sys.stderr)
        return 1
    print(f"index: OK ({index})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
