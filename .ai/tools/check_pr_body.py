#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""PR-body issue-reference lint — bare `Refs #N` fails (#92, AD-30).

Prevention layer of AD-30 (reliability by construction): every issue
referenced in a PR body must be classified. `Closes #N` / `Fixes #N`
close at merge and need no justification. `Refs #N` keeps the issue
open and therefore must state why, on the same line:

    Refs #79 — stays open until the PEP shape design starts

Fenced code blocks and HTML comments are stripped before scanning so
quoted content (review bodies, suggestion blocks) cannot false-
positive. Without a body (local runs, push events) the check skips
with a note, like lint-go without go.mod.

Usage: check_pr_body.py [--body TEXT]   (or PR_BODY env; body wins)
Exit 0 when clean or skipped, 1 on violations.
"""

import argparse
import os
import re
import sys

REF_RE = re.compile(r"(?i)\b(closes|fixes|refs|references)\s+#(\d+)")
FENCE_RE = re.compile(r"```.*?```", re.DOTALL)
COMMENT_RE = re.compile(r"<!--.*?-->", re.DOTALL)
LEADERS_RE = re.compile(r"^[\s:：;；,，—–\-–(/（]+")
REASON_MIN = 10


def violations(body: str) -> list[str]:
    text = COMMENT_RE.sub("", FENCE_RE.sub("", body))
    problems = []
    for line in text.splitlines():
        for m in REF_RE.finditer(line):
            if m.group(1).lower() in ("closes", "fixes"):
                continue
            rest = LEADERS_RE.sub("", line[m.end():]).strip()
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
