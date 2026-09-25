#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for validate-locks.py."""

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import validate_locks


class CheckLockTests(unittest.TestCase):
    def _write(self, text: str) -> Path:
        tmp = Path(tempfile.mkdtemp())
        lock = tmp / "skills.lock.toml"
        lock.write_text(text, encoding="utf-8")
        return lock

    def test_valid_entries_pass(self) -> None:
        lock = self._write(
            '[[skill]]\n'
            'name = "x"\n'
            'source = "github.com/org/repo"\n'
            'revision = "0123456789abcdef0123456789abcdef01234567"\n'
            'license = "MIT"\n'
        )
        self.assertEqual(validate_locks.check_lock(lock, "skill"), 0)

    def test_bad_sha_fails(self) -> None:
        lock = self._write(
            '[[skill]]\n'
            'name = "x"\n'
            'source = "github.com/org/repo"\n'
            'revision = "notasha"\n'
            'license = "MIT"\n'
        )
        self.assertEqual(validate_locks.check_lock(lock, "skill"), 1)

    def test_bad_license_fails(self) -> None:
        lock = self._write(
            '[[skill]]\n'
            'name = "x"\n'
            'source = "github.com/org/repo"\n'
            'revision = "0123456789abcdef0123456789abcdef01234567"\n'
            'license = "Proprietary"\n'
        )
        self.assertEqual(validate_locks.check_lock(lock, "skill"), 1)

    def test_bad_source_fails(self) -> None:
        lock = self._write(
            '[[skill]]\n'
            'name = "x"\n'
            'source = "gitlab.com/org/repo"\n'
            'revision = "0123456789abcdef0123456789abcdef01234567"\n'
            'license = "MIT"\n'
        )
        self.assertEqual(validate_locks.check_lock(lock, "skill"), 1)

    def test_missing_file_fails(self) -> None:
        missing = Path(tempfile.mkdtemp()) / "nope.lock.toml"
        self.assertEqual(validate_locks.check_lock(missing, "skill"), 1)


if __name__ == "__main__":
    unittest.main()
