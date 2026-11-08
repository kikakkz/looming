#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for create_pr.py — gh is mocked at the run_gh boundary."""

import io
import json
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import create_pr as cp


class GhFake:
    """Routes gh argv to canned results; records every call."""

    def __init__(self, create_url: str | None = None,
                 merge_error: str | None = None):
        self.create_url = create_url
        self.merge_error = merge_error
        self.calls: list[list[str]] = []

    def __call__(self, args: list[str], gh: str = "gh") -> str:
        self.calls.append(list(args))
        if args[:2] == ["pr", "create"]:
            if self.create_url is None:
                raise RuntimeError("gh pr create: boom")
            return self.create_url + "\n"
        if args[:2] == ["pr", "merge"]:
            if self.merge_error is not None:
                raise RuntimeError(f"gh pr merge: {self.merge_error}")
            return ""
        raise RuntimeError(f"unexpected gh call: {args}")


def run_tool(argv: list[str]) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = cp.main(argv)
    return code, out.getvalue(), err.getvalue()


class CreatePrTests(unittest.TestCase):
    def test_creates_then_arms_auto_merge(self) -> None:
        fake = GhFake(create_url="https://github.com/x/y/pull/42")
        with mock.patch.object(cp, "run_gh", fake):
            code, out, _ = run_tool(["--title", "t", "--body", "b"])
        self.assertEqual(code, 0)
        result = json.loads(out)
        self.assertEqual(result["pr"], 42)
        self.assertTrue(result["auto_merge"])
        create, merge = fake.calls
        self.assertEqual(create[:2], ["pr", "create"])
        self.assertIn("--title", create)
        self.assertIn("t", create)
        self.assertEqual(merge[:5],
                         ["pr", "merge", "42", "--squash", "--auto"])

    def test_create_failure_never_arms(self) -> None:
        fake = GhFake(create_url=None)
        with mock.patch.object(cp, "run_gh", fake):
            code, _, err = run_tool(["--title", "t", "--body", "b"])
        self.assertEqual(code, 1)
        self.assertIn("boom", err)
        self.assertEqual(len(fake.calls), 1)  # merge never attempted

    def test_arm_failure_is_loud_partial_success(self) -> None:
        fake = GhFake(create_url="https://github.com/x/y/pull/43",
                      merge_error="branch protection blocks auto-merge")
        with mock.patch.object(cp, "run_gh", fake):
            code, _, err = run_tool(["--title", "t", "--body", "b"])
        self.assertEqual(code, 1)
        self.assertIn("NOT armed", err)
        self.assertIn("https://github.com/x/y/pull/43", err)

    def test_body_file_is_used(self) -> None:
        fake = GhFake(create_url="https://github.com/x/y/pull/44")
        with tempfile.TemporaryDirectory() as tmp:
            body = Path(tmp) / "body.md"
            body.write_text("Closes #203\n", encoding="utf-8")
            with mock.patch.object(cp, "run_gh", fake):
                code, _, _ = run_tool(["--title", "t",
                                       "--body-file", str(body)])
        self.assertEqual(code, 0)
        create = fake.calls[0]
        self.assertEqual(create[create.index("--body") + 1],
                         "Closes #203\n")

    def test_base_and_repo_pass_through(self) -> None:
        fake = GhFake(create_url="https://github.com/x/y/pull/45")
        with mock.patch.object(cp, "run_gh", fake):
            run_tool(["--title", "t", "--body", "b",
                      "--base", "main", "--repo", "x/y"])
        create = fake.calls[0]
        self.assertIn("--base", create)
        self.assertIn("main", create)
        self.assertIn("--repo", create)
        self.assertIn("x/y", create)


class PureTests(unittest.TestCase):
    def test_number_from_url_variants(self) -> None:
        self.assertEqual(
            cp.pr_number_from_url("https://github.com/x/y/pull/42"), "42")
        self.assertEqual(
            cp.pr_number_from_url("https://github.com/x/y/pull/42/"), "42")

    def test_run_gh_wraps_subprocess_failures(self) -> None:
        with mock.patch("subprocess.run",
                        side_effect=FileNotFoundError("gh")):
            with self.assertRaises(RuntimeError):
                cp.run_gh(["pr", "list"])
        proc = subprocess.CompletedProcess(["gh"], 1, "", "denied")
        with mock.patch("subprocess.run", return_value=proc):
            with self.assertRaises(RuntimeError):
                cp.run_gh(["pr", "list"])


if __name__ == "__main__":
    unittest.main()
