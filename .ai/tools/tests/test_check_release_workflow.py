# SPDX-License-Identifier: Apache-2.0
"""Unit tests for check_release_workflow.py (#159, R3 bundle contract
#161, AD-40 VERSION gate #172)."""

import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import check_release_workflow as crw

PRECHECK = """\
      - name: Immutable pre-check
        run: |
          set -euo pipefail
          if out=$(docker manifest inspect "$IMAGE" 2>&1); then
            echo "::error::$IMAGE already exists — refusing to repoint"
            exit 1
          fi
          case "$out" in
            *"no such manifest"*|*"manifest unknown"*|*"not found"*|*"NAME_UNKNOWN"*)
              echo "tag absent — proceeding"
              ;;
            *)
              echo "::error::failed closed: $out"
              exit 1
              ;;
          esac
"""

GATE = """\
      - name: Check VERSION consistency
        if: needs.resolve.outputs.verify_only == 'false'
        env:
          COMPONENT: ${{ needs.resolve.outputs.component }}
          VERSION: ${{ needs.resolve.outputs.version }}
        run: |
          set -euo pipefail
          mapfile -t version_lines < <(grep -Ev '^[[:space:]]*(#|$)' "$COMPONENT/VERSION")
          if [[ "${#version_lines[@]}" -ne 1 ]]; then
            echo "::error::$COMPONENT/VERSION must hold exactly one vX.Y.Z version line (got ${#version_lines[@]}: ${version_lines[*]:-<none>})"
            exit 1
          fi
          file_version="$(sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' <<<"${version_lines[0]}")"
          if [[ ! "$file_version" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then
            echo "::error::$COMPONENT/VERSION holds no single vX.Y.Z version line (got '${version_lines[0]}')"
            exit 1
          fi
          if [[ "$file_version" != "$VERSION" ]]; then
            echo "::error::AD-40 consistency gate: $COMPONENT/VERSION is '$file_version' but the tag resolves to '$VERSION'"
            exit 1
          fi
          echo "AD-40 consistency gate green: $COMPONENT/VERSION == $VERSION"
"""

VALID = """\
# SPDX-License-Identifier: Apache-2.0
name: release

on:
  push:
    tags:
      # the two release families stay disjoint: a tag filter's "*"
      # never crosses a "/", so the bare vX.Y.Z cannot fire the
      # component jobs and component tags cannot fire the bundle job.
      - "gateway/v*.*.*"
      - "identity/v*.*.*"
      - "topology/v*.*.*"
      - "v*.*.*"
  workflow_dispatch:
    inputs:
      component:
        required: true
        type: string
      version:
        required: true
        type: string
      verify_only:
        required: false
        type: boolean
        default: false

permissions:
  contents: read

jobs:
  resolve:
    if: ${{ github.event_name == 'workflow_dispatch' || contains(github.ref_name, '/') }}
    outputs:
      git_ref: ${{ steps.parse.outputs.git_ref }}
    steps:
      - name: Resolve component and version
        id: parse
        run: |
          echo "git_ref=refs/tags/$component/$version" >> "$GITHUB_OUTPUT"
  publish:
    needs: resolve
    permissions:
      contents: read
      packages: write
    concurrency:
      group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          ref: ${{ needs.resolve.outputs.git_ref }}
          persist-credentials: false
__GATE____PRECHECK__      - name: Build and push multi-arch image
        env:
          VERSION: ${{ needs.resolve.outputs.version }}
        run: docker buildx build --build-arg VERSION="$VERSION" --tag ghcr.io/kikakkz/looming/gateway:v0.1.0 --push gateway
  bundle:
    if: ${{ github.event_name == 'push' && !contains(github.ref_name, '/') }}
    permissions:
      contents: write
    steps:
      - name: Resolve bundle version
        id: resolve
        run: |
          set -euo pipefail
          if [[ "$REF_NAME" == */* ]]; then
            echo "::error::component-shaped tag"
            exit 1
          fi
          if [[ ! "$REF_NAME" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then
            echo "::error::not vX.Y.Z"
            exit 1
          fi
          echo "git_ref=refs/tags/$REF_NAME" >> "$GITHUB_OUTPUT"
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          ref: ${{ steps.resolve.outputs.git_ref }}
          persist-credentials: false
      - name: Pinned-image smoke gate
        run: |
          set -euo pipefail
          docker pull "$image"
          docker exec smoke-pg pg_isready -U postgres
          probe_identity() { curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18081/v1/self/login -d '{'; }
          probe_topology() { curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18082/v1/internal/guide; }
          probe_gateway() { curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/; }
          expect "identityd (pinned)" 400 probe_identity
          expect "topologyd (pinned)" 401 probe_topology
          expect "gateway (pinned)" 404 probe_gateway
          echo "::error::smoke gate: $desc never answered HTTP $want"
          exit 1
          docker ps -a --filter network=smoke-net --format '{{.Names}}' | grep -v '^smoke-pg$'
          docker inspect -f '{{.State.Running}}' smoke-identity
      - name: Cross-compile CLI
        run: |
          set -euo pipefail
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o looming-linux-amd64 ./cmd/looming
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o looming-linux-arm64 ./cmd/looming
      - name: Write release notes
        run: |
          set -euo pipefail
          git log --pretty=%s "$prev..HEAD" | grep -E '^feat' | sed 's/^/- /' || true
          cp bundle/manifest.yaml bundle-manifest.yaml
      - name: Create GitHub Release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          set -euo pipefail
          gh release create "$VERSION" --notes-file release-notes.md looming-linux-amd64 looming-linux-arm64 bundle-manifest.yaml scripts/install.sh#install.sh
""".replace("__PRECHECK__", PRECHECK).replace("__GATE__", GATE)


