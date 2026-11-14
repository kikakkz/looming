// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e_test

// TestInstallScript is the CLI-only installer path (AD-42 decision 1,
// #191 S1): it drives the REAL scripts/install.sh against a
// release-shaped file:// mirror and asserts the shrunk contract —
//
//   - the arch-matched `looming` binary lands at the --prefix
//     destination, 0755, and runs (`looming --version` is both the
//     script's and this suite's check);
//   - NOTHING else is written: the destination holds exactly the one
//     binary — no topology.yaml, no env files, no manifest, no state
//     directory — and no converge side effects (the compose project
//     stays empty);
//   - re-running over the same destination is idempotent;
//   - a bad --version fails closed, leaving nothing behind;
//   - a host without docker fails closed at the preflight.
//
// The v0.1 cluster-in-one-shot assertions (persisted invite, admin
// registration, forwarded completion, the upgrade path rewriting the
// bundle block) retired with the contract they tested; that coverage
// returns with the setup → converge → purge scenario (AD-42 decisions
// 2/3, slice S3).
//
// The fixture publishes ONLY the CLI asset into the mirror — the S1
// installer downloads the binary and stops; no images are built or
// pulled and the OCI registry fixture (registryfixture_test.go) goes
// unexercised here until the S3 scenario brings the cluster journey
// back.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installVersion is the fixture release the mirror's `latest` resolves.
const installVersion = "v0.1.0"

// releaseMirror is the file:// download fixture: a directory shaped
// exactly like the release source install.sh expects (<base>/latest,
// <base>/<tag>/<assets>), so the REAL installer runs unmodified.
type releaseMirror struct {
	dir  string
	base string
}

func TestInstallScript(t *testing.T) {
	requireDocker(t)

	root := repoRoot(t)
	env := append(cleanDockerEnv(t), "HOME="+t.TempDir())

	// Fixture preparation stands in for the release CI's published
	// artifacts: the CLI binary (host arch) published into a
	// release-shaped mirror. None of this is the install path.
	cliBin := filepath.Join(t.TempDir(), "looming")
	buildCLI(t, root, cliBin)
	mirror := newReleaseMirror(t)
	publishCLI(t, mirror, cliBin, installVersion)

	// CLI-only delivery means zero converge side effects: whatever the
	// host already runs under the compose project, this install must
	// not add to it. The before/after diff (not a global emptiness
	// assertion) keeps the check honest on hosts that carry a
	// previously converged cluster; the strict teardown assertion is
	// the bootstrap scenario's own.
	convergedBefore := projectContainers(t)

	// --- the happy path: one binary, nothing else ---------------------

	prefix := t.TempDir()
	out := runInstaller(t, env, root, prefix, mirror.base)
	assert.Contains(t, out, installVersion+" is installed",
		"the summary names the installed release:\n%s", out)
	assert.Contains(t, out, "  binary:  "+filepath.Join(prefix, "looming"),
		"the summary names the installed path:\n%s", out)
	assert.Contains(t, out, "looming version",
		"the summary carries the --version check output:\n%s", out)
	assert.Contains(t, out, "looming apply --config",
		"the summary points at apply for bring-up:\n%s", out)
	assert.Contains(t, out, "looming join",
		"the summary points at join for more hosts:\n%s", out)

	entries, err := os.ReadDir(prefix)
	require.NoError(t, err)
	require.Len(t, entries, 1,
		"the installer writes NOTHING but the binary — no topology, no env files, no state dir")
	assert.Equal(t, "looming", entries[0].Name())
	info, err := entries[0].Info()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "the installed CLI must be 0755")

	// The script's --version check is the same check the operator runs;
	// the installed binary must answer it.
	cli := filepath.Join(prefix, "looming")
	versionOut, err := exec.Command(cli, "--version").CombinedOutput()
	require.NoError(t, err, "the installed binary must run: %s", versionOut)
	assert.Contains(t, string(versionOut), "looming version")

	// --- re-running over the same destination is idempotent -----------

	out = runInstaller(t, env, root, prefix, mirror.base)
	assert.Contains(t, out, installVersion+" is installed",
		"a re-run succeeds and reports the same release:\n%s", out)
	entries, err = os.ReadDir(prefix)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a re-run still leaves exactly the binary")

	// --- a bad --version fails closed, leaving nothing behind ---------

	badPrefix := t.TempDir()
	out = runInstallerExpectFail(t, env, root, badPrefix, mirror.base,
		"--version", "v9.9.9")
	assert.Contains(t, out, "could not download",
		"the failure names the missing artifact:\n%s", out)
	entries, err = os.ReadDir(badPrefix)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed install writes nothing")

	// --- a host without docker fails closed at the preflight ----------

	nulEnv := []string{
		"PATH=" + t.TempDir(), // nothing resolvable: no docker, no curl
		"HOME=" + t.TempDir(),
	}
	out = runInstallerExpectFail(t, nulEnv, root, t.TempDir(), mirror.base)
	assert.Contains(t, out, "required command not found: docker",
		"the preflight names the missing prerequisite:\n%s", out)

	// Every phase ran the installer; the compose project is untouched.
	assert.Equal(t, convergedBefore, projectContainers(t),
		"the installer converges nothing — the compose project set is unchanged")
}

