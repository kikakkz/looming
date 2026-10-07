#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""pr-watch — own the PR review-fix loop's mechanical parts (issue #23).

Watches one PR's review state, lists actionable findings, and performs
the two idempotent writes the loop needs: a single
`@coderabbitai full review` nudge per head when checks are green and
no verdict is on the current head, plus review-thread housekeeping
(resolve/dismiss) via GraphQL. Decision-making stays in the
`.ai/skills/pr-watch/` skill; this tool only gathers state and guards
the writes.

Empirical CodeRabbit behavior this encodes (field notes in #21):
- zero-finding incremental reviews are silent and mark the head reviewed
- `@coderabbitai full review` is the only reliable verdict lever
- a just-dismissed approval (dismiss_stale_reviews on push) reads as
  "no review" here — treat it as in-flight and re-acquire via nudge
- conversation resolution blocks merge independently of reviews

Commands:
  status PR    — JSON snapshot: head, checks, verdict, open threads, nudge state
  findings PR  — open (unresolved, non-outdated) review threads as JSON
  nudge PR     — post ONE full-review comment for the current head (guarded)
  resolve THREAD_ID — mark a review thread resolved (GraphQL)
  dismiss REVIEW_ID — dismiss a stale review (GraphQL)

Options: --repo OWNER/NAME (default: origin remote), --gh PATH,
--dry-run (print the comment instead of posting).

gh compatibility: GraphQL Int variables must ride the typed flag `-F`
— gh 2.101 stopped coercing `-f` string values into Int (#133).
`graphql_args` owns the per-type flag choice; call sites declare types.

Exit codes: 0 ok / 1 usage, auth, or guard-blocked / 2 unexpected.
"""

import argparse
import json
import re
import subprocess
import sys

NUDGE_TEXT = "@coderabbitai full review"
BOT_LOGIN = "coderabbitai[bot]"


# --------------------------------------------------------------------------
# gh plumbing

def run_gh(args: list[str], gh: str = "gh", timeout: int = 60) -> str:
    try:
        proc = subprocess.run([gh, *args], capture_output=True, text=True,
                              timeout=timeout)
    except (subprocess.TimeoutExpired, FileNotFoundError) as exc:
        raise RuntimeError(f"gh {' '.join(args)}: {exc}") from exc
    if proc.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args)}: {proc.stderr.strip()}")
    return proc.stdout


def gh_json(args: list[str], gh: str = "gh") -> dict:
    return json.loads(run_gh([*args, "--jq", "."], gh) or "{}")


def gh_json_list(args: list[str], gh: str = "gh") -> list:
    out = run_gh([*args, "--paginate", "--jq", "."], gh)
    # --paginate concatenates JSON arrays; a single page is one array.
    decoder = json.JSONDecoder()
    items, idx = [], 0
    while idx < len(out):
        chunk, end = decoder.raw_decode(out, idx)
        items.extend(chunk if isinstance(chunk, list) else [chunk])
        idx = end
        while idx < len(out) and out[idx] in " \t\r\n,":
            idx += 1
    return items


def graphql_args(query: str,
                 variables: list[tuple[str, object, str]]) -> list[str]:
    """Assemble a `gh api graphql` argv from typed variables.

    gh 2.101 stopped coercing `-f name=value` strings into GraphQL Int,
    so Int-typed variables must ride the typed flag `-F` (the API parses
    `123` as Int); every other type stays a `-f` string (#133).
    """
    args = ["api", "graphql", "-f", f"query={query}"]
    for name, value, gtype in variables:
        flag = "-F" if gtype == "Int" else "-f"
        args += [flag, f"{name}={value}"]
    return args


def detect_repo(gh: str = "gh") -> str:
    try:
        proc = subprocess.run(
            ["git", "config", "--get", "remote.origin.url"],
            capture_output=True, text=True, timeout=30)
    except (subprocess.TimeoutExpired, FileNotFoundError) as exc:
        raise RuntimeError(f"git config remote.origin.url: {exc}") from exc
    url = proc.stdout.strip()
    match = re.search(r"github\.com[:/]([^/]+/[^/]+?)(?:\.git)?$", url)
    if not match:
        raise RuntimeError("cannot determine owner/repo from remote.origin.url")
    return match.group(1)


# --------------------------------------------------------------------------
# state model

def head_sha(pull: dict) -> str:
    return pull["head"]["sha"]


def checks_state(check_runs: list[dict]) -> str:
    """green when every required run concluded success/skipped/neutral."""
    relevant = [c for c in check_runs if c.get("status") == "completed"]
    if any(c["conclusion"] not in ("success", "neutral", "skipped")
           for c in relevant):
        return "failing"
    if len(relevant) != len(check_runs):
        return "pending"
    return "green"


def verdict_for_head(reviews: list[dict], head: str) -> str:
    """latest standing bot verdict on this head, else 'none'.

    COMMENTED reviews (acknowledgements, summary notes) never override
    a standing APPROVED / CHANGES_REQUESTED; DISMISSED stays selectable
    so an explicit dismissal clears the verdict.
    """
    latest: dict | None = None
    for review in reviews:
        if review["user"]["login"] != BOT_LOGIN:
            continue
        if review.get("state") == "COMMENTED":
            continue
        if review.get("commit_id") != head:
            continue
        when = review.get("submitted_at") or ""
        if latest is None or when >= (latest.get("submitted_at") or ""):
            latest = review
    return latest["state"] if latest else "none"


def open_threads(repo: str, pr: int, gh: str) -> list[dict]:
    query = """query($owner:String!,$name:String!,$number:Int!,$after:String) {
      repository(owner:$owner, name:$name) {
        pullRequest(number:$number) {
          reviewThreads(first:100, after:$after) {
            pageInfo { hasNextPage endCursor }
            nodes {
              id isResolved isOutdated path line
              comments(first:1) { nodes { body author { login } } }
            }
          }
        }
      }
    }"""
    owner, name = repo.split("/")
    nodes: list[dict] = []
    cursor: str | None = None
    while True:
        args = graphql_args(query, [
            ("owner", owner, "String"),
            ("name", name, "String"),
            ("number", pr, "Int"),
            ("after", cursor or "", "String"),
        ])
        out = json.loads(run_gh(args, gh))
        threads = (out["data"]["repository"]["pullRequest"]
                   ["reviewThreads"])
        nodes.extend(threads["nodes"])
        page = threads["pageInfo"]
        if not page["hasNextPage"]:
            break
        cursor = page["endCursor"]
    # GitHub tracks isOutdated and isResolved separately: a fix can
    # outdate a thread without resolving it, and those conversations
    # still block merge (required_conversation_resolution). Keep every
    # unresolved thread visible; callers decide fix vs resolve.
    return [t for t in nodes if not t["isResolved"]]


def nudges_for_head(comments: list[dict], head: str) -> int:
    """our full-review nudges that name this head SHA (one nudge per head).

    Matching the full `head: <sha>` marker line recorded in our own nudge
    comment, not a bare SHA substring: prose mentioning the SHA must not
    block the review request. Timestamps are not used at all —
    head.repo.pushed_at moves on any push to any branch of the fork, so
    timestamp windows can reset the count and allow a second nudge.
    """
    marker = f"\nhead: {head}"
    return sum(1 for c in comments
               if NUDGE_TEXT in c.get("body", "")
               and marker in c.get("body", ""))


def snapshot(repo: str, pr: int, gh: str) -> dict:
    pull = gh_json(["api", f"repos/{repo}/pulls/{pr}"], gh)
    head = head_sha(pull)
    reviews = gh_json_list(["api", f"repos/{repo}/pulls/{pr}/reviews"], gh)
    # the check-runs endpoint wraps runs in {"total_count", "check_runs"};
    # ask gh for the array so --paginate concatenates pages correctly
    checks = gh_json_list(
        ["api", f"repos/{repo}/commits/{head}/check-runs",
         "--jq", ".check_runs"], gh)
    comments = gh_json_list(
        ["api", f"repos/{repo}/issues/{pr}/comments"], gh)
    threads = open_threads(repo, pr, gh)
    return {
        "pr": pr,
        "head": head,
        "merged": pull.get("merged", False),
        "state": pull.get("state"),
        "checks": checks_state(checks),
        "verdict": verdict_for_head(reviews, head),
        "open_threads": len(threads),
        "nudges_for_head": nudges_for_head(comments, head),
    }


# --------------------------------------------------------------------------
# guarded writes

def cmd_nudge(repo: str, pr: int, dry_run: bool, gh: str) -> int:
    snap = snapshot(repo, pr, gh)
    if snap["state"] != "open" or snap["merged"]:
        print("nudge: PR closed or merged — nothing to do", file=sys.stderr)
        return 1
    if snap["checks"] != "green":
        print(f"nudge: checks are {snap['checks']} — wait for green",
              file=sys.stderr)
        return 1
    if snap["verdict"] == "APPROVED":
        print("nudge: current head already APPROVED — auto-merge owns the rest",
              file=sys.stderr)
        return 1
    if snap["verdict"] == "CHANGES_REQUESTED":
        print("nudge: standing CHANGES_REQUESTED — fix findings first",
              file=sys.stderr)
        return 1
    if snap["open_threads"]:
        print(f"nudge: {snap['open_threads']} unresolved threads — "
              "fix or resolve before re-review", file=sys.stderr)
        return 1
    if snap["nudges_for_head"]:
        print("nudge: a full-review request already exists for this head "
              "(one nudge per head)", file=sys.stderr)
        return 1
    if dry_run:
        print(NUDGE_TEXT)
        return 0
    body = f"{NUDGE_TEXT}\n\nhead: {snap['head']}"
    run_gh(["api", "-X", "POST", f"repos/{repo}/issues/{pr}/comments",
            "-f", f"body={body}"], gh)
    print(f"nudge: posted on #{pr} @ {snap['head'][:8]}")
    return 0


def cmd_resolve(repo: str, thread_id: str, gh: str) -> int:
    query = ("mutation($id:ID!){resolveReviewThread(input:{threadId:$id})"
             "{thread{isResolved}}}")
    run_gh(graphql_args(query, [("id", thread_id, "ID")]), gh)
    print(f"resolved thread {thread_id}")
    return 0


def cmd_dismiss(repo: str, review_id: str, gh: str) -> int:
    query = ("mutation($id:ID!){dismissPullRequestReview("
             "input:{pullRequestReviewId:$id,message:\"outdated: fixed "
             "or superseded on a later head\"}){pullRequestReview{id}}}")
    run_gh(graphql_args(query, [("id", review_id, "ID")]), gh)
    print(f"dismissed review {review_id}")
    return 0


# --------------------------------------------------------------------------
# commands

def cmd_status(repo: str, pr: int, gh: str) -> int:
    print(json.dumps(snapshot(repo, pr, gh), indent=2))
    return 0


def cmd_findings(repo: str, pr: int, gh: str) -> int:
    threads = open_threads(repo, pr, gh)
    slim = [{
        "id": t["id"],
        "path": t.get("path"),
        "line": t.get("line"),
        "outdated": t.get("isOutdated", False),
        "author": t["comments"]["nodes"][0]["author"]["login"]
        if t["comments"]["nodes"] else None,
        "body": (t["comments"]["nodes"][0]["body"]
                 if t["comments"]["nodes"] else ""),
    } for t in threads]
    # findings that live in the review body rather than the diff have no
    # thread; surface the latest CHANGES_REQUESTED body so triage sees them
    pull = gh_json(["api", f"repos/{repo}/pulls/{pr}"], gh)
    head = head_sha(pull)
    reviews = gh_json_list(["api", f"repos/{repo}/pulls/{pr}/reviews"], gh)
    on_head = [r for r in reviews
               if r["user"]["login"] == BOT_LOGIN
               and r.get("commit_id") == head
               and r.get("state") != "COMMENTED"]
    if on_head:
        latest = max(on_head, key=lambda r: r.get("submitted_at") or "")
        if latest.get("state") != "CHANGES_REQUESTED" \
                or not (latest.get("body") or "").strip():
            latest = None
    else:
        latest = None
    if latest:
        slim.append({
            "id": None,
            "path": None,
            "line": None,
            "outdated": False,
            "author": BOT_LOGIN,
            "body": "[review summary] " + latest["body"].strip(),
        })
    print(json.dumps(slim, indent=2))
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", help="OWNER/NAME (default: origin remote)")
    parser.add_argument("--gh", default="gh", help="path to the gh binary")
    parser.add_argument("--dry-run", action="store_true",
                        help="print instead of posting (nudge only)")
    parser.add_argument("command",
                        choices=["status", "findings", "nudge",
                                 "resolve", "dismiss"])
    parser.add_argument("target",
                        help="PR number (status/findings/nudge) or GraphQL "
                             "node id (resolve/dismiss)")
    args = parser.parse_args(argv)
    try:
        repo = args.repo or detect_repo(args.gh)
        if args.command == "resolve":
            return cmd_resolve(repo, args.target, args.gh)
        if args.command == "dismiss":
            return cmd_dismiss(repo, args.target, args.gh)
        if not args.target.isdigit():
            print(f"ERROR: PR number expected, got {args.target!r}",
                  file=sys.stderr)
            return 1
        pr = int(args.target)
        if args.command == "status":
            return cmd_status(repo, pr, args.gh)
        if args.command == "findings":
            return cmd_findings(repo, pr, args.gh)
        return cmd_nudge(repo, pr, args.dry_run, args.gh)
    except (RuntimeError, KeyError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
