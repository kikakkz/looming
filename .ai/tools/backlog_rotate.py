#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Backlog rotator — automates the lot lifecycle from .ai/skills/backlog/.

The backlog skill defines rotation: when the current lot reaches ~10
ungraduated items or ~2 months without a graduation, close it with a
graduated/carried/died summary, open the successor with carried ideas
re-stated, and point the skill's single "Current lot" line at the
successor. This tool performs that rotation (monthly, from the
housekeeping workflow, with the built-in token) and supports --dry-run
for humans.

Design notes:
- The current lot's number is parsed from the skill file — the single
  named place (the skill's own rule).
- "Two months without a graduation" is approximated by the lot issue's
  updated_at: any body edit (checking a box, adding an idea) refreshes
  it. Documented approximation, kept deliberately simple.

Usage: backlog_rotate.py [--repo OWNER/NAME] [--skill PATH] [--dry-run]
Exit 0 whether or not rotation was due (nothing to do is success);
exit 2 on API or parse errors.
"""

import argparse
import datetime
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

SKILL_DEFAULT = ".ai/skills/backlog/SKILL.md"
CURRENT_LOT_RE = re.compile(r"\*\*Current lot: #(\d+)", re.IGNORECASE)
ITEM_RE = re.compile(r"^- \[( |x)\] (.*)$", re.MULTILINE)
ROTATE_THRESHOLD_ITEMS = 10
ROTATE_IDLE_DAYS = 60
LOT_TITLE = "Backlog: idea parking lot (lot {n})"
LOT_LABELS = ["kind/task", "area/docs"]

LOT_BODY_TEMPLATE = """\
Parking lot for ideas that are not ready to become work — lot {n} of a
rotating series (`.ai/skills/backlog/` owns the protocol).

## How to use

- One idea per task-list item or comment — a sentence or two, no design
  required
- Reference stable identifiers only (issue / AD numbers), never file
  anchors
- Graduation = a real issue exists (decided work files an issue
  directly; `status/parked` covers issue-shaped not-now work); check the
  box here and link it
- Issue-shaped but not-now work files a real issue labeled
  `status/parked` instead of waiting here

## Carried from lot {prev}

{carried}

## Ideas
"""

CARRIED_EMPTY = "- (nothing carried — the lot starts empty)"


# --------------------------------------------------------------------------
# pure logic (unit-tested)

def parse_current_lot(skill_text: str) -> int:
    match = CURRENT_LOT_RE.search(skill_text)
    if not match:
        raise ValueError("skill file has no '**Current lot: #N' line")
    return int(match.group(1))


def replace_current_lot(skill_text: str, lot: int) -> str:
    return CURRENT_LOT_RE.sub(f"**Current lot: #{lot}", skill_text, count=1)


def parse_items(body: str) -> tuple[list[str], list[str]]:
    """unchecked (carried candidates) and checked (graduated) item texts."""
    unchecked: list[str] = []
    graduated: list[str] = []
    for mark, text in ITEM_RE.findall(body):
        (graduated if mark == "x" else unchecked).append(text.strip())
    return unchecked, graduated


def rotation_due(unchecked: int, updated_at: str | None,
                 now: datetime.date | None = None) -> tuple[bool, str]:
    """(due, reason). Approximation documented in the module docstring."""
    now = now or datetime.date.today()
    if unchecked >= ROTATE_THRESHOLD_ITEMS:
        return True, f"{unchecked} ungraduated items (>= {ROTATE_THRESHOLD_ITEMS})"
    if unchecked > 0 and updated_at:
        stamp = datetime.date.fromisoformat(updated_at[:10])
        idle = (now - stamp).days
        if idle >= ROTATE_IDLE_DAYS:
            return True, (f"{idle} days without lot activity "
                          f"(>= {ROTATE_IDLE_DAYS})")
    return False, "rotation not due"


def build_summary_comment(lot: int, graduated: list[str],
                          carried: list[str]) -> str:
    def lines(items: list[str], empty: str) -> str:
        return "\n".join(f"- {item}" for item in items) if items else f"- {empty}"

    return (
        f"Backlog lot {lot} rotating per `.ai/skills/backlog/`.\n\n"
        "**Graduated**\n" + lines(graduated, "(none)") + "\n\n"
        "**Carried (re-stated in the successor)**\n"
        + lines(carried, "(none — everything else died with this lot)") + "\n\n"
        "**Died here**\n- any parked idea that earned no re-affirmation "
        "decays with this lot — that decay is the forcing function."
    )


def build_successor_body(lot: int, prev: int, carried: list[str]) -> str:
    carried_block = ("\n".join(f"- [ ] {item}" for item in carried)
                     if carried else CARRIED_EMPTY)
    return LOT_BODY_TEMPLATE.format(n=lot, prev=prev,
                                    carried=carried_block)


# --------------------------------------------------------------------------
# GitHub API

class Api:
    def __init__(self, repo: str, token: str):
        self.repo = repo
        self.token = token

    def call(self, path: str, payload: dict | None = None,
             method: str | None = None) -> dict:
        req = urllib.request.Request(
            f"https://api.github.com/repos/{self.repo}{path}",
            data=json.dumps(payload).encode() if payload else None,
            method=method,
            headers={"Authorization": f"Bearer {self.token}",
                     "Accept": "application/vnd.github+json"},
        )
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.load(resp)


def rotate(api: Api, skill_path: Path, dry_run: bool) -> int:
    skill_text = skill_path.read_text(encoding="utf-8")
    lot = parse_current_lot(skill_text)
    issue = api.call(f"/issues/{lot}")
    unchecked, graduated = parse_items(issue["body"] or "")
    due, reason = rotation_due(len(unchecked), issue.get("updated_at"))
    print(f"lot #{lot}: {len(unchecked)} ungraduated, "
          f"{len(graduated)} graduated — {reason}")
    if not due:
        return 0
    if dry_run:
        print("dry-run: would close this lot and open "
              f"lot {lot + 1} with {len(unchecked)} re-stated idea(s)")
        return 0

    successor = api.call("/issues", {
        "title": LOT_TITLE.format(n=lot + 1),
        "body": build_successor_body(lot + 1, lot, unchecked),
        "labels": LOT_LABELS,
    })
    api.call(f"/issues/{lot}", {"state": "closed",
                                "state_reason": "completed"})
    api.call(f"/issues/{lot}/comments", {
        "body": build_summary_comment(lot, graduated, unchecked)})
    # the skill file lives in git: a CI checkout is throwaway, so the
    # reference update rides a follow-up issue rather than a lost write
    followup_title = f"chore: point backlog skill at lot #{lot + 1}"
    open_issues = api.call("/issues?state=open&labels=kind/cleanup")
    if not any(i["title"] == followup_title for i in open_issues):
        api.call("/issues", {
            "title": followup_title,
            "body": (
                f"Lot #{lot} rotated; successor is #{successor['number']}.\n\n"
                "In `.ai/skills/backlog/SKILL.md` update the single named "
                f"reference:\n\n```\n**Current lot: #{lot} ...** -> "
                f"**Current lot: #{lot + 1}**\n```\n\n"
                "Land via the normal PR flow (one commit, `docs` type)."),
            "labels": ["kind/cleanup", "area/ai-assets"],
        })
    print(f"lot #{lot} closed; successor #{successor['number']} opened; "
          "follow-up issue filed for the skill reference")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"),
                        help="OWNER/NAME (default: GITHUB_REPOSITORY)")
    parser.add_argument("--skill", default=SKILL_DEFAULT,
                        help="path to the backlog skill")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--token", default=os.environ.get("GITHUB_TOKEN"),
                        help="fallback: ~/.git-credentials is not read here; "
                             "pass GITHUB_TOKEN")
    args = parser.parse_args(argv)
    if not args.repo:
        print("ERROR: --repo or GITHUB_REPOSITORY required", file=sys.stderr)
        return 2
    if not args.dry_run and not args.token:
        print("ERROR: --token or GITHUB_TOKEN required outside dry-run",
              file=sys.stderr)
        return 2
    try:
        api = Api(args.repo, args.token or "dry-run")
        return rotate(api, Path(args.skill), args.dry_run)
    except (ValueError, OSError, urllib.error.URLError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