// projectContainers lists the container ids currently under the
// "looming" compose project, sorted for a stable diff.
func projectContainers(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=com.docker.compose.project="+composeProject).Output()
	require.NoError(t, err)
	ids := strings.Fields(string(out))
	sort.Strings(ids)
	return ids
}

// runInstaller drives the REAL scripts/install.sh against the given
// install destination and release mirror, failing the test on a
// non-zero exit.
func runInstaller(t *testing.T, env []string, root, prefix, releaseURL string, args ...string) string {
	t.Helper()
	out, err := runInstallerCmd(t, env, root, prefix, releaseURL, args...)
	require.NoError(t, err, "install.sh:\n%s", out)
	return out
}

// runInstallerExpectFail is runInstaller for the fail-closed cases: the
// non-zero exit is required, and the output comes back for assertions.
func runInstallerExpectFail(t *testing.T, env []string, root, prefix, releaseURL string, args ...string) string {
	t.Helper()
	out, err := runInstallerCmd(t, env, root, prefix, releaseURL, args...)
	require.Error(t, err, "install.sh must fail:\n%s", out)
	return out
}

func runInstallerCmd(t *testing.T, env []string, root, prefix, releaseURL string, args ...string) (string, error) {
	t.Helper()
	argv := append([]string{
		filepath.Join(root, "scripts", "install.sh"),
		"--prefix", prefix,
		"--release-url", releaseURL,
	}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", argv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// newReleaseMirror builds the file:// download fixture's root. Version
// directories are added by publishCLI; latest points at the published
// version.
func newReleaseMirror(t *testing.T) *releaseMirror {
	t.Helper()
	dir := t.TempDir()
	return &releaseMirror{dir: dir, base: "file://" + dir}
}

// publishCLI assembles one release in the mirror: the host-arch CLI
// asset under <dir>/<version>/ and `latest` pointing at it. Nothing
// else — the S1 installer downloads only the binary.
func publishCLI(t *testing.T, m *releaseMirror, cliBin, version string) {
	t.Helper()
	versionDir := filepath.Join(m.dir, version)
	require.NoError(t, os.MkdirAll(versionDir, 0o755))
	raw, err := os.ReadFile(cliBin)
	require.NoError(t, err)
	arch := runtime.GOARCH
	require.Contains(t, []string{"amd64", "arm64"}, arch, "the released CLI assets cover linux/amd64 + linux/arm64")
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "looming-linux-"+arch), raw, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(m.dir, "latest"), []byte(version+"\n"), 0o600))
}
