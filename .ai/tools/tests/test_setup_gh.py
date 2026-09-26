#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for setup_gh.sh."""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "setup_gh.sh"
BASH = shutil.which("bash") or "/bin/bash"


def run_script(args, env=None):
    full_env = dict(os.environ)
    full_env.update(env or {})
    return subprocess.run(
        [BASH, str(SCRIPT), *args],
        capture_output=True, text=True, env=full_env,
    )


class UsageTests(unittest.TestCase):
    def test_unknown_subcommand_fails(self):
        r = run_script(["frobnicate"])
        self.assertEqual(r.returncode, 1)
        self.assertIn("usage", r.stderr)

    def test_check_fails_without_gh(self):
        with tempfile.TemporaryDirectory() as empty:
            r = run_script(["check"], env={"PATH": empty})
        self.assertEqual(r.returncode, 1)
        self.assertNotIn("gh ready", r.stdout)


class InstallGateTests(unittest.TestCase):
    def _run_install(self, tmp, **env):
        uname = Path(tmp) / "uname"
        uname.write_text(
            "#!/bin/sh\n"
            "case \"$1\" in\n"
            "  -s) echo \"${MOCK_UNAME_S:-Linux}\" ;;\n"
            "  -m) echo \"${MOCK_UNAME_M:-x86_64}\" ;;\n"
            "esac\n"
        )
        uname.chmod(0o755)
        full = {"PATH": f"{tmp}:/usr/bin:/bin", "HOME": tmp}
        full.update(env)
        return run_script(["install"], env=full)

    def test_rejects_non_linux(self):
        with tempfile.TemporaryDirectory() as tmp:
            r = self._run_install(tmp, MOCK_UNAME_S="Darwin")
            self.assertFalse((Path(tmp) / ".local" / "opt").exists())
        self.assertEqual(r.returncode, 1)
        self.assertIn("Linux binaries only", r.stderr)

    def test_rejects_unsupported_arch(self):
        with tempfile.TemporaryDirectory() as tmp:
            r = self._run_install(tmp, MOCK_UNAME_M="riscv64")
        self.assertEqual(r.returncode, 1)
        self.assertIn("unsupported architecture", r.stderr)


class ConfigDirTests(unittest.TestCase):
    def _config_dir(self, env):
        code = f'source "{SCRIPT}"; config_dir'
        full = {"PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        full.update(env)
        r = subprocess.run(["bash", "-c", code],
                           capture_output=True, text=True, env=full)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout.strip()

    def test_gh_config_dir_wins(self):
        self.assertEqual(
            self._config_dir({"GH_CONFIG_DIR": "/tmp/a",
                              "XDG_CONFIG_HOME": "/tmp/b"}),
            "/tmp/a")

    def test_xdg_fallback(self):
        self.assertEqual(
            self._config_dir({"GH_CONFIG_DIR": "",
                              "XDG_CONFIG_HOME": "/tmp/x"}),
            "/tmp/x/gh")

    def test_home_fallback(self):
        self.assertEqual(
            self._config_dir({"GH_CONFIG_DIR": "",
                              "XDG_CONFIG_HOME": "",
                              "HOME": "/home/t"}),
            "/home/t/.config/gh")


if __name__ == "__main__":
    unittest.main()
