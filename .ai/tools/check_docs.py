#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Doc-consistency checker — the enumerable half of anti-rot (#73).

README and AGENTS.md enumerate repository facts by hand (gate composition,
skill counts, command lists). Hand-written enumerations drift silently —
this repo caught three such staleness bugs by human eye in one day
(#67/#69/#71). This check mechanizes the rot classes that are verifiable:

  - every `make <target>` mentioned in the tracked docs exists in the
    Makefile, and every ci-gate prerequisite is named in AGENTS.md
  - skill-count claims ("N skills") match the real .ai/skills/ directory
  - wikilinks in memory files resolve: [[AD-N]] against the decisions
    directory, [[name]] against memory file stems

Semantic rot — a rule that no longer describes practice — has no
mechanical fix; it stays with review discipline.

Usage: check_docs.py [--root DIR]
Exit 0 when consistent, 1 with the list of drift findings.
"""

import argparse
import re
import sys
from pathlib import Path

DOC_GLOBS = ("AGENTS.md", "README.md", ".ai/AGENTS.md")
MAKE_MENTION_RE = re.compile(r"`make ([a-z0-9][a-z0-9-]*)`")
COUNT_CLAIM_RE = re.compile(r"(\d+) (?:self-authored )?skills?", re.I)
WIKILINK_RE = re.compile(r"\[\[([^\]]+)\]\]")
BUDGET_LINES = 200  # always-loaded constitution cap (survey: adherence
                    # degrades and cost inflates past this)
AD_LINK_RE = re.compile(r"^AD-(\d+)$")


def makefile_targets(makefile: Path) -> set[str]:
    targets: set[str] = set()
    for line in makefile.read_text(encoding="utf-8").splitlines():
        if line.startswith("\t") or line.startswith("."):
            continue
        name, sep, _ = line.partition(":")
        if sep and re.fullmatch(r"[a-z0-9][a-z0-9-]*", name.strip()):
            targets.add(name.strip())
    return targets


def ci_gate_prereqs(makefile: Path) -> list[str]:
    for line in makefile.read_text(encoding="utf-8").splitlines():
        if line.startswith("ci-gate:"):
            return [t.strip() for t in line.partition(":")[2].split()
                    if t.strip()]
    return []


def count_claims(root: Path, docs: list[Path]) -> list[str]:
    actual = len([p for p in (root / ".ai" / "skills").iterdir()
                  if p.is_dir()]) if (root / ".ai" / "skills").is_dir() else 0
    problems = []
    for doc in docs:
        for match in COUNT_CLAIM_RE.finditer(doc.read_text(encoding="utf-8")):
            if int(match.group(1)) != actual:
                problems.append(
                    f"{doc}: claims '{match.group(0)}' but .ai/skills/ "
                    f"has {actual}")
    return problems


def wikilinks(root: Path) -> list[str]:
    memory = root / ".ai" / "memory"
    stems = {p.stem for p in memory.glob("*.md")}
    decisions = {int(p.name.split("-")[0]) for p in
                 (memory / "decisions").glob("*.md")
                 if re.match(r"^\d{4}-", p.name)} \
        if (memory / "decisions").is_dir() else set()
    problems = []
    for path in sorted(memory.rglob("*.md")):
        if path.name == "README.md":
            continue
        for target in WIKILINK_RE.findall(path.read_text(encoding="utf-8")):
            ad = AD_LINK_RE.match(target)
            if ad:
                if int(ad.group(1)) not in decisions:
                    problems.append(f"{path}: unresolved AD link [[{target}]]")
            elif target not in stems:
                problems.append(f"{path}: unresolved wikilink [[{target}]]")
    return problems


def size_budget(root: Path) -> list[str]:
    """always-loaded files (index.yaml read_order) stay under the line cap"""
    index = root / ".ai" / "index.yaml"
    problems = []
    if not index.is_file():
        return problems
    in_order, current = [], None
    for line in index.read_text(encoding="utf-8").splitlines():
        if line.startswith("read_order:"):
            current = in_order
            continue
        if current is not None:
            if line.startswith("  - "):
                current.append(line.strip()[2:].strip())
            elif line and not line.startswith(" "):
                current = None
    for ref in in_order:
        path = root / ref
        if path.is_file():
            lines = len(path.read_text(encoding="utf-8").splitlines())
            if lines > BUDGET_LINES:
                problems.append(
                    f"{ref}: {lines} lines exceeds the {BUDGET_LINES}-line "
                    f"budget for always-loaded files")
    return problems


def check(root: Path) -> list[str]:
    problems: list[str] = []
    makefile = root / "Makefile"
    targets = makefile_targets(makefile)
    docs = [root / g for g in DOC_GLOBS if (root / g).is_file()]
    for doc in docs:
        for mention in MAKE_MENTION_RE.findall(doc.read_text(encoding="utf-8")):
            if mention not in targets:
                problems.append(f"{doc}: documents `make {mention}` but no "
                                f"such Makefile target")
    agents_md = (root / "AGENTS.md").read_text(encoding="utf-8")
    documented = set(MAKE_MENTION_RE.findall(agents_md))
    for dep in ci_gate_prereqs(makefile):
        if dep not in documented:
            problems.append(f"AGENTS.md: ci-gate prerequisite '{dep}' "
                            f"lacks a `make {dep}` entry in the docs")
    problems += count_claims(root, docs)
    problems += wikilinks(root)
    problems += size_budget(root)
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".",
                        help="repository root (default: .)")
    args = parser.parse_args(argv)
    problems = check(Path(args.root))
    if problems:
        for problem in problems:
            print(f"ERROR: {problem}", file=sys.stderr)
        return 1
    print("docs: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
