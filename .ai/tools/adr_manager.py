#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""ADR manager — one-file-per-decision records with bidirectional supersede.

Adopts the mechanics of adr-tools (npryce) and the KEP `replaces:` /
`superseded-by` convention in a minimal stdlib tool, using the repository's
frontmatter format with dual timestamps (adopted-at / superseded-at).

Files: .ai/memory/decisions/NNNN-slug.md, index at decisions/README.md
(generated — never edit by hand).

Commands:
  new --title T [--supersedes N[,N...]]   create the next AD; flips targets
  import-legacy PATH [--supersedes-map J] split a legacy single-file log
  index                                   regenerate decisions/README.md
  check                                   validate links and index freshness

Override the decisions directory with ADR_DIR (used by tests).
"""

import argparse
import datetime
import json
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ADR_DIR = Path(os.environ.get("ADR_DIR", ROOT / "memory" / "decisions"))
INDEX = ADR_DIR / "README.md"

STATUSES = ("accepted", "superseded", "deprecated")


# --------------------------------------------------------------------------
# frontmatter (minimal): key: value | key: [int, ...] | key: "string"

def parse_frontmatter(text: str) -> dict:
    if not text.startswith("---\n"):
        raise ValueError("missing frontmatter")
    try:
        block = text.split("---\n", 2)[1]
    except IndexError as exc:
        raise ValueError("malformed frontmatter") from exc
    data: dict = {}
    for line in block.splitlines():
        if not line.strip() or line.strip().startswith("#"):
            continue
        key, _, raw = line.partition(":")
        key = key.strip()
        raw = raw.strip()
        if raw.startswith("[") and raw.endswith("]"):
            inner = raw[1:-1].strip()
            data[key] = [int(x.strip()) for x in inner.split(",") if x.strip()]
        elif raw.startswith('"') and raw.endswith('"'):
            data[key] = raw[1:-1]
        elif re.fullmatch(r"-?\d+", raw):
            data[key] = int(raw)
        else:
            data[key] = raw
    return data


def dump_frontmatter(data: dict) -> str:
    lines = ["---"]
    for key, value in data.items():
        if isinstance(value, list):
            rendered = "[" + ", ".join(str(v) for v in value) + "]"
        elif isinstance(value, int):
            rendered = str(value)
        else:
            rendered = f'"{value}"'
        lines.append(f"{key}: {rendered}")
    lines.append("---")
    return "\n".join(lines) + "\n"


# --------------------------------------------------------------------------
# model

class Adr:
    def __init__(self, path: Path):
        self.path = path
        self.text = path.read_text(encoding="utf-8")
        self.meta = parse_frontmatter(self.text)
        self.number = int(self.meta["number"])
        self.slug = path.stem.split("-", 1)[1]

    @property
    def title(self) -> str:
        return str(self.meta.get("title", ""))

    @property
    def status(self) -> str:
        return str(self.meta.get("status", ""))

    @property
    def supersedes(self) -> list[int]:
        return [int(n) for n in self.meta.get("supersedes", [])]

    @property
    def superseded_by(self) -> int | None:
        value = self.meta.get("superseded-by")
        return int(value) if value is not None else None


def load_all() -> dict[int, Adr]:
    adrs: dict[int, Adr] = {}
    if not ADR_DIR.exists():
        return adrs
    for path in sorted(ADR_DIR.glob("*.md")):
        if path.name == "README.md":
            continue
        adr = Adr(path)
        adrs[adr.number] = adr
    return adrs


def today() -> str:
    return datetime.date.today().isoformat()


def slugify(title: str) -> str:
    words = re.sub(r"[^a-z0-9 ]", " ", title.lower()).split()
    return "-".join(words[:6]) or "decision"


def write_adr(number: int, title: str, status: str, body: str,
              supersedes: list[int], superseded_by: int | None,
              adopted_at: str, superseded_at: str | None) -> Path:
    meta = {
        "number": number,
        "title": title,
        "date": adopted_at,
        "status": status,
        "supersedes": supersedes,
    }
    if superseded_by is not None:
        meta["superseded-by"] = superseded_by
    meta["adopted-at"] = adopted_at
    if superseded_at is not None:
        meta["superseded-at"] = superseded_at
    path = ADR_DIR / f"{number:04d}-{slugify(title)}.md"
    content = dump_frontmatter(meta) + f"\n# AD-{number} — {title}\n\n{body.rstrip()}\n"
    path.write_text(content, encoding="utf-8")
    return path


def flip_to_superseded(target: Adr, by: int) -> None:
    meta = dict(target.meta)
    meta["status"] = "superseded"
    meta["superseded-by"] = by
    meta["superseded-at"] = today()
    body = target.text.split("---\n", 2)[2]
    target.path.write_text(dump_frontmatter(meta) + body, encoding="utf-8")


def render_index(adrs: dict[int, Adr]) -> str:
    active = sorted((a for a in adrs.values() if a.status == "accepted"),
                    key=lambda a: a.number, reverse=True)
    dead = sorted((a for a in adrs.values() if a.status != "accepted"),
                  key=lambda a: a.number, reverse=True)
    out = ["# Architecture Decisions", "",
           "<!-- generated by adr_manager.py index — do not edit by hand -->", "",
           "## Active", ""]
    for a in active:
        out.append(f"- [AD-{a.number} — {a.title}]({a.path.name})")
    out += ["", "## Superseded / Deprecated", ""]
    for a in dead:
        link = f" — superseded by [AD-{a.superseded_by}]({a.superseded_by:04d})" if a.superseded_by else ""
        out.append(f"- [AD-{a.number} — {a.title}]({a.path.name}){link}")
    return "\n".join(out) + "\n"


def regenerate_index() -> None:
    INDEX.write_text(render_index(load_all()), encoding="utf-8")


# --------------------------------------------------------------------------
# commands

def cmd_new(args: argparse.Namespace) -> int:
    adrs = load_all()
    number = max(adrs, default=0) + 1
    supersedes = sorted({int(n) for n in args.supersedes})
    for target in supersedes:
        if target not in adrs:
            print(f"ERROR: cannot supersede missing AD-{target}", file=sys.stderr)
            return 1
    body = ("## Context\n\n(todo)\n\n## Decision\n\n(todo)\n\n"
            "## Consequences\n\n(todo)\n")
    path = write_adr(number, args.title, "accepted", body, supersedes,
                     None, today(), None)
    for target in supersedes:
        flip_to_superseded(adrs[target], number)
    regenerate_index()
    print(f"created {path.name}")
    return 0


def derive_title(first_line: str) -> str:
    line = first_line.strip().rstrip(".")
    if ":" in line and line.index(":") <= 60:
        line = line[:line.index(":")]
    if len(line) > 60:
        line = line[:60].rsplit(" ", 1)[0]
    return line[0].upper() + line[1:]


def cmd_import_legacy(args: argparse.Namespace) -> int:
    adrs = load_all()
    if adrs:
        print("ERROR: decisions directory is not empty; refusing import",
              file=sys.stderr)
        return 1
    source = Path(args.legacy)
    text = source.read_text(encoding="utf-8")
    pattern = re.compile(r"^## AD-(\d+) — (\d{4}-\d{2}-\d{2}) — (\w+)\n",
                         re.MULTILINE)
    matches = list(pattern.finditer(text))
    if not matches:
        print("ERROR: no '## AD-N — date — status' sections found",
              file=sys.stderr)
        return 1
    supersedes_map = {int(k): [int(n) for n in v]
                      for k, v in json.loads(args.supersedes_map or "{}").items()}
    created: dict[int, Adr] = {}
    for i, match in enumerate(matches):
        number = int(match.group(1))
        date = match.group(2)
        start = match.end()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
        body = text[start:end].strip()
        first_line = next((l for l in body.splitlines() if l.strip()), "")
        title = derive_title(first_line)
        path = write_adr(number, title, "accepted", body, [],
                         None, date, None)
        created[number] = Adr(path)
    for number, targets in supersedes_map.items():
        if number not in created:
            print(f"ERROR: supersedes-map target AD-{number} not imported",
                  file=sys.stderr)
            return 1
        for target in targets:
            if target in created:
                created[number].path.write_text(
                    created[number].path.read_text(encoding="utf-8")
                    .replace("supersedes: []", f"supersedes: [{target}]"),
                    encoding="utf-8")
                flip_to_superseded(created[target], number)
    regenerate_index()
    print(f"imported {len(created)} decisions into {ADR_DIR}")
    return 0


def cmd_check(_args: argparse.Namespace) -> int:
    errors: list[str] = []
    adrs = load_all()
    if not adrs:
        errors.append(f"{ADR_DIR}: no AD files found")

    for number, adr in adrs.items():
        if f"{number:04d}" != adr.path.stem.split("-", 1)[0]:
            errors.append(f"{adr.path.name}: filename does not match number {number}")
        if adr.status not in STATUSES:
            errors.append(f"AD-{number}: bad status '{adr.status}'")
        if adr.status == "superseded":
            if adr.superseded_by is None or "superseded-at" not in adr.meta:
                errors.append(f"AD-{number}: superseded without superseded-by/at")
        elif adr.superseded_by is not None:
            errors.append(f"AD-{number}: superseded-by set but status is {adr.status}")
        for target in adr.supersedes:
            other = adrs.get(target)
            if other is None:
                errors.append(f"AD-{number}: supersedes missing AD-{target}")
            else:
                if other.superseded_by != number:
                    errors.append(
                        f"AD-{number}: AD-{target} lacks reciprocal superseded-by")
                if other.status != "superseded":
                    errors.append(f"AD-{target}: not marked superseded by AD-{number}")
        if adr.superseded_by is not None:
            other = adrs.get(adr.superseded_by)
            if other is None:
                errors.append(f"AD-{number}: superseded-by missing AD-{adr.superseded_by}")
            elif number not in other.supersedes:
                errors.append(
                    f"AD-{number}: AD-{adr.superseded_by} lacks reciprocal supersedes")

    expected = render_index(adrs)
    actual = INDEX.read_text(encoding="utf-8") if INDEX.exists() else ""
    if actual != expected:
        errors.append("index stale — run: python3 .ai/tools/adr_manager.py index")

    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    if errors:
        print(f"adr check FAILED ({len(errors)} problem(s))", file=sys.stderr)
        return 1
    print(f"adr check: OK ({len(adrs)} decisions)")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p_new = sub.add_parser("new")
    p_new.add_argument("--title", required=True)
    p_new.add_argument("--supersedes", type=lambda s: [int(x) for x in s.split(",")],
                       default=[])
    p_new.set_defaults(func=cmd_new)

    p_import = sub.add_parser("import-legacy")
    p_import.add_argument("legacy")
    p_import.add_argument("--supersedes-map", default="{}")
    p_import.set_defaults(func=cmd_import_legacy)

    p_index = sub.add_parser("index")
    p_index.set_defaults(func=lambda _a: (regenerate_index(), 0)[1])

    p_check = sub.add_parser("check")
    p_check.set_defaults(func=cmd_check)

    args = parser.parse_args(argv)
    ADR_DIR.mkdir(parents=True, exist_ok=True)
    return int(args.func(args))


if __name__ == "__main__":
    sys.exit(main())
