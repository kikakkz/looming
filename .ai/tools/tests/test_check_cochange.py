#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_cochange.py — git diff is mocked."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_cochange as cc

INDEX = """\
read_order:
  - AGENTS.md
  - .ai/AGENTS.md
  - .ai/memory/activeContext.md

read_on_demand:
  progress: .ai/memory/progress.md
  decisions: .ai/memory/decisions/
  skills: .ai/skills/
  tracker: https://example.com/issues
"""


class CochangeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp()) / "repo"
        (self.root / ".ai" / "memory").mkdir(parents=True)
        (self.root / ".ai" / "index.yaml").write_text(INDEX,
                                                      encoding="utf-8")

    def tearDown(self) -> None:
        shutil.rmtree(self.root.parent, ignore_errors=True)

    def run_check(self, changed: list[str]) -> list[str]:
        with mock.patch.object(cc, "changed_paths", return_value=set(changed)):
            return cc.check(self.root, "base")

    def test_anchors_parsed_excluding_dirs_and_urls(self) -> None:
        anchors = cc.index_file_anchors(self.root)
        self.assertIn("AGENTS.md", anchors)
        self.assertIn(".ai/memory/progress.md", anchors)
        self.assertNotIn(".ai/memory/decisions/", anchors)
        self.assertNotIn("https://example.com/issues", anchors)

    def test_anchor_change_without_index_update_fails(self) -> None:
        problems = self.run_check([".ai/memory/progress.md"])
        self.assertEqual(len(problems), 1)
        self.assertIn("progress.md", problems[0])

    def test_anchor_change_with_index_update_passes(self) -> None:
        self.assertEqual(self.run_check(
            [".ai/memory/progress.md", ".ai/index.yaml"]), [])

    def test_unrelated_change_passes(self) -> None:
        self.assertEqual(self.run_check(["README.md"]), [])

    def test_directory_anchor_does_not_tripwire(self) -> None:
        # skills/ is a directory anchor: content churn is expected and
        # must not force an index edit
        self.assertEqual(self.run_check([".ai/skills/new/SKILL.md"]), [])

    def test_no_base_skips(self) -> None:
        self.assertEqual(cc.main(["--root", str(self.root)]), 0)


if __name__ == "__main__":
    unittest.main()
