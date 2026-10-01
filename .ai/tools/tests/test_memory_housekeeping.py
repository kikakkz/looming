#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for memory_housekeeping.py against a throwaway memory root."""

import shutil
import sys
import tempfile
import unittest
from datetime import date, timedelta
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import memory_housekeeping as mh

TODAY = date.today()


def first_of_month(d: date) -> date:
    return d.replace(day=1)


def prev_month(d: date) -> date:
    """First day of the month before d's month."""
    return first_of_month(first_of_month(d) - timedelta(days=1))


def ym(d: date) -> str:
    return d.strftime("%Y-%m")


# Fixture dates must be derived from the real clock: rotation keys off the
# actual current month, so hardcoded dates break on every month rollover.


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

    def rotate(self) -> int:
        """Run rotate with the tool's clock frozen to the fixture date.

        Without the freeze, a test process alive across a month boundary
        would see the tool compute a different \"current month\" than the
        fixture data assumes.
        """
        with patch.object(mh, "today", return_value=TODAY.isoformat()):
            return mh.main(["rotate"])

    def tearDown(self) -> None:
        mh.MEMORY, mh.PROGRESS, mh.ACTIVE, mh.ARCHIVE_DIR = self._orig
        shutil.rmtree(self.tmp, ignore_errors=True)

    def write_progress(self, old: str, new: str) -> None:
        mh.PROGRESS.write_text(
            PROGRESS_TEMPLATE.format(date=new, old=old, new=new),
            encoding="utf-8")


class MemoryHousekeepingTests(HousekeepingCase):
    def test_rotate_archives_old_month_only(self) -> None:
        old = prev_month(TODAY).replace(day=15)
        self.write_progress(old.isoformat(), TODAY.isoformat())
        self.assertEqual(self.rotate(), 0)
        archive = mh.ARCHIVE_DIR / f"{ym(old)}.md"
        self.assertTrue(archive.exists())
        archived = archive.read_text(encoding="utf-8")
        self.assertIn("Old work", archived)
        self.assertTrue(archived.startswith("---\n"),
                        "archives carry the memory-bank frontmatter")
        kept = mh.PROGRESS.read_text(encoding="utf-8")
        self.assertIn("Current work", kept)
        self.assertNotIn("Old work", kept)

    def test_carried_over_generated_from_refs(self) -> None:
        old = prev_month(TODAY).replace(day=15)
        self.write_progress(old.isoformat(), TODAY.isoformat())
        mh.ACTIVE.write_text("focus on #12 and #7\n", encoding="utf-8")
        self.assertEqual(self.rotate(), 0)
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
        old = prev_month(TODAY).replace(day=15)
        self.write_progress(old.isoformat(), TODAY.isoformat())
        mh.ARCHIVE_DIR.mkdir(parents=True)
        (mh.ARCHIVE_DIR / f"{ym(old)}.md").write_text("exists\n",
                                                     encoding="utf-8")
        self.assertEqual(self.rotate(), 1)
        self.assertIn("Old work", mh.PROGRESS.read_text(encoding="utf-8"))

    def test_rotate_noop_within_current_month(self) -> None:
        self.write_progress(first_of_month(TODAY).isoformat(),
                            TODAY.isoformat())
        self.assertEqual(self.rotate(), 0)
        for month in (prev_month(TODAY), TODAY):
            self.assertFalse((mh.ARCHIVE_DIR / f"{ym(month)}.md")
                             .exists())

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
