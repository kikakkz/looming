#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Skill validator — enforces the .ai/skills/ contract (.ai/AGENTS.md).

Every self-authored skill is one directory per skill containing a
SKILL.md that follows the Agent Skills open standard: YAML frontmatter
with required `name` and `description`. This check exists because the
`.ai/` rule requires every addition to arrive with its CI in the same
PR (#39 review finding).

Checks per skill:
  - SKILL.md exists and starts with a frontmatter block
  - frontmatter carries non-empty `name` and `description`
  - `name` equals the directory name (discovery is name-based)
  - a body follows the frontmatter

Usage: check_skills.py [--root DIR]   (default: .ai/skills)
Exit 0 when every skill passes, 1 otherwise.
"""

import argparse
import sys
from pathlib import Path

REQUIRED = ("name", "description")


def parse_frontmatter(text: str) -> dict[str, str] | None:
    """minimal frontmatter: key: value lines between --- fences."""
    if not text.startswith("---\n"):
        return None
    try:
        block = text.split("---\n", 2)[1]
    except IndexError:
        return None
    data: dict[str, str] = {}
    for line in block.splitlines():
        if not line.strip() or line.strip().startswith("#"):
            continue
        key, sep, value = line.partition(":")
        if sep:
            data[key.strip()] = value.strip().strip('"').strip("'")
    return data


def check_skill(skill_md: Path) -> list[str]:
    problems: list[str] = []
    rel = skill_md.parent.name
    text = skill_md.read_text(encoding="utf-8")
    meta = parse_frontmatter(text)
    if meta is None:
        return [f"{rel}: missing or malformed YAML frontmatter"]
    for key in REQUIRED:
        if not meta.get(key):
            problems.append(f"{rel}: frontmatter lacks a non-empty {key!r}")
    if meta.get("name") and meta["name"] != rel:
        problems.append(
            f"{rel}: name {meta['name']!r} does not match the directory")
    body = text.split("---\n", 2)[2] if text.count("---\n") >= 2 else ""
    if not body.strip():
        problems.append(f"{rel}: no body after the frontmatter")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".ai/skills",
                        help="skills directory (default: .ai/skills)")
    args = parser.parse_args(argv)
    root = Path(args.root)
    if not root.is_dir():
        print(f"ERROR: {root} is not a directory", file=sys.stderr)
        return 1
    skills = sorted(p for p in root.iterdir() if p.is_dir())
    if not skills:
        print(f"ERROR: no skills under {root}", file=sys.stderr)
        return 1
    problems: list[str] = []
    for skill in skills:
        skill_md = skill / "SKILL.md"
        if not skill_md.exists():
            problems.append(f"{skill.name}: missing SKILL.md")
            continue
        problems.extend(check_skill(skill_md))
    if problems:
        for problem in problems:
            print(f"ERROR: {problem}", file=sys.stderr)
        return 1
    print(f"skills: OK ({len(skills)} skill(s) under {root})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
