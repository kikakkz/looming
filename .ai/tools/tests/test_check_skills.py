#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_skills.py against throwaway skill trees."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_skills as cs


GOOD = """---
name: {name}
description: Does the thing. Use when unsure.
---

# {name}

Body.
"""


class CheckSkillsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp()) / "skills"

    def tearDown(self) -> None:
        shutil.rmtree(self.root.parent, ignore_errors=True)

    def mk(self, name: str, text: str) -> None:
        d = self.root / name
        d.mkdir(parents=True, exist_ok=True)
        (d / "SKILL.md").write_text(text, encoding="utf-8")

    def run_check(self) -> int:
        return cs.main(["--root", str(self.root)])

    def test_good_tree_passes(self) -> None:
        self.mk("alpha", GOOD.format(name="alpha"))
        self.mk("beta", GOOD.format(name="beta"))
        self.assertEqual(self.run_check(), 0)

    def test_missing_skill_md(self) -> None:
        (self.root / "ghost").mkdir(parents=True)
        self.assertEqual(self.run_check(), 1)

    def test_missing_frontmatter(self) -> None:
        self.mk("raw", "# no frontmatter\n")
        self.assertEqual(self.run_check(), 1)

    def test_missing_description(self) -> None:
        self.mk("nodesc", "---\nname: nodesc\n---\n\n# x\n")
        self.assertEqual(self.run_check(), 1)

    def test_name_mismatch(self) -> None:
        self.mk("dirname", GOOD.format(name="other"))
        self.assertEqual(self.run_check(), 1)

    def test_empty_body(self) -> None:
        self.mk("empty", "---\nname: empty\ndescription: d\n---\n\n")
        self.assertEqual(self.run_check(), 1)

    def test_embedded_fence_line_is_not_the_closer(self) -> None:
        # a --- separator inside the prose body must not end the
        # frontmatter early; the real closing fence is the LAST one
        self.mk("sep", "---\nname: sep\ndescription: d\n---\n\n# sep\n\n"
                        "---\nnot: frontmatter\n\n")
        self.assertEqual(self.run_check(), 0)

    def test_closing_fence_at_eof_without_newline(self) -> None:
        self.mk("eof", "---\nname: eof\ndescription: d\n---")
        self.assertEqual(self.run_check(), 1)  # no body after the fence

    def test_missing_root_is_error(self) -> None:
        self.assertEqual(
            cs.main(["--root", str(self.root / "absent")]), 1)


if __name__ == "__main__":
    unittest.main()
