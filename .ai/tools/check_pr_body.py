#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""PR-body issue-reference lint — bare `Refs #N` fails (#92, AD-30).

Prevention layer of AD-30 (reliability by construction): every issue
classification in a PR body must be deliberate. Mature practice is a
plain-text contract, not rendered-Markdown parsing (GitHub's own
auto-close keywords are plain regex; commitlint/mergify lint the
source text): the lint therefore scans **reference lines only** — a
line that, after leading whitespace, blockquote (`>`), and list
(`-`, `*`, `+`, `1.`) markers are stripped, starts with a reference
keyword:

    Closes #92
    Refs #79 — stays open until the PEP shape design starts

`Closes #N` / `Fixes #N` close at merge and need no justification.
`Refs #N` / `References #N` keep the issue open and must state why on
the same line (>= 10 chars after the reference, bounded at the next
reference). Mentions in prose, quotes, code, or templates are not
classifications and are ignored — if a reference matters, put it on
its own reference line (the PR template provides the blocks).

Before line scanning, fenced blocks are blanked and HTML comments
removed in one pass (CommonMark-shaped: fence contents are literal;
a backtick fence whose info string contains a backtick is not a
fence; blockquote and list markers are normalized for fence openers
and closers; an unclosed quote-level fence ends when the quote ends;
inline code spans cannot open a comment). Without a body (local
runs, push events) the check skips with a note.

Usage: check_pr_body.py [--body TEXT]   (or PR_BODY env; --body wins)
Exit 0 when clean or skipped, 1 on violations.
"""

import argparse
import os
import re
import sys

REF_RE = re.compile(r"(?i)\b(closes|fixes|refs|references)\s*:?\s*#(\d+)")
REF_LINE_RE = re.compile(r"(?i)^(closes|fixes|refs|references)\b")
BLOCKQUOTE_RE = re.compile(r"^(?: {0,3}>[ \t]?)+")
LIST_RE = re.compile(r"^ {0,3}(?:[-*+]|\d+[.)])[ \t]+")
CODE_SPAN_RE = re.compile(r"`+[^`\n]*`+")
LEADERS_RE = re.compile(r"^[\s:：;；,，—–\-–(/（]+")
REASON_MIN = 10


def _strip_noise(text: str) -> str:
    """Blank fenced blocks and remove HTML comments in one pass."""
    out: list[str] = []
    fence_char = ""
    fence_len = 0
    fence_quoted = False
    in_comment = False
    for line in text.splitlines():
        if fence_char:
            view = BLOCKQUOTE_RE.sub("", line)
            view = LIST_RE.sub("", view)
            closing = re.match(
                r"^ {0,3}(%s{%d,})\s*$" % (re.escape(fence_char), fence_len), view)
            if closing:
                fence_char = ""
            elif fence_quoted and line.strip() and not BLOCKQUOTE_RE.match(line):
                fence_char = ""  # unclosed quote-level fence dies with the quote
                buf, in_comment = _strip_comment_spans(line, in_comment)
                out.append(buf)
                continue
            out.append("")
            continue
        view = BLOCKQUOTE_RE.sub("", line)
        view = LIST_RE.sub("", view)
        opener = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", view)
        if opener and not (opener.group(1)[0] == "`" and "`" in opener.group(2)):
            fence_char = opener.group(1)[0]
            fence_len = len(opener.group(1))
            fence_quoted = bool(BLOCKQUOTE_RE.match(line))
            out.append("")
            continue
        buf, in_comment = _strip_comment_spans(line, in_comment)
        out.append(buf)
    return "\n".join(out)


def _strip_comment_spans(line: str, in_comment: bool) -> tuple[str, bool]:
    """Remove HTML comments from one line, preserving newlines.

    Inline code spans are blanked first: backtick contents are literal,
    so `<!--` inside a span is not a comment opener.
    """
    if not in_comment:
        line = CODE_SPAN_RE.sub("", line)
    buf = ""
    rest = line
    while rest:
        if in_comment:
            k = rest.find("-->")
            if k == -1:
                rest = ""
            else:
                rest = rest[k + 3:]
                in_comment = False
        else:
            k = rest.find("<!--")
            if k == -1:
                buf += rest
                rest = ""
            else:
                buf += rest[:k]
                rest = rest[k + 4:]
                in_comment = True
    return buf, in_comment


def violations(body: str) -> list[str]:
    text = _strip_noise(body)
    problems = []
    for line in text.splitlines():
        stripped = line.lstrip()
        if stripped.startswith(">"):
            continue  # quoted content is a mention, never a classification
        stripped = LIST_RE.sub("", stripped).lstrip()
        if not REF_LINE_RE.match(stripped):
            continue
        matches = list(REF_RE.finditer(stripped))
        for i, m in enumerate(matches):
            if m.group(1).lower() in ("closes", "fixes"):
                continue
            end = matches[i + 1].start() if i + 1 < len(matches) else len(stripped)
            rest = LEADERS_RE.sub("", stripped[m.end():end]).strip()
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
