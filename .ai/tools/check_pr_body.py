#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""PR-body issue-reference lint — bare `Refs #N` fails (#92, AD-30).

Prevention layer of AD-30 (reliability by construction): every issue
referenced in a PR body must be classified. `Closes #N` / `Fixes #N`
close at merge and need no justification. `Refs #N` keeps the issue
open and therefore must state why, on the same line:

    Refs #79 — stays open until the PEP shape design starts

Stripping is line-based (CommonMark fences: a fence opens with a
run of >= 3 identical backticks or tildes after <= 3 leading spaces,
and closes only with the same character in a run at least as long,
with whitespace-only trailing content). HTML comments are removed
with their newlines preserved so lines never merge. A reason ends at
the next issue reference on the same line or at end of line.

Without a body (local runs, push events) the check skips with a
note, like lint-go without go.mod.

Usage: check_pr_body.py [--body TEXT]   (or PR_BODY env; --body wins)
Exit 0 when clean or skipped, 1 on violations.
"""

import argparse
import os
import re
import sys

REF_RE = re.compile(r"(?i)\b(closes|fixes|refs|references)\s*:?\s*#(\d+)")
COMMENT_RE = re.compile(r"<!--.*?-->", re.DOTALL)
OPEN_FENCE_RE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
LEADERS_RE = re.compile(r"^[\s:：;；,，—–\-–(/（]+")
REASON_MIN = 10


def _strip_comments(text: str) -> str:
    """Remove HTML comments, preserving their newlines."""

    def repl(match: re.Match) -> str:
        return "\n" * match.group(0).count("\n")

    return COMMENT_RE.sub(repl, text)


def _strip_fences(text: str) -> str:
    """Blank out fenced code blocks (CommonMark-close rules)."""
    out: list[str] = []
    char = ""
    length = 0
    for line in text.splitlines():
        if char:
            closing = re.match(r"^ {0,3}(%s{%d,})\s*$" % (re.escape(char), length), line)
            if closing:
                char, length = "", 0
            out.append("")
            continue
        m = OPEN_FENCE_RE.match(line)
        if m:
            char, length = m.group(1)[0], len(m.group(1))
            out.append("")
        else:
            out.append(line)
    return "\n".join(out)


def violations(body: str) -> list[str]:
    text = _strip_fences(_strip_comments(body))
    problems = []
    for line in text.splitlines():
        matches = list(REF_RE.finditer(line))
        for i, m in enumerate(matches):
            if m.group(1).lower() in ("closes", "fixes"):
                continue
            end = matches[i + 1].start() if i + 1 < len(matches) else len(line)
            rest = LEADERS_RE.sub("", line[m.end():end]).strip()
            if len(rest) < REASON_MIN:
                problems.append(
                    f"`{m.group(0)}` has no stated stay-open reason "
                    f"(need >= {REASON_MIN} chars on the same line, "
                    f"e.g. `Refs #N — <reason>`)"
                )
    return problems


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--body", default=None,
                    help="PR body text (default: PR_BODY env; empty skips)")
    args = ap.parse_args(argv)
    body = args.body if args.body is not None else os.environ.get("PR_BODY", "")
    if not body.strip():
        print("pr-body: no PR body given, skipped")
        return 0
    problems = violations(body)
    if problems:
        for p in problems:
            print(f"ERROR: {p}", file=sys.stderr)
        print(f"pr-body: FAILED ({len(problems)} problem(s))", file=sys.stderr)
        return 1
    print("pr-body: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
