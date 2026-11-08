#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""create-pr — open a PR with auto-merge armed, as one indivisible step (#203).

PRs are born merge-ready: this tool wraps `gh pr create` and immediately
arms squash auto-merge, so the review loop (pr-watch skill) can rely on
"APPROVED -> auto-merge owns the rest" being true. A PR that exists but
is not armed is treated as failure: partial success exits non-zero and
prints the PR URL for repair.

Usage:
  create_pr.py --title TITLE (--body BODY | --body-file PATH)
               [--base BASE] [--repo OWNER/NAME] [--gh PATH]

Exit codes: 0 created + armed / 1 usage error or gh failure / 2 unexpected.
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path


def run_gh(args: list[str], gh: str = "gh") -> str:
    try:
        proc = subprocess.run([gh, *args], capture_output=True, text=True,
                              timeout=120)
    except (subprocess.TimeoutExpired, FileNotFoundError) as exc:
        raise RuntimeError(f"gh {' '.join(args)}: {exc}") from exc
    if proc.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args)}: {proc.stderr.strip()}")
    return proc.stdout


def pr_number_from_url(url: str) -> str:
    return url.rstrip("/").rsplit("/", 1)[-1]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--title", required=True)
    body = parser.add_mutually_exclusive_group(required=True)
    body.add_argument("--body")
    body.add_argument("--body-file", type=Path)
    parser.add_argument("--base", default=None, help="target branch")
    parser.add_argument("--repo", default=None, help="OWNER/NAME override")
    parser.add_argument("--gh", default="gh", help="path to the gh binary")
    args = parser.parse_args(argv)

    text = args.body if args.body is not None \
        else args.body_file.read_text(encoding="utf-8")

    create = ["pr", "create", "--title", args.title, "--body", text]
    if args.base:
        create += ["--base", args.base]
    if args.repo:
        create += ["--repo", args.repo]

    url = ""
    try:
        url = run_gh(create, args.gh).strip()
        number = pr_number_from_url(url)
        run_gh(["pr", "merge", number, "--squash", "--auto"], args.gh)
    except RuntimeError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        if url:
            print(f"PR created but auto-merge NOT armed: {url}",
                  file=sys.stderr)
        return 1

    print(json.dumps({"pr": int(number), "url": url,
                      "auto_merge": True}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
