#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for adr_manager.py against a throwaway decisions directory."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import adr_manager as adr


class AdrCase(unittest.TestCase):
    def setUp(self) -> None:
        self._orig_dir, self._orig_index = adr.ADR_DIR, adr.INDEX
        self.tmp = Path(tempfile.mkdtemp())
        adr.ADR_DIR = self.tmp / "decisions"
        adr.INDEX = adr.ADR_DIR / "README.md"

    def tearDown(self) -> None:
        adr.ADR_DIR, adr.INDEX = self._orig_dir, self._orig_index
        shutil.rmtree(self.tmp, ignore_errors=True)

    def check(self) -> int:
        return adr.main(["check"])


class AdrManagerTests(AdrCase):
    def test_new_creates_file_and_index(self) -> None:
        self.assertEqual(adr.main(["new", "--title", "First decision"]), 0)
        self.assertTrue((adr.ADR_DIR / "0001-first-decision.md").exists())
        self.assertIn("AD-1 — First decision", adr.INDEX.read_text(encoding="utf-8"))
        self.assertEqual(self.check(), 0)

    def test_supersede_is_bidirectional(self) -> None:
        adr.main(["new", "--title", "Old way"])
        adr.main(["new", "--title", "New way", "--supersedes", "1"])
        old = adr.ADR_DIR.joinpath("0001-old-way.md").read_text(encoding="utf-8")
        new = adr.ADR_DIR.joinpath("0002-new-way.md").read_text(encoding="utf-8")
        self.assertIn('status: "superseded"', old)
        self.assertIn("superseded-by: 2", old)
        self.assertIn("supersedes: [1]", new)
        self.assertEqual(self.check(), 0)

    def test_check_detects_missing_reciprocal_link(self) -> None:
        adr.main(["new", "--title", "A"])
        adr.main(["new", "--title", "B"])
        path_b = adr.ADR_DIR / "0002-b.md"
        path_b.write_text(path_b.read_text(encoding="utf-8")
                          .replace("supersedes: []", "supersedes: [1]"),
                          encoding="utf-8")
        self.assertEqual(self.check(), 1)

    def test_check_detects_stale_index(self) -> None:
        adr.main(["new", "--title", "A"])
        adr.INDEX.write_text("hand-edited\n", encoding="utf-8")
        self.assertEqual(self.check(), 1)

    def test_import_legacy_splits_and_links(self) -> None:
        legacy = self.tmp / "decisions.md"
        legacy.write_text(
            "# Decisions\n\n"
            "## AD-1 — 2026-01-01 — accepted\n\n"
            "Monorepo with hybrid versioning: details here.\n\n"
            "## AD-2 — 2026-01-02 — accepted\n\n"
            "Single bundle artifact: details here.\n",
            encoding="utf-8")
        self.assertEqual(adr.main([
            "import-legacy", str(legacy), "--supersedes-map", '{"2": [1]}'
        ]), 0)
        one = adr.ADR_DIR.joinpath("0001-monorepo-with-hybrid-versioning.md")
        two = adr.ADR_DIR.joinpath("0002-single-bundle-artifact.md")
        self.assertTrue(one.exists())
        self.assertIn("superseded-by: 2", one.read_text(encoding="utf-8"))
        self.assertIn("supersedes: [1]", two.read_text(encoding="utf-8"))
        self.assertEqual(self.check(), 0)

    def test_import_refuses_nonempty_directory(self) -> None:
        adr.main(["new", "--title", "Existing"])
        legacy = self.tmp / "decisions.md"
        legacy.write_text("## AD-1 — 2026-01-01 — accepted\n\nx\n",
                          encoding="utf-8")
        self.assertEqual(adr.main(["import-legacy", str(legacy)]), 1)


if __name__ == "__main__":
    unittest.main()
