#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for pr_watch.py — every gh call is mocked at the boundary."""

import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import ExitStack, redirect_stderr, redirect_stdout
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
                        if not a.startswith(("--jq", "--paginate", ".")))
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
        # the fake returns the jq-selected payload directly, as gh would
        # after applying the caller's `--jq .check_runs` client-side
        ("api", f"repos/x/y/commits/{HEAD}/check-runs"):
            [{"status": "completed", "conclusion": "success"}],
        ("api", "repos/x/y/issues/7/comments"): [],
        ("api", "graphql", "query"): {"data": {"repository": {"pullRequest":
            {"reviewThreads": {"pageInfo": {"hasNextPage": False,
                                            "endCursor": None},
                                 "nodes": []}}}}},
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

    def test_commented_review_preserves_standing_verdict(self) -> None:
        reviews = [review("APPROVED",
                          submitted="2026-10-01T00:00:00Z"),
                   review("COMMENTED",
                          submitted="2026-10-01T01:00:00Z")]
        self.assertEqual(pw.verdict_for_head(reviews, HEAD), "APPROVED")

    def test_run_gh_wraps_subprocess_failures(self) -> None:
        with mock.patch("subprocess.run",
                        side_effect=FileNotFoundError("gh")):
            with self.assertRaises(RuntimeError):
                pw.run_gh(["api", "x"])
        with mock.patch("subprocess.run",
                        side_effect=subprocess.TimeoutExpired("gh", 60)):
            with self.assertRaises(RuntimeError):
                pw.run_gh(["api", "x"])

    def test_verdict_ignores_other_heads(self) -> None:
        reviews = [review("CHANGES_REQUESTED", commit="oldsha")]
        self.assertEqual(pw.verdict_for_head(reviews, HEAD), "none")

    def test_open_threads_query_is_balanced(self) -> None:
        # live-demo regression: a string-concatenation slip produced
        # `linecomments` and an unbalanced query that gh rejected
        sent: list[list[str]] = []
        fake = GhFake({("api", "graphql", "query"): {"data": {"repository":
            {"pullRequest": {"reviewThreads": {
                "pageInfo": {"hasNextPage": False, "endCursor": None},
                "nodes": []}}}}}})

        def spy(args: list[str], gh: str = "gh") -> str:
            sent.append(args)
            return fake(args, gh)

        with mock.patch.object(pw, "run_gh", spy):
            self.assertEqual(pw.open_threads("x/y", 7, "gh"), [])
        body = next(a.split("=", 1)[1] for a in sent[0]
                    if a.startswith("query="))
        self.assertEqual(body.count("{"), body.count("}"))
        self.assertIn("line\n", body)  # field boundary survived

    def test_open_threads_paginates_past_first_page(self) -> None:
        # a connection limited with first:100 needs explicit pagination:
        # an unresolved thread on page 2 must still block the nudge
        resolved = {"id": "t0", "isResolved": True, "isOutdated": False,
                    "path": "f", "line": 1, "comments": {"nodes": []}}
        pending = {"id": "t1", "isResolved": False, "isOutdated": False,
                   "path": "f", "line": 2, "comments": {"nodes": []}}
        pages = [
            {"reviewThreads": {"pageInfo": {"hasNextPage": True,
                                            "endCursor": "CURSOR1"},
                               "nodes": [resolved]}},
            {"reviewThreads": {"pageInfo": {"hasNextPage": False,
                                            "endCursor": None},
                               "nodes": [pending]}},
        ]
        calls: list[list[str]] = []

        def paged(args: list[str], gh: str = "gh") -> str:
            calls.append(args)
            after = next((a.split("=", 1)[1] for a in args
                          if a.startswith("after=")), "")
            page = pages[1] if after == "CURSOR1" else pages[0]
            return json.dumps({"data": {"repository":
                {"pullRequest": page}}})

        with mock.patch.object(pw, "run_gh", paged):
            threads = pw.open_threads("x/y", 7, "gh")
        self.assertEqual([t["id"] for t in threads], ["t1"])
        self.assertEqual(len(calls), 2)

    def test_nudge_count_matches_head_sha(self) -> None:
        comments = [
            {"body": "@coderabbitai full review\n\nhead: " + HEAD},
            {"body": "@coderabbitai full review\n\nhead: oldsha"},
            # prose mentioning the SHA without the marker line must not block
            {"body": "the push " + HEAD + " broke things"},
            {"body": "unrelated"},
        ]
        self.assertEqual(pw.nudges_for_head(comments, HEAD), 1)

    def test_detect_repo_from_https_and_scp(self) -> None:
        for url in ("https://github.com/own/rep.git",
                    "git@github.com:own/rep.git"):
            with mock.patch("subprocess.run") as run:
                run.return_value = mock.Mock(stdout=url + "\n",
                                             returncode=0)
                self.assertEqual(pw.detect_repo(), "own/rep")

    def test_gh_json_list_keeps_caller_jq_expression(self) -> None:
        # #156: gh evaluates the LAST --jq expression, so stacking the
        # default `.` over a caller's `.check_runs` made the check-runs
        # endpoint return its wrapper object — checks never read green
        # and the nudge guard pinned every PR at "wait for green"
        sent: list[list[str]] = []

        def spy(args: list[str], gh: str = "gh") -> str:
            sent.append(args)
            return "[]"

        with mock.patch.object(pw, "run_gh", spy):
            self.assertEqual(
                pw.gh_json_list(["api", "x", "--jq", ".check_runs"], "gh"),
                [])
        self.assertEqual(sum(1 for a in sent[0] if a == "--jq"), 1)
        self.assertIn(".check_runs", sent[0])


