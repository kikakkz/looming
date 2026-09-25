#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for memory_housekeeping.py against a throwaway memory root."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import memory_housekeeping as mh


PROGRESS_TEMPLATE = """---
updated: {date}
type: progress
---

# Progress

## {old} — Old work

- landed #3 and referenced #7 in discussion

## {new} — Current work

- working on #7 and #9 today
"""


class HousekeepingCase(unittest.TestCase):
    def setUp(self) -> None:
        self._orig = mh.MEMORY, mh.PROGRESS, mh.ACTIVE, mh.ARCHIVE_DIR
        self.tmp = Path(tempfile.mkdtemp())
        mh.MEMORY = self.tmp
        mh.PROGRESS = self.tmp / "progress.md"
        mh.ACTIVE = self.tmp / "activeContext.md"
        mh.ARCHIVE_DIR = self.tmp / "progress"

    def tearDown(self) -> None:
        mh.MEMORY, mh.PROGRESS, mh.ACTIVE, mh.ARCHIVE_DIR = self._orig
        shutil.rmtree(self.tmp, ignore_errors=True)

    def write_progress(self, old: str, new: str) -> None:
        mh.PROGRESS.write_text(
            PROGRESS_TEMPLATE.format(date=new, old=old, new=new),
            encoding="utf-8")


class MemoryHousekeepingTests(HousekeepingCase):
    def test_rotate_archives_old_month_only(self) -> None:
        self.write_progress("2026-08-15", "2026-09-25")
        self.assertEqual(mh.main(["rotate"]), 0)
        archive = mh.ARCHIVE_DIR / "2026-08.md"
        self.assertTrue(archive.exists())
        self.assertIn("Old work", archive.read_text(encoding="utf-8"))
        kept = mh.PROGRESS.read_text(encoding="utf-8")
        self.assertIn("Current work", kept)
        self.assertNotIn("Old work", kept)

    def test_carried_over_generated_from_refs(self) -> None:
        self.write_progress("2026-08-15", "2026-09-25")
        mh.ACTIVE.write_text("focus on #12 and #7\n", encoding="utf-8")
        self.assertEqual(mh.main(["rotate"]), 0)
        kept = mh.PROGRESS.read_text(encoding="utf-8")
        self.assertIn("Carried over", kept)
        # refs from rotated entries + activeContext, deduplicated
        for ref in ("- #3", "- #7", "- #12"):
            self.assertIn(ref, kept)
        self.assertEqual(kept.count("- #7"), 1)  # deduplicated
        # current-month refs stay in place; they are not "carried"
        carried = kept.split("## ")[1]
        self.assertNotIn("- #9", carried)

    def test_rotate_refuses_existing_archive(self) -> None:
        self.write_progress("2026-08-15", "2026-09-25")
        mh.ARCHIVE_DIR.mkdir(parents=True)
        (mh.ARCHIVE_DIR / "2026-08.md").write_text("exists\n",
                                                   encoding="utf-8")
        self.assertEqual(mh.main(["rotate"]), 1)
        self.assertIn("Old work", mh.PROGRESS.read_text(encoding="utf-8"))

    def test_rotate_noop_within_current_month(self) -> None:
        self.write_progress("2026-09-01", "2026-09-25")
        self.assertEqual(mh.main(["rotate"]), 0)
        self.assertFalse((mh.ARCHIVE_DIR / "2026-09.md").exists())

    def test_check_strict_fails_over_threshold(self) -> None:
        mh.PROGRESS.write_text("x\n" * (mh.PROGRESS_SOFT_LINES + 1),
                               encoding="utf-8")
        mh.ACTIVE.write_text("ok\n", encoding="utf-8")
        self.assertEqual(mh.main(["check", "--strict"]), 1)
        self.assertEqual(mh.main(["check"]), 0)  # advisory by default

    def test_check_passes_under_threshold(self) -> None:
        mh.PROGRESS.write_text("small\n", encoding="utf-8")
        mh.ACTIVE.write_text("small\n", encoding="utf-8")
        self.assertEqual(mh.main(["check", "--strict"]), 0)


if __name__ == "__main__":
    unittest.main()
