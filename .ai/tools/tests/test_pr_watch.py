#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for pr_watch.py — every gh call is mocked at the boundary."""

import io
import json
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import pr_watch as pw

HEAD = "abc123def4567890"
BOT = pw.BOT_LOGIN


def review(state: str, login: str = BOT, commit: str = HEAD,
           submitted: str = "2026-10-01T00:00:00Z") -> dict:
    return {"state": state, "user": {"login": login},
            "commit_id": commit, "submitted_at": submitted}


def pull() -> dict:
    return {"head": {"sha": HEAD,
                     "repo": {"pushed_at": "2026-10-01T00:00:00Z"}},
            "merged": False, "state": "open"}


class GhFake:
    """Routes gh argv to canned responses; records writes."""

    def __init__(self, routes: dict[tuple, object]):
        self.routes = routes
        self.posts: list[list[str]] = []

    def __call__(self, args: list[str], gh: str = "gh") -> str:
        if args[:2] == ["api", "graphql"]:
            kind = "mutation" if any("mutation(" in a for a in args) \
                else "query"
            key = ("api", "graphql", kind)
        else:
            key = tuple(a for a in args
                        if not a.startswith(("--jq", "--paginate"))
                        and a != ".")
        if args[:3] == ["api", "-X", "POST"] or (
                args[:2] == ["api", "graphql"]
                and any("mutation" in a for a in args)):
            self.posts.append(args)
            return "{}"
        if key not in self.routes:
            raise RuntimeError(f"gh route missing: {key}")
        payload = self.routes[key]
        return payload if isinstance(payload, str) else json.dumps(payload)


def base_routes() -> dict:
    return {
        ("api", "repos/x/y/pulls/7"): pull(),
        ("api", "repos/x/y/pulls/7/reviews"): [review("COMMENTED")],
        ("api", f"repos/x/y/commits/{HEAD}/check-runs"):
            [{"status": "completed", "conclusion": "success"}],
        ("api", "repos/x/y/issues/7/comments"): [],
        ("api", "graphql", "query"): {"data": {"repository": {"pullRequest":
            {"reviewThreads": {"nodes": []}}}}},
    }


class PureFunctionTests(unittest.TestCase):
    def test_checks_green(self) -> None:
        self.assertEqual(pw.checks_state(
            [{"status": "completed", "conclusion": "success"},
             {"status": "completed", "conclusion": "skipped"}]), "green")

    def test_checks_failing(self) -> None:
        self.assertEqual(pw.checks_state(
            [{"status": "completed", "conclusion": "failure"}]), "failing")

    def test_checks_pending(self) -> None:
        self.assertEqual(pw.checks_state(
            [{"status": "completed", "conclusion": "success"},
             {"status": "queued"}]), "pending")

    def test_verdict_picks_latest_on_head(self) -> None:
        reviews = [review("CHANGES_REQUESTED",
                          submitted="2026-09-30T00:00:00Z"),
                   review("APPROVED")]
        self.assertEqual(pw.verdict_for_head(reviews, HEAD), "APPROVED")

    def test_verdict_ignores_other_heads(self) -> None:
        reviews = [review("CHANGES_REQUESTED", commit="oldsha")]
        self.assertEqual(pw.verdict_for_head(reviews, HEAD), "none")

    def test_open_threads_query_is_balanced(self) -> None:
        # live-demo regression: a string-concatenation slip produced
        # `linecomments` and an unbalanced query that gh rejected
        sent: list[list[str]] = []
        fake = GhFake({("api", "graphql", "query"): {"data": {"repository":
            {"pullRequest": {"reviewThreads": {"nodes": []}}}}}})

        def spy(args: list[str], gh: str = "gh") -> str:
            sent.append(args)
            return fake(args, gh)

        with mock.patch.object(pw, "run_gh", spy):
            self.assertEqual(pw.open_threads("x/y", 7, "gh"), [])
        body = next(a.split("=", 1)[1] for a in sent[0]
                    if a.startswith("query="))
        self.assertEqual(body.count("{"), body.count("}"))
        self.assertIn("line\n", body)  # field boundary survived

    def test_nudge_count_filters_text_and_time(self) -> None:
        comments = [
            {"body": "@coderabbitai full review",
             "created_at": "2026-10-02T00:00:00Z"},
            {"body": "@coderabbitai full review",
             "created_at": "2026-09-01T00:00:00Z"},
            {"body": "unrelated", "created_at": "2026-10-02T00:00:00Z"},
        ]
        self.assertEqual(pw.nudge_count_since(
            comments, "2026-10-01T00:00:00Z"), 1)

    def test_detect_repo_from_https_and_scp(self) -> None:
        for url in ("https://github.com/own/rep.git",
                    "git@github.com:own/rep.git"):
            with mock.patch("subprocess.run") as run:
                run.return_value = mock.Mock(stdout=url + "\n",
                                             returncode=0)
                self.assertEqual(pw.detect_repo(), "own/rep")


