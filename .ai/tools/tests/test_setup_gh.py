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


def run_script(args, env=None, cwd=None):
    full_env = dict(os.environ)
    # a developer or CI job exporting these must not leak into the tests
    full_env.pop("GH_TOKEN", None)
    full_env.pop("GITHUB_TOKEN", None)
    for key, value in (env or {}).items():
        if value is None:
            full_env.pop(key, None)
        else:
            full_env[key] = value
    return subprocess.run(
        [BASH, str(SCRIPT), *args],
        capture_output=True, text=True, env=full_env, cwd=cwd, timeout=60,
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


class InstallSuccessTests(unittest.TestCase):
    """Stubbed happy path: no network, no host credentials."""

    def _write(self, d, name, body):
        p = Path(d) / name
        p.write_text(body)
        p.chmod(0o755)
        return p

    def test_install_success_with_stubs(self):
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir = Path(tmp) / "bin"
            bin_dir.mkdir()
            self._write(bin_dir, "uname",
                        '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
            self._write(bin_dir, "curl",
                        '#!/bin/sh\nout=""\n'
                        'while [ $# -gt 0 ]; do\n'
                        '  case "$1" in -o) out="$2"; shift 2 ;; *) shift ;; esac\n'
                        'done\n'
                        '[ -n "$out" ] && printf fake >"$out"\n')
            self._write(bin_dir, "sha256sum", '#!/bin/sh\ncat >/dev/null\nexit 0\n')
            self._write(bin_dir, "tar",
                        '#!/bin/sh\n'
                        'dest=""\n'
                        'while [ $# -gt 0 ]; do\n'
                        '  case "$1" in -C) dest="$2"; shift 2 ;; *) shift ;; esac\n'
                        'done\n'
                        'mkdir -p "$dest/gh_2.101.0_linux_amd64/bin"\n'
                        'printf "#!/bin/sh\\n" >"$dest/gh_2.101.0_linux_amd64/bin/gh"\n'
                        'chmod +x "$dest/gh_2.101.0_linux_amd64/bin/gh"\n')
            local_bin = Path(tmp) / ".local" / "bin"
            env = {"PATH": f"{bin_dir}:{local_bin}:/usr/bin:/bin", "HOME": tmp}
            r = run_script(["install"], env=env)
            self.assertEqual(r.returncode, 0, r.stderr)
            installed = Path(tmp) / ".local" / "opt" / "gh_2.101.0_linux_amd64" / "bin" / "gh"
            self.assertTrue(os.access(installed, os.X_OK))
            self.assertEqual(os.readlink(Path(tmp) / ".local" / "bin" / "gh"),
                             str(installed))

    def test_install_aborts_on_checksum_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir = Path(tmp) / "bin"
            bin_dir.mkdir()
            self._write(bin_dir, "uname",
                        '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
            self._write(bin_dir, "curl",
                        '#!/bin/sh\nout=""\n'
                        'while [ $# -gt 0 ]; do\n'
                        '  case "$1" in -o) out="$2"; shift 2 ;; *) shift ;; esac\n'
                        'done\n'
                        '[ -n "$out" ] && printf fake >"$out"\n')
            self._write(bin_dir, "sha256sum", '#!/bin/sh\ncat >/dev/null\nexit 1\n')
            tar_marker = Path(tmp) / "tar-called"
            self._write(bin_dir, "tar", f'#!/bin/sh\ntouch "{tar_marker}"\n')
            existing = Path(tmp) / ".local" / "opt" / "existing" / "bin"
            existing.mkdir(parents=True)
            (existing / "gh").write_text("#!/bin/sh\n")
            env = {"PATH": f"{bin_dir}:/usr/bin:/bin", "HOME": tmp}
            r = run_script(["install"], env=env)
            self.assertEqual(r.returncode, 1)
            self.assertIn("checksum mismatch", r.stderr)
            self.assertFalse(tar_marker.exists(), "tar ran despite checksum failure")
            self.assertTrue((existing / "gh").exists(),
                            "existing installation was touched")


class AuthBootstrapTests(unittest.TestCase):
    """Stubbed gh + fake credential helper: no host credentials touched."""

    GH_MOCK = ('#!/bin/sh\n'
               'case "$*" in\n'
               '  "auth status"*) exit 1 ;;\n'
               '  *) exit 0 ;;\n'
               'esac\n')

    def _env(self, tmp):
        bin_dir = Path(tmp) / "bin"
        bin_dir.mkdir(exist_ok=True)
        gh = bin_dir / "gh"
        gh.write_text(self.GH_MOCK)
        gh.chmod(0o755)
        gitconfig = Path(tmp) / "gitconfig"
        gitconfig.write_text(
            '[credential]\n'
            '\thelper = "!echo username=x-access-token; echo password=fake-token-123"\n')
        return {
            "PATH": f"{bin_dir}:/usr/bin:/bin",
            "HOME": tmp,
            "GH_CONFIG_DIR": str(Path(tmp) / "ghconf"),
            "GIT_CONFIG_GLOBAL": str(gitconfig),
        }

    def test_auth_bootstrap_writes_secured_hosts_yml(self):
        with tempfile.TemporaryDirectory() as tmp:
            r = run_script(["auth"], env=self._env(tmp))
            self.assertEqual(r.returncode, 0, r.stderr)
            hosts = Path(tmp) / "ghconf" / "hosts.yml"
            self.assertIn("fake-token-123", hosts.read_text())
            self.assertEqual(hosts.stat().st_mode & 0o777, 0o600)
            self.assertNotIn("fake-token-123", r.stdout + r.stderr)

    def test_auth_backup_is_unique_and_preserves_content(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = self._env(tmp)
            first = run_script(["auth"], env=env)
            self.assertEqual(first.returncode, 0, first.stderr)
            second = run_script(["auth"], env=env)
            self.assertEqual(second.returncode, 0, second.stderr)
            backups = list((Path(tmp) / "ghconf").glob("hosts.yml.bak.*"))
            self.assertEqual(len(backups), 1)
            self.assertIn("fake-token-123", backups[0].read_text())
            self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)

    def test_auth_rejects_invalid_env_token(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = self._env(tmp)
            env["GH_TOKEN"] = "invalid-token"
            r = run_script(["auth"], env=env)
            self.assertEqual(r.returncode, 1)
            self.assertIn("GH_TOKEN", r.stderr)
            self.assertFalse((Path(tmp) / "ghconf" / "hosts.yml").exists())

    def test_auth_repairs_permissive_config_dir(self):
        # a umask-775 config directory we own gets tightened, not rejected
        with tempfile.TemporaryDirectory() as tmp:
            loose = Path(tmp) / "loose"
            loose.mkdir()
            loose.chmod(0o777)
            env = self._env(tmp)
            env["GH_CONFIG_DIR"] = str(loose)
            r = run_script(["auth"], env=env)
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(loose.stat().st_mode & 0o777, 0o700)
            self.assertIn("fake-token-123", (loose / "hosts.yml").read_text())

    def test_auth_rejects_relative_config_dir(self):
        # a relative GH_CONFIG_DIR would make the parent walk loop on
        # ${d%/*}; it must fail fast instead (timeout also guards the
        # hang regression)
        with tempfile.TemporaryDirectory() as tmp:
            env = self._env(tmp)
            env["GH_CONFIG_DIR"] = "relative-ghconf"
            r = run_script(["auth"], env=env, cwd=tmp)
            self.assertEqual(r.returncode, 1)
            self.assertIn("absolute", r.stderr)

    def test_credential_lookup_disables_askpass(self):
        # an inherited GIT_ASKPASS must not run: with no helper
        # providing the credential the lookup fails cleanly instead of
        # blocking unattended runs (the 60s timeout also guards hangs)
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir = Path(tmp) / "bin"
            bin_dir.mkdir()
            marker = Path(tmp) / "askpass-called"
            askpass = bin_dir / "askpass-helper"
            askpass.write_text(f'#!/bin/sh\ntouch "{marker}"\n'
                               "echo username=u\necho password=p\n")
            askpass.chmod(0o755)
            env = {"PATH": f"{bin_dir}:/usr/bin:/bin", "HOME": tmp,
                   "GH_CONFIG_DIR": str(Path(tmp) / "ghconf"),
                   "GIT_ASKPASS": str(askpass),
                   # inherited Git configuration overrides must not
                   # satisfy the credential lookup for this test
                   "GIT_CONFIG_GLOBAL": None,
                   "GIT_CONFIG_SYSTEM": None,
                   "GIT_CONFIG_COUNT": None}
            gh = bin_dir / "gh"
            gh.write_text('#!/bin/sh\n'
                          'case "$*" in\n'
                          '  "auth status"*) exit 1 ;;\n'
                          '  *) exit 0 ;;\n'
                          'esac\n')
            gh.chmod(0o755)
            r = run_script(["auth"], env=env)
            self.assertEqual(r.returncode, 1)
            self.assertIn("no github.com credential", r.stderr)
            self.assertFalse(marker.exists(), "inherited GIT_ASKPASS ran")


class RepoSlugTests(unittest.TestCase):
    def _mk_repo(self, tmp, url):
        # repo_slug reads only the checkout's own config (--local), so
        # the fixture must be a real repository, not a global config;
        # inherited repository-location variables are cleared so a host
        # hook cannot redirect init/config away from tmp
        clean = dict(os.environ)
        for var in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
            clean.pop(var, None)
        subprocess.run(["git", "init", "-q", tmp], check=True, env=clean)
        subprocess.run(["git", "-C", tmp, "config", "--local",
                        "remote.origin.url", url], check=True, env=clean)

    def _slug(self, url):
        with tempfile.TemporaryDirectory() as tmp:
            self._mk_repo(tmp, url)
            full = {"PATH": os.environ.get("PATH", "/usr/bin:/bin")}
            return subprocess.run(
                [BASH, "-c", f'source "{SCRIPT}"; repo_slug'],
                capture_output=True, text=True, env=full, cwd=tmp)

    def test_https_remote(self):
        r = self._slug("https://github.com/kikakkz/looming.git")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "github.com/kikakkz/looming")

    def test_ssh_remote(self):
        r = self._slug("git@github.com:kikakkz/looming.git")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "github.com/kikakkz/looming")

    def test_non_github_remote_rejected(self):
        r = self._slug("https://example.com/x/y.git")
        self.assertNotEqual(r.returncode, 0)

    def test_access_check_ignores_gh_host(self):
        with tempfile.TemporaryDirectory() as tmp:
            self._mk_repo(tmp, "https://github.com/kikakkz/looming.git")
            bin_dir = Path(tmp) / "bin"
            bin_dir.mkdir()
            mock_log = Path(tmp) / "gh-args.log"
            gh = bin_dir / "gh"
            gh.write_text(
                "#!/bin/sh\n"
                'echo "$*" >> "' + str(mock_log) + '"\n'
                'case "$*" in\n'
                '  "auth status --help") echo "  -a, --active" ;;\n'
                '  *) exit 0 ;;\n'
                "esac\n")
            gh.chmod(0o755)
            full = {"PATH": f"{bin_dir}:/usr/bin:/bin",
                    "GH_HOST": "evil.example.com",
                    "GH_MOCK_LOG": str(mock_log)}
            r = subprocess.run(
                [BASH, "-c", f'source "{SCRIPT}"; gh_ready'],
                capture_output=True, text=True, env=full, cwd=tmp)
            self.assertEqual(r.returncode, 0, r.stderr)
            calls = mock_log.read_text()
            self.assertIn("pr list --limit 1 --repo github.com/kikakkz/looming",
                          calls)


if __name__ == "__main__":
    unittest.main()
