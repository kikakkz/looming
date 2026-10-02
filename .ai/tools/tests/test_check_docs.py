#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_docs.py against throwaway repo trees."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_docs as cd

MAKEFILE = """\
.PHONY: ci-gate check-adr lint-sh
ci-gate: check-adr lint-sh
check-adr:
\tpython3 .ai/tools/adr_manager.py check
lint-sh:
\tshellcheck .ai/tools/*.sh
"""

AGENTS = """\
# AGENTS

## Commands

- `make check-adr` — validate decision records.
- `make lint-sh` — shell lint.
"""

README = """\
# readme

2 self-authored skills live here.
"""


class CheckDocsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp()) / "repo"
        (self.root / ".ai" / "skills" / "one").mkdir(parents=True)
        (self.root / ".ai" / "skills" / "two").mkdir(parents=True)
        (self.root / ".ai" / "memory" / "decisions").mkdir(parents=True)
        (self.root / ".ai" / "memory" / "decisions.md").write_text(
            "stub", encoding="utf-8")
        (self.root / "Makefile").write_text(MAKEFILE, encoding="utf-8")
        (self.root / "AGENTS.md").write_text(AGENTS, encoding="utf-8")
        (self.root / "README.md").write_text(README, encoding="utf-8")

    def tearDown(self) -> None:
        shutil.rmtree(self.root.parent, ignore_errors=True)

    def problems(self) -> list[str]:
        return cd.check(self.root)

    def test_clean_tree_passes(self) -> None:
        self.assertEqual(self.problems(), [])

    def test_documented_target_missing_from_makefile(self) -> None:
        text = (self.root / "AGENTS.md").read_text(encoding="utf-8")
        (self.root / "AGENTS.md").write_text(text + "- `make ghost` — nope\n",
                                             encoding="utf-8")
        self.assertTrue(any("ghost" in p for p in self.problems()))

    def test_gate_prereq_not_documented(self) -> None:
        text = (self.root / "AGENTS.md").read_text(encoding="utf-8")
        (self.root / "AGENTS.md").write_text(
            text.replace("`make lint-sh`", "`make sh-lint`"), encoding="utf-8")
        self.assertTrue(any("lint-sh" in p for p in self.problems()))

    def test_skill_count_claim_mismatch(self) -> None:
        (self.root / "README.md").write_text(
            "9 self-authored skills live here.\n", encoding="utf-8")
        self.assertTrue(any("claims" in p for p in self.problems()))

    def test_wikilink_resolution(self) -> None:
        (self.root / ".ai" / "memory" / "progress.md").write_text(
            "see [[activeContext]] and [[AD-9]]\n", encoding="utf-8")
        self.assertTrue(any("[[activeContext]]" in p for p in self.problems()))
        self.assertTrue(any("[[AD-9]]" in p for p in self.problems()))
        (self.root / ".ai" / "memory" / "decisions" / "0009-x.md").write_text(
            "x", encoding="utf-8")
        (self.root / ".ai" / "memory" / "activeContext.md").write_text(
            "x", encoding="utf-8")
        problems = [p for p in self.problems() if "progress.md" in p]
        self.assertEqual(problems, [])

    def test_main_cli(self) -> None:
        (self.root / "README.md").write_text(
            "3 skills\n", encoding="utf-8")
        self.assertEqual(cd.main(["--root", str(self.root)]), 1)


if __name__ == "__main__":
    unittest.main()