class TagTriggerTests(unittest.TestCase):
    def test_valid_workflow_has_no_problems(self):
        self.assertEqual(crw.problems(VALID), [])

    def test_missing_gateway_tag_pattern(self):
        text = VALID.replace('      - "gateway/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'gateway/v*.*.*'", problems[0])

    def test_missing_identity_tag_pattern(self):
        text = VALID.replace('      - "identity/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'identity/v*.*.*'", problems[0])

    def test_missing_topology_tag_pattern(self):
        text = VALID.replace('      - "topology/v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'topology/v*.*.*'", problems[0])

    def test_changed_active_entry_with_stale_comment_fails(self):
        # the active trigger moves to v9, the old pattern survives only
        # in a comment — the check must read active entries, not text
        text = VALID.replace(
            '      - "gateway/v*.*.*"\n',
            '      - "gateway/v9.*.*.*"\n      # "gateway/v*.*.*"\n')
        problems = crw.problems(text)
        self.assertTrue(any("'gateway/v*.*.*'" in p for p in problems))
        self.assertTrue(any("'gateway/v9.*.*.*'" in p for p in problems))

    def test_patterns_only_in_comments_are_not_triggers(self):
        text = VALID.replace('      - "gateway/v*.*.*"\n', "")
        text = text.replace('name: release', '# "gateway/v*.*.*"\nname: release')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'gateway/v*.*.*'", problems[0])


class PermissionsTests(unittest.TestCase):
    def test_missing_packages_write(self):
        text = VALID.replace("      packages: write\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("packages: write", problems[0])

    def test_read_only_packages_permission_fails(self):
        text = VALID.replace("      packages: write", "      packages: read")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("packages: write", problems[0])


class ConcurrencyTests(unittest.TestCase):
    def test_missing_concurrency_group(self):
        text = VALID.replace(
            "      group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}\n",
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("no concurrency group", problems[0])

    def test_per_run_key_fails(self):
        text = VALID.replace(
            "group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}",
            "group: release-${{ github.run_id }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("resolved", problems[0])

    def test_component_only_key_fails(self):
        text = VALID.replace(
            "group: release-${{ needs.resolve.outputs.component }}-${{ needs.resolve.outputs.version }}",
            "group: release-${{ needs.resolve.outputs.component }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("resolved", problems[0])


class PrecheckTests(unittest.TestCase):
    def test_renamed_step_fails(self):
        text = VALID.replace("- name: Immutable pre-check",
                             "- name: Check tag existence")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("immutable pre-check step absent", problems[0])

    def test_bare_inspect_command_fails(self):
        # a naked inspect neither refuses existing tags nor fails closed
        text = VALID.replace(PRECHECK, """\
      - name: Immutable pre-check
        run: docker manifest inspect "$IMAGE"
""")
        problems = crw.problems(text)
        self.assertTrue(any("verdict" in p for p in problems))
        self.assertTrue(any("already exists" in p for p in problems))
        self.assertTrue(any("not-found dispatch" in p for p in problems))

    def test_refusal_without_exit_fails(self):
        text = VALID.replace(
            'echo "::error::$IMAGE already exists — refusing to repoint"\n'
            '            exit 1\n',
            'echo "::error::$IMAGE already exists — refusing to repoint"\n')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("existing-tag branch", problems[0])

    def test_no_not_found_markers_fails(self):
        text = VALID.replace(
            '*"no such manifest"*|*"manifest unknown"*|*"not found"*|*"NAME_UNKNOWN"*)',
            '*"some other verdict"*)')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("not-found verdict", problems[0])

    def test_default_without_fail_closed_exit_fails(self):
        text = VALID.replace(
            'echo "::error::failed closed: $out"\n              exit 1\n',
            'echo "::error::failed closed: $out"\n')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("fail closed", problems[0])

    def test_missing_case_dispatch_fails(self):
        text = VALID.replace('case "$out" in', 'echo "$out"')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("not-found dispatch", problems[0])


class GitRefTests(unittest.TestCase):
    def test_git_ref_without_output_redirect_fails(self):
        text = VALID.replace(
            ' >> "$GITHUB_OUTPUT"',
            '', 1)  # the R1 resolve line only; the bundle line stays
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("git_ref", problems[0])

    def test_missing_job_output_mapping_fails(self):
        text = VALID.replace(
            "      git_ref: ${{ steps.parse.outputs.git_ref }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("outputs do not map", problems[0])

    def test_checkout_without_pinned_ref_fails(self):
        text = VALID.replace(
            "          ref: ${{ needs.resolve.outputs.git_ref }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("checkout does not pin", problems[0])

    def test_checkout_pinning_branch_ref_fails(self):
        text = VALID.replace(
            "ref: ${{ needs.resolve.outputs.git_ref }}",
            "ref: ${{ github.ref }}")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("checkout does not pin", problems[0])


class BundleTriggerTests(unittest.TestCase):
    def test_missing_bundle_tag_pattern(self):
        text = VALID.replace('      - "v*.*.*"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'v*.*.*'", problems[0])

    def test_bundle_pattern_only_in_comment_is_not_a_trigger(self):
        text = VALID.replace('      - "v*.*.*"\n', "")
        text = text.replace("permissions:\n",
                            '# "v*.*.*"\npermissions:\n')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("'v*.*.*'", problems[0])

    def test_disjointness_note_required(self):
        # the v*.*.* filter only excludes component tags because the
        # glob "*" does not cross "/"; the note pinning that semantic
        # must survive refactors of the trigger block
        text = VALID.replace('never crosses a "/"', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("disjoint", problems[0])


class BundleGuardTests(unittest.TestCase):
    def test_missing_component_guard(self):
        text = VALID.replace(
            "if: ${{ github.event_name == 'workflow_dispatch' || contains(github.ref_name, '/') }}\n",
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("component jobs", problems[0])

    def test_missing_bundle_guard(self):
        text = VALID.replace(
            "if: ${{ github.event_name == 'push' && !contains(github.ref_name, '/') }}\n",
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("bundle job", problems[0])

    def test_bundle_guard_not_first_property_fails(self):
        # a guard below another property (or off the job) does not
        # unambiguously belong to the bundle job
        guard = ("if: ${{ github.event_name == 'push' "
                 "&& !contains(github.ref_name, '/') }}\n")
        text = VALID.replace(guard, "")
        text = text.replace("  bundle:\n",
                            "  bundle:\n    runs-on: ubuntu-latest\n")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("bundle job", problems[0])


class ContentsWriteTests(unittest.TestCase):
    def test_missing_contents_write(self):
        text = VALID.replace("      contents: write\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("contents: write", problems[0])

    def test_contents_write_outside_the_bundle_job_fails(self):
        # the publish job must stay packages-scoped; only the bundle
        # job earns contents write
        text = VALID.replace("      packages: write\n",
                             "      contents: read\n      packages: write\n"
                             "      contents: write\n")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("outside the bundle job", problems[0])


class BundleResolveTests(unittest.TestCase):
    def test_renamed_resolve_step_fails(self):
        text = VALID.replace("- name: Resolve bundle version",
                             "- name: Resolve version")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("bundle version resolve step absent", problems[0])

    def test_missing_slash_rejection_fails(self):
        text = VALID.replace('if [[ "$REF_NAME" == */* ]]; then\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("component-shaped", problems[0])

    def test_missing_vxyz_validation_fails(self):
        text = VALID.replace(
            'if [[ ! "$REF_NAME" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then\n',
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("vX.Y.Z", problems[0])

    def test_missing_git_ref_write_fails(self):
        text = VALID.replace('echo "git_ref=refs/tags/$REF_NAME" >> "$GITHUB_OUTPUT"\n',
                             "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("git_ref", problems[0])

    def test_checkout_without_pinned_ref_fails(self):
        text = VALID.replace(
            "          ref: ${{ steps.resolve.outputs.git_ref }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("checkout does not pin", problems[0])


class SmokeGateTests(unittest.TestCase):
    def test_renamed_gate_step_fails(self):
        text = VALID.replace("- name: Pinned-image smoke gate",
                             "- name: Smoke test")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("smoke gate step absent", problems[0])

    def test_missing_pull_fails(self):
        text = VALID.replace('docker pull "$image"\n', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("pull", problems[0])

    def test_missing_postgres_fixture_fails(self):
        text = VALID.replace("docker exec smoke-pg pg_isready -U postgres\n",
                             "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("postgres", problems[0])

    def test_missing_identity_probe_fails(self):
        text = VALID.replace('expect "identityd (pinned)" 400 probe_identity\n',
                             "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("identityd probe", problems[0])

    def test_identity_probe_without_login_route_fails(self):
        text = VALID.replace("http://127.0.0.1:18081/v1/self/login", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("identityd probe", problems[0])

    def test_missing_topology_probe_fails(self):
        text = VALID.replace('expect "topologyd (pinned)" 401 probe_topology\n',
                             "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("topologyd probe", problems[0])

    def test_missing_gateway_probe_fails(self):
        text = VALID.replace('expect "gateway (pinned)" 404 probe_gateway\n',
                             "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("gateway probe", problems[0])

    def test_missing_liveness_recheck_fails(self):
        text = VALID.replace(
            "          docker inspect -f '{{.State.Running}}' smoke-identity\n",
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("liveness", problems[0])

    def test_plain_docker_ps_cannot_see_exited_containers(self):
        # CodeRabbit review on PR #164: `docker ps` lists running
        # containers only — a container that crashed after a lucky
        # probe would vanish from the recheck and the gate would
        # false-green; the recheck must use `docker ps -a`
        text = VALID.replace("docker ps -a --filter network=smoke-net",
                             "docker ps --filter network=smoke-net")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("docker ps -a", problems[0])

    def test_probe_miss_without_fail_closed_fails(self):
        text = VALID.replace(
            'echo "::error::smoke gate: $desc never answered HTTP $want"\n',
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("fail closed", problems[0])


class CrossCompileTests(unittest.TestCase):
    def test_renamed_step_fails(self):
        text = VALID.replace("- name: Cross-compile CLI", "- name: Build CLI")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("cross-compile step absent", problems[0])

    def test_cgo_enabled_required(self):
        text = VALID.replace("CGO_ENABLED=0 ", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("CGO_ENABLED=0", problems[0])

    def test_both_arches_required(self):
        text = VALID.replace("GOARCH=arm64", "GOARCH=amd64")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("amd64 + linux/arm64", problems[0])


class ReleaseAssetTests(unittest.TestCase):
    def test_renamed_release_step_fails(self):
        text = VALID.replace("- name: Create GitHub Release",
                             "- name: Publish release")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("release step absent", problems[0])

    def test_missing_gh_release_create_fails(self):
        text = VALID.replace("gh release create", "gh release upload")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("gh release create", problems[0])

    def test_missing_asset_fails(self):
        text = VALID.replace(" bundle-manifest.yaml", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("bundle-manifest.yaml", problems[0])

    def test_install_sh_missing_from_assets_fails(self):
        # R4 (#151) shipped the installer: install.sh must ride every
        # bundle release's asset set, alongside the CLI + manifest.
        text = VALID.replace(
            "looming-linux-amd64 looming-linux-arm64 bundle-manifest.yaml scripts/install.sh#install.sh",
            "looming-linux-amd64 looming-linux-arm64 bundle-manifest.yaml")
        problems = crw.problems(text)
        # Both halves of the contract fire: the asset-set membership
        # check and the repo-path form check.
        self.assertEqual(len(problems), 2)
        self.assertIn("install.sh", problems[0])
        self.assertIn("repo path", problems[1])

    def test_commented_out_release_create_fails(self):
        # Round 4 (CodeRabbit #165): a COMMENTED-OUT gh release create
        # carrying the full asset set in its text must not satisfy the
        # asset checks — only an executable command line counts.
        text = VALID.replace(
            'gh release create "$VERSION" --notes-file release-notes.md '
            'looming-linux-amd64 looming-linux-arm64 bundle-manifest.yaml '
            'scripts/install.sh#install.sh',
            '# gh release create "$VERSION" --notes-file release-notes.md '
            'looming-linux-amd64 looming-linux-arm64 bundle-manifest.yaml '
            'scripts/install.sh#install.sh')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("gh release create", problems[0])

    def test_repo_path_asset_hiding_in_comment_fails(self):
        # Round 3 (CodeRabbit #165): the asset checks parse the gh
        # release create COMMAND LINE — the correct repo-path form
        # hiding in a comment must not satisfy them while the actual
        # command uploads a bare install.sh.
        text = VALID.replace(
            "bundle-manifest.yaml scripts/install.sh#install.sh",
            "bundle-manifest.yaml install.sh")
        text = text.replace(
            'gh release create "$VERSION"',
            '# upload via scripts/install.sh#install.sh (gh rename form)\n'
            '          gh release create "$VERSION"')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("repo path", problems[0])

    def test_install_sh_bare_asset_name_fails(self):
        # install.sh lives at scripts/install.sh in the checkout: a
        # bare asset name names a file that does not exist at the
        # workspace root and gh's upload would fail — the repo-path#
        # asset-name form is required.
        text = VALID.replace("scripts/install.sh#install.sh", "install.sh")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("repo path", problems[0])

    def test_missing_notes_generation_fails(self):
        text = VALID.replace('git log --pretty=%s "$prev..HEAD"', "echo notes")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("conventional commits", problems[0])

    def test_missing_notes_file_fails(self):
        text = VALID.replace("--notes-file release-notes.md", "--draft")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("notes", problems[0])

    def test_missing_gh_token_fails(self):
        text = VALID.replace(
            "          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("GH_TOKEN", problems[0])


class OtherContractTests(unittest.TestCase):
    def test_missing_registry_prefix(self):
        text = VALID.replace("ghcr.io/kikakkz/looming/", "ghcr.io/other/ns/")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("ghcr.io/kikakkz/looming/", problems[0])

    def test_missing_verify_only_input(self):
        text = VALID.replace("      verify_only:\n", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("verify_only", problems[0])

    def test_empty_workflow_reports_every_piece(self):
        # three component tag patterns + the bundle pattern, each
        # reported on its own line, plus the R3 contract pieces, the
        # AD-40 VERSION gate, and the VERSION build-arg passthrough
        self.assertEqual(len(crw.problems("")), 24)


class VersionGateTests(unittest.TestCase):
    """AD-40 S2 (#172): the publish job refuses a tag whose version
    disagrees with the component's VERSION file, before any image push,
    and passes the version into the docker build (S3 prep)."""

    def test_missing_gate_step_fails(self):
        text = VALID.replace(GATE, "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("VERSION consistency gate step absent", problems[0])

    def test_gate_without_file_read_fails(self):
        text = VALID.replace('"$COMPONENT/VERSION"', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("VERSION file", problems[0])

    def test_gate_without_shape_guard_fails(self):
        # unparsable file content must fail closed, never read as
        # "matches" or "absent"
        text = VALID.replace(
            'if [[ ! "$file_version" =~ ^v[0-9]+\\.[0-9]+\\.[0-9]+$ ]]; then\n',
            "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("fail closed", problems[0])

    def test_gate_without_string_comparison_fails(self):
        # any check short of string equality (e.g. a mere -n presence
        # test) lets a mismatched tag through
        text = VALID.replace('if [[ "$file_version" != "$VERSION" ]]; then',
                             'if [ -n "$file_version" ]; then')
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("string-compare", problems[0])

    def test_gate_without_ad_reference_fails(self):
        text = VALID.replace("AD-40", "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("AD-40", problems[0])

    def test_gate_after_build_fails(self):
        # the gate must reject before the build/push sequence runs
        text = VALID.replace(GATE, "")
        build_line = ('        run: docker buildx build --build-arg '
                      'VERSION="$VERSION" --tag '
                      'ghcr.io/kikakkz/looming/gateway:v0.1.0 --push gateway\n')
        text = text.replace(build_line, build_line + GATE)
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("before the image build", problems[0])

    def test_build_without_version_build_arg_fails(self):
        text = VALID.replace('--build-arg VERSION="$VERSION" ', "")
        problems = crw.problems(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("build arg", problems[0])


class CliTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.workflow = self.tmp / "release.yml"

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_valid_file_ok(self):
        self.workflow.write_text(VALID, encoding="utf-8")
        self.assertEqual(crw.main(["--workflow", str(self.workflow)]), 0)

    def test_broken_file_fails(self):
        self.workflow.write_text("name: release\n", encoding="utf-8")
        self.assertEqual(crw.main(["--workflow", str(self.workflow)]), 1)

    def test_missing_file_fails(self):
        self.assertEqual(
            crw.main(["--workflow", str(self.tmp / "absent.yml")]), 1)


if __name__ == "__main__":
    unittest.main()