class NudgeGuardTests(unittest.TestCase):
    def nudge(self, fake: GhFake) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            with mock.patch.object(pw, "run_gh", fake):
                code = pw.cmd_nudge("x/y", 7, False, "gh")
        return code, out.getvalue(), err.getvalue()

    def test_posts_when_green_undecided_unnudged(self) -> None:
        fake = GhFake(base_routes())
        code, out, _ = self.nudge(fake)
        self.assertEqual(code, 0)
        self.assertIn("posted", out)
        self.assertTrue(any("full review" in " ".join(p)
                            for p in fake.posts))

    def test_blocked_when_checks_failing(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", f"repos/x/y/commits/{HEAD}/check-runs")] = \
            [{"status": "completed", "conclusion": "failure"}]
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("checks", err)

    def test_blocked_when_merged(self) -> None:
        fake = GhFake(base_routes())
        p = pull()
        p["state"] = "closed"
        p["merged"] = True
        fake.routes[("api", "repos/x/y/pulls/7")] = p
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("closed or merged", err)

    def test_blocked_when_already_approved(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", "repos/x/y/pulls/7/reviews")] = \
            [review("APPROVED")]
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("APPROVED", err)

    def test_blocked_when_changes_requested(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", "repos/x/y/pulls/7/reviews")] = \
            [review("CHANGES_REQUESTED")]
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("CHANGES_REQUESTED", err)

    def test_blocked_on_second_nudge_same_head(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", "repos/x/y/pulls/7/reviews")] = []
        fake.routes[("api", "repos/x/y/issues/7/comments")] = [
            {"body": "@coderabbitai full review",
             "created_at": "2026-10-01T01:00:00Z"}]
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("one nudge per head", err)

    def test_blocked_with_open_threads(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", "repos/x/y/pulls/7/reviews")] = []
        fake.routes[("api", "graphql", "query")] = {
            "data": {"repository": {"pullRequest": {"reviewThreads":
                {"nodes": [{"id": "t1", "isResolved": False,
                            "isOutdated": False, "path": "f", "line": 1,
                            "comments": {"nodes": []}}]}}}}}
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("unresolved threads", err)

    def test_dry_run_prints_without_posting(self) -> None:
        fake = GhFake(base_routes())
        out = io.StringIO()
        with redirect_stdout(out):
            with mock.patch.object(pw, "run_gh", fake):
                self.assertEqual(pw.cmd_nudge("x/y", 7, True, "gh"), 0)
        self.assertIn("@coderabbitai full review", out.getvalue())
        self.assertEqual(fake.posts, [])


class CliTests(unittest.TestCase):
    def test_rejects_non_numeric_pr(self) -> None:
        err = io.StringIO()
        with redirect_stderr(err):
            with mock.patch.object(pw, "detect_repo", lambda gh: "x/y"):
                code = pw.main(["status", "abc"])
        self.assertEqual(code, 1)
        self.assertIn("PR number expected", err.getvalue())

    def test_resolve_dispatches_graphql(self) -> None:
        fake = GhFake({("api", "graphql", "mutation"): {"data": {}}})
        out = io.StringIO()
        with redirect_stdout(out):
            with mock.patch.object(pw, "run_gh", fake):
                self.assertEqual(
                    pw.main(["--repo", "x/y", "resolve", "THREADNODE"]), 0)
        self.assertTrue(any("resolveReviewThread" in " ".join(p)
                            for p in fake.posts))

    def test_dismiss_dispatches_graphql(self) -> None:
        fake = GhFake({("api", "graphql", "mutation"): {"data": {}}})
        with redirect_stdout(io.StringIO()):
            with mock.patch.object(pw, "run_gh", fake):
                self.assertEqual(
                    pw.main(["--repo", "x/y", "dismiss", "REVIEWNODE"]), 0)
        self.assertTrue(any("dismissPullRequestReview" in " ".join(p)
                            for p in fake.posts))


if __name__ == "__main__":
    unittest.main()