class GraphqlArgvTests(unittest.TestCase):
    """#133: gh 2.101 stopped coercing `-f` strings into GraphQL Int, so
    Int-typed variables must ride the typed `-F` flag; assert on the
    constructed argv (subprocess stays mocked)."""

    @staticmethod
    def flag_pairs(args: list[str]) -> list[tuple[str, str]]:
        return list(zip(args, args[1:]))

    def test_int_var_rides_typed_flag(self) -> None:
        args = pw.graphql_args("query($number:Int!){x}",
                               [("number", 7, "Int")])
        self.assertIn(("-F", "number=7"), self.flag_pairs(args))

    def test_string_vars_ride_plain_flag(self) -> None:
        args = pw.graphql_args("query($owner:String!,$after:String){x}",
                               [("owner", "octo", "String"),
                                ("after", "", "String")])
        pairs = self.flag_pairs(args)
        self.assertIn(("-f", "owner=octo"), pairs)
        self.assertIn(("-f", "after="), pairs)  # null cursor = empty
        self.assertNotIn("-F", args)

    def test_id_var_rides_plain_flag(self) -> None:
        # resolve/dismiss: ID! accepts a string; stays on -f
        args = pw.graphql_args("mutation($id:ID!){x}",
                               [("id", "PRRT_thread", "ID")])
        self.assertIn(("-f", "id=PRRT_thread"), self.flag_pairs(args))
        self.assertNotIn("-F", args)

    def test_open_threads_sends_number_as_typed(self) -> None:
        # argv-level regression for the status/findings/nudge path: the
        # Int! PR number broke under gh 2.101 when sent as -f string
        sent: list[list[str]] = []
        fake = GhFake(base_routes())

        def spy(args: list[str], gh: str = "gh") -> str:
            sent.append(args)
            return fake(args, gh)

        with mock.patch.object(pw, "run_gh", spy):
            pw.open_threads("x/y", 7, "gh")
        pairs = self.flag_pairs(sent[0])
        self.assertIn(("-F", "number=7"), pairs)
        self.assertIn(("-f", "owner=x"), pairs)
        self.assertIn(("-f", "name=y"), pairs)


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
            {"body": "@coderabbitai full review\n\nhead: " + HEAD}]
        code, _, err = self.nudge(fake)
        self.assertEqual(code, 1)
        self.assertIn("one nudge per head", err)

    def test_blocked_with_open_threads(self) -> None:
        fake = GhFake(base_routes())
        fake.routes[("api", "repos/x/y/pulls/7/reviews")] = []
        fake.routes[("api", "graphql", "query")] = {
            "data": {"repository": {"pullRequest": {"reviewThreads":
                {"pageInfo": {"hasNextPage": False, "endCursor": None},
                 "nodes": [{"id": "t1", "isResolved": False,
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


class RetireTests(unittest.TestCase):
    """retire: real git plumbing in a throwaway repo, gh mocked at the
    run_gh boundary; repo_root anchored at the throwaway repo."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.repo = self.tmp / "repo"
        self.repo.mkdir()
        self.env = os.environ.copy()
        self.env["GIT_CONFIG_GLOBAL"] = "/dev/null"
        self.env["GIT_CONFIG_SYSTEM"] = "/dev/null"
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "T")
        self.git("config", "user.email", "t@example.com")
        (self.repo / "f.txt").write_text("base", encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "chore: base")
        self.git("checkout", "-q", "-b", "feat/7-demo")
        (self.repo / "f.txt").write_text("feat", encoding="utf-8")
        self.git("commit", "-q", "-am", "feat: demo")
        self.tip = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=self.repo, env=self.env,
            capture_output=True, text=True, check=True).stdout.strip()
        self.git("checkout", "-q", "main")
        self.git("worktree", "add", "-q", ".wt/feat-7", "feat/7-demo")
        self.wt = self.repo / ".wt" / "feat-7"

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def git(self, *args: str) -> None:
        subprocess.run(["git", *args], cwd=self.repo, env=self.env,
                       check=True, capture_output=True)

    def routes(self) -> dict:
        return {
            ("api", "repos/x/y/pulls/9"): {
                "merged": True, "state": "closed",
                "head": {"ref": "feat/7-demo", "sha": self.tip}},
            ("api", "repos/x/y/issues/7"): {"state": "closed"},
            ("api", "repos/x/y/issues?state=open&per_page=100"): [],
        }

    def run_retire(self, fake: GhFake, dry_run: bool = False,
                   patch_root: bool = True) -> tuple:
        out, err = io.StringIO(), io.StringIO()
        argv = ["--repo", "x/y"] + (["--dry-run"] if dry_run else []) \
            + ["retire", "9"]
        with mock.patch.dict(os.environ, {
                "GIT_CONFIG_GLOBAL": "/dev/null",
                "GIT_CONFIG_SYSTEM": "/dev/null"}):
            with redirect_stdout(out), redirect_stderr(err):
                with ExitStack() as stack:
                    stack.enter_context(mock.patch.object(pw, "run_gh", fake))
                    if patch_root:
                        stack.enter_context(mock.patch.object(
                            pw, "repo_root", lambda: self.repo))
                    code = pw.main(argv)
        return code, out.getvalue(), err.getvalue()

    def worktree_exists(self) -> bool:
        return subprocess.run(
            ["git", "-C", str(self.repo), "worktree", "list", "--porcelain"],
            env=self.env, capture_output=True, text=True,
            check=True).stdout.find(f"worktree {self.wt}") != -1

    def test_removes_own_worktree_and_branch(self) -> None:
        fake = GhFake(self.routes())
        code, out, _ = self.run_retire(fake)
        self.assertEqual(code, 0, out)
        self.assertFalse(self.worktree_exists())
        proc = subprocess.run(
            ["git", "-C", str(self.repo), "rev-parse", "--verify",
             "refs/heads/feat/7-demo"],
            env=self.env, capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)

    def test_notifies_dependents_and_dedupes(self) -> None:
        routes = self.routes()
        routes[("api", "repos/x/y/issues?state=open&per_page=100")] = [
            {"number": 195, "title": "next",
             "body": "Sequenced behind #7 — lands after it merges."},
        ]
        routes[("api", "repos/x/y/issues/195/comments")] = []
        code, out, _ = self.run_retire(GhFake(routes))
        self.assertEqual(code, 0, out)
        self.assertIn("195", out)
        routes[("api", "repos/x/y/issues/195/comments")] = [
            {"body": pw.UNBLOCK_TEXT.format(n=7)}]
        fake = GhFake(routes)
        self.run_retire(fake)  # second pass: notification must not re-post
        self.assertFalse(any("issues/195/comments" in " ".join(p)
                             for p in fake.posts))

    def test_finds_dependent_in_comments(self) -> None:
        routes = self.routes()
        routes[("api", "repos/x/y/issues?state=open&per_page=100")] = [
            {"number": 196, "title": "later", "body": "unrelated"},
        ]
        routes[("api", "repos/x/y/issues/196/comments")] = [
            {"body": "depends on #7"}]
        code, out, _ = self.run_retire(GhFake(routes))
        self.assertEqual(code, 0, out)
        self.assertIn("196", out)

    def test_refuses_when_not_merged(self) -> None:
        routes = self.routes()
        routes[("api", "repos/x/y/pulls/9")] = {
            "merged": False, "state": "open",
            "head": {"ref": "feat/7-demo", "sha": self.tip}}
        code, _, err = self.run_retire(GhFake(routes))
        self.assertEqual(code, 1)
        self.assertIn("not merged", err)
        self.assertTrue(self.worktree_exists())

    def test_refuses_when_issue_open(self) -> None:
        routes = self.routes()
        routes[("api", "repos/x/y/issues/7")] = {"state": "open"}
        code, _, err = self.run_retire(GhFake(routes))
        self.assertEqual(code, 1)
        self.assertIn("is open", err)

    def test_refuses_when_branch_unparseable(self) -> None:
        routes = self.routes()
        routes[("api", "repos/x/y/pulls/9")] = {
            "merged": True, "state": "closed",
            "head": {"ref": "hotfix-something", "sha": self.tip}}
        code, _, err = self.run_retire(GhFake(routes))
        self.assertEqual(code, 1)
        self.assertIn("linked issue", err)

    def test_leaves_other_branch_worktree_untouched(self) -> None:
        # rule 9: a worktree holding another session's branch is
        # unreachable by construction — retire proceeds with its own
        # branch and never touches it.
        self.git("branch", "feat/8-other")
        self.git("worktree", "remove", "--force", str(self.wt))
        self.git("worktree", "add", "-q", str(self.wt), "feat/8-other")
        code, out, _ = self.run_retire(GhFake(self.routes()))
        self.assertEqual(code, 0, out)
        self.assertTrue(self.worktree_exists())
        proc = subprocess.run(
            ["git", "-C", str(self.repo), "worktree", "list", "--porcelain"],
            env=self.env, capture_output=True, text=True, check=True)
        self.assertIn(f"worktree {self.wt}\n", proc.stdout)
        self.assertIn("branch refs/heads/feat/8-other", proc.stdout)

    def test_refuses_when_dirty(self) -> None:
        (self.wt / "untracked.txt").write_text("x", encoding="utf-8")
        code, _, err = self.run_retire(GhFake(self.routes()))
        self.assertEqual(code, 1)
        self.assertIn("dirty", err)
        self.assertTrue(self.worktree_exists())

    def test_refuses_when_local_branch_ahead(self) -> None:
        (self.wt / "f.txt").write_text("more", encoding="utf-8")
        subprocess.run(["git", "-C", str(self.wt), "commit", "-q", "-am",
                        "feat: extra"], env=self.env, check=True,
                       capture_output=True)
        code, _, err = self.run_retire(GhFake(self.routes()))
        self.assertEqual(code, 1)
        self.assertIn("not at the merged head", err)

    def test_no_worktree_still_deletes_branch(self) -> None:
        self.git("worktree", "remove", str(self.wt))
        code, out, _ = self.run_retire(GhFake(self.routes()))
        self.assertEqual(code, 0, out)
        self.assertNotIn("worktree_removed", out)

    def test_dry_run_removes_nothing(self) -> None:
        fake = GhFake(self.routes())
        code, out, _ = self.run_retire(fake, dry_run=True)
        self.assertEqual(code, 0, out)
        self.assertIn('"dry_run": true', out)
        self.assertTrue(self.worktree_exists())
        self.assertEqual(fake.posts, [])

    def test_repo_root_resolves_main_worktree_from_linked(self) -> None:
        # #199 review: --show-toplevel returns the linked worktree itself;
        # the main worktree must come from porcelain's first entry.
        cwd = Path.cwd()
        try:
            os.chdir(self.wt)
            self.assertEqual(pw.repo_root(), self.repo)
        finally:
            os.chdir(cwd)

    def test_retire_run_from_inside_its_own_worktree(self) -> None:
        # retire is typically invoked from the worktree it deletes —
        # repo_root must anchor every git op at the main worktree.
        cwd = Path.cwd()
        try:
            os.chdir(self.wt)
            code, out, err = self.run_retire(GhFake(self.routes()),
                                             patch_root=False)
        finally:
            os.chdir(cwd)
        self.assertEqual(code, 0, err)
        self.assertFalse(self.worktree_exists())


class RetirePureTests(unittest.TestCase):
    def test_issue_number_for_branch(self) -> None:
        self.assertEqual(pw.issue_number_for_branch("feat/7-demo"), 7)
        self.assertEqual(
            pw.issue_number_for_branch("ci/194-ai-commit-trailer-pair"), 194)
        self.assertEqual(
            pw.issue_number_for_branch("feat/151-artifact-installer"), 151)

    def test_issue_number_rejects_non_conforming(self) -> None:
        self.assertIsNone(pw.issue_number_for_branch("hotfix-something"))
        self.assertIsNone(pw.issue_number_for_branch("feat/x-demo"))
        self.assertIsNone(pw.issue_number_for_branch("main"))


if __name__ == "__main__":
    unittest.main()
