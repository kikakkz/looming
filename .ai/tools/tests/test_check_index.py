#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_index.py against throwaway index trees."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_index as ci

GOOD = """\
# comment line is ignored
read_order:
  - AGENTS.md
  - .ai/AGENTS.md

read_on_demand:
  skills: .ai/skills/
  tools: .ai/tools/

authoritative_sources:
  tracker: https://example.invalid/issues
"""


class CheckIndexTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.root = self.tmp / "repo"
        (self.root / ".ai").mkdir(parents=True)
        self.index = self.root / ".ai" / "index.yaml"

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def write(self, text: str) -> None:
        self.index.write_text(text, encoding="utf-8")

    def mk(self, rel: str, is_dir: bool = False) -> None:
        path = self.root / rel.rstrip("/")
        if is_dir or rel.endswith("/"):
            path.mkdir(parents=True, exist_ok=True)
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("x", encoding="utf-8")

    def problems(self) -> list[str]:
        return ci.check(self.root, self.index)

    def test_good_index_passes(self) -> None:
        self.write(GOOD)
        self.mk("AGENTS.md")
        self.mk(".ai/AGENTS.md")
        self.mk(".ai/skills/", is_dir=True)
        self.mk(".ai/tools/", is_dir=True)
        self.assertEqual(self.problems(), [])

    def test_missing_anchor_reported(self) -> None:
        self.write(GOOD)
        self.mk("AGENTS.md")  # .ai/AGENTS.md left missing
        found = self.problems()
        self.assertTrue(any(".ai/AGENTS.md" in p for p in found))

    def test_unknown_section_rejected(self) -> None:
        self.write(GOOD + "\nleftover:\n  - surprise\n")
        self.mk("AGENTS.md")
        self.mk(".ai/AGENTS.md")
        self.mk(".ai/skills/", is_dir=True)
        self.mk(".ai/tools/", is_dir=True)
        self.assertTrue(any("unknown top-level" in p for p in self.problems()))

    def test_mixed_list_and_map_rejected(self) -> None:
        self.write("read_order:\n  - a\n  b: c\n")
        self.assertTrue(self.problems())

    def test_entry_before_section_rejected(self) -> None:
        self.write("  - orphan\nread_order:\n  - a\n")
        self.assertTrue(self.problems())

    def test_directory_anchor_requires_dir(self) -> None:
        self.write("read_order:\n  - docs/\n")
        self.mk("docs")  # file, not directory
        self.assertTrue(any("docs/" in p for p in self.problems()))

    def test_tab_indentation_rejected(self) -> None:
        self.write("read_order:\n\t- a\n")
        self.assertTrue(any("two spaces" in p for p in self.problems()))

    def test_deep_indentation_rejected(self) -> None:
        self.write("read_on_demand:\n    skills: .ai/skills/\n")
        self.assertTrue(any("two spaces" in p for p in self.problems()))

    def test_duplicate_subsection_key_rejected(self) -> None:
        self.write("read_on_demand:\n  skills: one.md\n  skills: two.md\n")
        self.assertTrue(any("duplicate key" in p for p in self.problems()))

    def test_required_rules_shape_enforced(self) -> None:
        self.write("required_rules:\n  - .ai/skills/missing/SKILL.md\n")
        self.assertTrue(any("required_rules" in p for p in self.problems()))

    def test_main_cli(self) -> None:
        self.write(GOOD)
        self.mk("AGENTS.md")
        self.mk(".ai/AGENTS.md")
        self.mk(".ai/skills/", is_dir=True)
        self.mk(".ai/tools/", is_dir=True)
        self.assertEqual(ci.main(["--root", str(self.root)]), 0)
        self.assertEqual(ci.main(
            ["--root", str(self.root),
             "--index", str(self.root / "absent.yaml")]), 1)


if __name__ == "__main__":
    unittest.main()
