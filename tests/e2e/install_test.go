// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e_test

// TestInstallScript is the artifact-only installer path of the bundle
// e2e layer (#151, release-l1 §3.7/§5 R4): it drives the REAL
// scripts/install.sh against a release-shaped file:// mirror and a
// fixture OCI registry, then walks the operator journey the installer
// promises — persisted invite, admin registration, member key, a
// forwarded completion — over the installed bundle's published ports.
// The bootstrap scenario (bootstrap_test.go) proves `looming apply`
// itself; this test proves the artifact path around it: released
// artifacts only, zero builds inside the installer.
//
// Fixture preparation builds the component images and PUBLISHES them
// to the fixture registry (the e2e layer drives source builds by
// design); the installer under test only downloads the released CLI +
// manifest and pulls the pinned images back. The registry host rides
// the LOOMING_IMAGE_REGISTRY seam (render package) so the pinned refs
// resolve at the fixture instead of ghcr.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The installer's phase versions: the fresh install resolves "latest"
// from the mirror (v0.1.0), the upgrade pins v0.2.0 explicitly. The
// pins differ so the rendered image refs move and render-diff must
// reconverge the containers.
const (
	installVersion = "v0.1.0"
	upgradeVersion = "v0.2.0"
)

// releaseComponent is one publishable component image: the release
// name (the manifest's components key), the compose service name its
// container converges under, and its docker build shape — gateway and
// identity build from their component directory, topologyd from the
// bundle root (its go.mod replace pins platform/go).
type releaseComponent struct {
	release    string
	service    string
	context    string
	dockerfile string
}

var releaseComponents = []releaseComponent{
	{release: "gateway", service: "gateway-front", context: "gateway", dockerfile: "gateway/Dockerfile"},
	{release: "identity", service: "identityd", context: "identity", dockerfile: "identity/Dockerfile"},
	{release: "topology", service: "topologyd", context: ".", dockerfile: "topology/Dockerfile"},
}

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
	upstream := newFakeUpstream(t)
	env := append(cleanDockerEnv(t),
		"HOME="+t.TempDir(),
		"LOOMING_IMAGE_REGISTRY="+registryHost,
	)

	// Phase 0 — fixture preparation: the CLI binary (host arch), the
	// component images built and PUBLISHED to the fixture registry,
	// and the release-shaped mirror. None of this is the install path:
	// it stands in for the release CI's published artifacts.
	cliBin := filepath.Join(t.TempDir(), "looming")
	buildCLI(t, root, cliBin)
	reg := newOCIRegistry(t)
	mirror := newReleaseMirror(t)
	publishVersion(t, root, env, mirror, cliBin, installVersion, "")

	prefix := t.TempDir()

	// The teardown channel converges the installed prefix down through
	// the installed CLI's own dry-run render; registered before the
	// install so a failed run still cleans up what it reached.
	f := &fixture{
		repoRoot:   prefix,
		bundleRoot: prefix,
		cli:        filepath.Join(prefix, "bin", "looming"),
		dockerEnv:  env,
	}
	t.Cleanup(func() {
		if t.Failed() && f.teardownCompose != "" {
			t.Log("installer scenario failed — container logs follow")
			t.Log(f.composeLogs(f.teardownCompose, "gateway-front", "identityd", "topologyd", "bundle-postgres"))
		}
		if f.teardownCompose != "" {
			if out, err := f.composeDown(); err != nil {
				t.Logf("teardown: compose down (best effort): %v\n%s", err, out)
			}
		}
		assertNoStrayContainers(t)
		if _, err := os.Stat(filepath.Join(prefix, "pgdata")); err == nil {
			f.chownPostgresData(t)
		}
	})

	// --- phase 1: fresh install through the real installer ----------

	installArgs := []string{
		"--email", "admin@example.com",
		"--engine-url", fmt.Sprintf("http://host.docker.internal:%d/v1", upstream.port),
		"--engine-key", staticUpstreamAuth,
		"--models", e2eModel,
	}
	out := runInstaller(t, env, root, prefix, mirror.base, installArgs...)
	assert.Contains(t, out, "bootstrap invite",
		"the installer must print the invite block:\n%s", out)
	assert.Contains(t, out, installVersion+" is installed",
		"the summary names the installed release:\n%s", out)

	// The operator config lands with the secret-channel permissions:
	// every file the installer writes carries 0600 (the CLI 0755).
	topologyPath := filepath.Join(prefix, "topology.yaml")
	require.FileExists(t, topologyPath)
	for _, name := range []string{
		"postgres.env", "identityd.env", "gateway.env", "topologyd.env",
		"topology.yaml", "bundle-manifest.yaml",
		"apply.log", "bootstrap-invite.txt",
	} {
		info, err := os.Stat(filepath.Join(prefix, name))
		require.NoError(t, err, "installer writes %s", name)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s must be 0600", name)
	}
	cliInfo, err := os.Stat(filepath.Join(prefix, "bin", "looming"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), cliInfo.Mode().Perm(), "the downloaded CLI must be 0755")

	// The generated topology is the e2e-proven single-host shape plus
	// the installer-owned bundle block: the manifest selects the
	// pinned combination, so the file itself never names images.
	raw, err := os.ReadFile(topologyPath)
	require.NoError(t, err)
	topology := string(raw)
	assert.Contains(t, topology, "bundle:\n  version: "+installVersion+"\n  manifest: '"+filepath.Join(prefix, "bundle-manifest.yaml")+"'",
		"the installer-owned bundle block pins the release and names the downloaded manifest")
	assert.NotContains(t, topology, "ghcr.io", "the topology never carries image refs — apply resolves pins from the manifest")
	assert.NotContains(t, topology, "build:", "a pinned topology has no build path at all")

	// The installed combination is exactly the release's pins: every
	// service converged from the fixture registry at the pinned tag.
	for _, c := range releaseComponents {
		c := c
		waitFor(t, 90*time.Second, 2*time.Second, c.service+" running the "+installVersion+" pin", func() bool {
			return installedImage(t, env, c.service) == registryHost+"/"+c.release+":"+installVersion
		})
	}

	// The teardown channel: the installed CLI renders the same project
	// the installer converged (fully pinned → no bundle root needed).
	f.teardownCompose = f.dryRunCompose(t)

	// The persisted invite is the same "  token: <value>" shape apply
	// prints; the admin journey below consumes it.
	invite := inviteTokenFromFile(t, filepath.Join(prefix, "bootstrap-invite.txt"))

	client := &http.Client{Timeout: 10 * time.Second}
	waitFor(t, 90*time.Second, 2*time.Second, "identityd serving", func() bool {
		// 400-not-401: the route is live (same probe as the bootstrap
		// scenario's first-boot contract).
		req, _ := http.NewRequest(http.MethodPost, f.identityBase()+"/v1/self/login", strings.NewReader("{"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusBadRequest
	})

	// Admin registers with the persisted invite and lands active.
	status, body := postJSON(t, client, f.identityBase()+"/v1/self/register", nil, map[string]any{
		"username":     "admin",
		"password":     "admin-password-1234",
		"email":        "admin@example.com",
		"invite_token": invite,
	})
	require.Equal(t, http.StatusCreated, status, "bootstrap registration with the installer's invite: %v", body)
	assert.Equal(t, "active", body["status"], "the first admin lands active: %v", body)

	status, body = postJSON(t, client, f.identityBase()+"/v1/self/login", nil, map[string]any{
		"username": "admin",
		"password": "admin-password-1234",
	})
	require.Equal(t, http.StatusOK, status, "admin login: %v", body)
	adminAuth := map[string]string{"Authorization": "Bearer " + body["token"].(string)}

	// A provisioned member issues a key and forwards through the
	// installer-generated gateway placement: the catalog (from
	// --models) admits the model, the static engine auth rides
	// southbound, a catalog-outside model fails closed.
	status, body = postJSON(t, client, f.identityBase()+"/v1/admin/principals", adminAuth, map[string]any{
		"username":     "member",
		"password":     "member-password-1234",
		"display_name": "Installer Member",
		"kind":         "human",
		"roles":        []string{"member"},
	})
	require.Equal(t, http.StatusCreated, status, "admin provisions the member: %v", body)

	status, body = postJSON(t, client, f.identityBase()+"/v1/self/login", nil, map[string]any{
		"username": "member",
		"password": "member-password-1234",
	})
	require.Equal(t, http.StatusOK, status, "member login: %v", body)
	memberAuth := map[string]string{"Authorization": "Bearer " + body["token"].(string)}

	status, body = postJSON(t, client, f.identityBase()+"/v1/self/keys", memberAuth, map[string]any{
		"name": "installer-key",
	})
	require.Equal(t, http.StatusCreated, status, "member issues a key: %v", body)
	memberKey := body["key"].(string)
	require.True(t, strings.HasPrefix(memberKey, "lk-"), "the raw key is the only plaintext exit: %q", memberKey)

	chat := map[string]any{
		"model":    e2eModel,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	var fwdBody []byte
	waitFor(t, 120*time.Second, 3*time.Second, "the member's forwarded chat completion", func() bool {
		var ok bool
		status, fwdBody, ok = tryPostRaw(client, f.gatewayBase()+"/v1/chat/completions",
			map[string]string{"Authorization": "Bearer " + memberKey}, chat)
		return ok && status == http.StatusOK
	})
	assert.Contains(t, string(fwdBody), "chatcmpl-e2e-canned", "the upstream's canned completion rides back: %s", fwdBody)
	assert.True(t, upstream.contractOK(),
		"every forwarded request carried the installer's --engine-key southbound")

	blockedChat := map[string]any{
		"model":    e2eModel + "-not-in-catalog",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	status, _ = postRaw(t, client, f.gatewayBase()+"/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + memberKey}, blockedChat)
	assert.Equal(t, http.StatusForbidden, status, "a catalog-outside model is a 403")

	// --- phase 2: re-running at the installed version is a
	// converge-only re-run, not a clobber (release-l1 §3.7) ----------

	topologyBefore, err := os.ReadFile(topologyPath)
	require.NoError(t, err)
	pgEnvBefore, err := os.ReadFile(filepath.Join(prefix, "postgres.env"))
	require.NoError(t, err)

	out = runInstaller(t, env, root, prefix, mirror.base, installArgs...)
	assert.Contains(t, out, "converge-only",
		"a same-version re-run must announce itself as converge-only:\n%s", out)
	assert.Contains(t, out, "host local: skipped",
		"nothing changed, so apply skips the converge:\n%s", out)

	raw, err = os.ReadFile(topologyPath)
	require.NoError(t, err)
	assert.Equal(t, topologyBefore, raw, "a converge-only re-run never rewrites the operator's topology")
	raw, err = os.ReadFile(filepath.Join(prefix, "postgres.env"))
	require.NoError(t, err)
	assert.Equal(t, pgEnvBefore, raw, "a converge-only re-run never rotates the existing secrets")

	// --- phase 3: upgrade — only the bundle: block moves; operator
	// edits, secrets, and state survive (release-l1 §3.8) -------------

	edited := bytes.Replace(topologyBefore,
		[]byte("cluster: {name: 'looming cluster'}"),
		[]byte("cluster: {name: 'operator edited cluster'}"), 1)
	require.NotEqual(t, topologyBefore, edited, "the operator edit applies")
	require.NoError(t, os.WriteFile(topologyPath, edited, 0o600))

	publishVersion(t, root, env, mirror, cliBin, upgradeVersion, installVersion)

	out = runInstaller(t, env, root, prefix, mirror.base,
		append([]string{"--version", upgradeVersion}, installArgs...)...)
	assert.Contains(t, out, "upgrading "+installVersion+" → "+upgradeVersion,
		"the upgrade names both releases:\n%s", out)

	raw, err = os.ReadFile(topologyPath)
	require.NoError(t, err)
	upgraded := string(raw)
	assert.Contains(t, upgraded, "cluster: {name: 'operator edited cluster'}",
		"operator edits survive the upgrade (only the bundle: block is installer-owned)")
	assert.Contains(t, upgraded, "  version: "+upgradeVersion,
		"the bundle block moved to the requested release")
	assert.Contains(t, upgraded, "  manifest: '"+filepath.Join(prefix, "bundle-manifest.yaml")+"'",
		"the bundle block still names the prefix manifest")

	manifest, err := os.ReadFile(filepath.Join(prefix, "bundle-manifest.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "gateway: "+upgradeVersion,
		"the refreshed manifest file pins the new combination")

	raw, err = os.ReadFile(filepath.Join(prefix, "postgres.env"))
	require.NoError(t, err)
	assert.Equal(t, pgEnvBefore, raw, "an upgrade never rotates the existing secrets")

	for _, c := range releaseComponents {
		c := c
		waitFor(t, 90*time.Second, 2*time.Second, c.service+" running the "+upgradeVersion+" pin", func() bool {
			return installedImage(t, env, c.service) == registryHost+"/"+c.release+":"+upgradeVersion
		})
	}

	// State survived the upgrade: identityd answers and the admin still
	// authenticates against the same state plane.
	waitFor(t, 90*time.Second, 2*time.Second, "identityd serving after the upgrade", func() bool {
		req, _ := http.NewRequest(http.MethodPost, f.identityBase()+"/v1/self/login", strings.NewReader("{"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusBadRequest
	})
	status, body = postJSON(t, client, f.identityBase()+"/v1/self/login", nil, map[string]any{
		"username": "admin",
		"password": "admin-password-1234",
	})
	require.Equal(t, http.StatusOK, status, "admin login after the upgrade: %v", body)

	reg.assertClean(t)
}

// runInstaller drives the REAL scripts/install.sh against the given
// prefix and release mirror, failing the test on a non-zero exit.
func runInstaller(t *testing.T, env []string, root, prefix, releaseURL string, args ...string) string {
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
	require.NoError(t, err, "install.sh:\n%s", out)
	return string(out)
}

// newReleaseMirror builds the file:// download fixture's root. Version
// directories are added by publishVersion; latest points at the first
// published version.
func newReleaseMirror(t *testing.T) *releaseMirror {
	t.Helper()
	dir := t.TempDir()
	return &releaseMirror{dir: dir, base: "file://" + dir}
}

// publishVersion assembles one release in the mirror and registry:
// the component images tagged, pushed, and REMOVED from the local
// daemon (so the installer's converge can only succeed by pulling
// through the fixture registry — a stale local image must not mask a
// broken artifact path), plus the mirror's download assets. fromVersion,
// when set, re-tags the already-published images at the new version
// instead of rebuilding (the upgrade's image content is identical —
// the pin movement is what render-diff must see).
func publishVersion(t *testing.T, root string, env []string, m *releaseMirror, cliBin, version, fromVersion string) {
	t.Helper()

	for _, c := range releaseComponents {
		ref := registryHost + "/" + c.release + ":" + version
		if fromVersion != "" {
			out, err := dockerCmd(t, env, root, 2*time.Minute, "tag",
				registryHost+"/"+c.release+":"+fromVersion, ref)
			require.NoError(t, err, "tag %s at %s:\n%s", c.release, version, out)
		} else {
			// Name-only GOPROXY passthrough, the same contract as the
			// release workflow (release-l1 §2): the invoking
			// environment supplies a mirror when it needs one (some
			// build hosts cannot reach proxy.golang.org); unset keeps
			// the Dockerfile's default.
			args := []string{"build", "-t", ref, "-f", c.dockerfile}
			if gp := os.Getenv("GOPROXY"); gp != "" {
				args = append(args, "--build-arg", "GOPROXY="+gp)
			}
			args = append(args, c.context)
			out, err := dockerCmd(t, env, root, 10*time.Minute, args...)
			require.NoError(t, err, "build %s:\n%s", ref, out)
		}
		out, err := dockerCmd(t, env, root, 5*time.Minute, "push", ref)
		require.NoError(t, err, "push %s:\n%s", ref, out)
		out, err = dockerCmd(t, env, root, 2*time.Minute, "rmi", ref)
		require.NoError(t, err, "drop the local %s so the converge must pull it:\n%s", ref, out)
	}

	versionDir := filepath.Join(m.dir, version)
	require.NoError(t, os.MkdirAll(versionDir, 0o755))
	raw, err := os.ReadFile(cliBin)
	require.NoError(t, err)
	arch := runtime.GOARCH
	require.Contains(t, []string{"amd64", "arm64"}, arch, "the released CLI assets cover linux/amd64 + linux/arm64")
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "looming-linux-"+arch), raw, 0o755))
	manifest := fmt.Sprintf("components:\n  gateway: %s\n  identity: %s\n  topology: %s\n",
		version, version, version)
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "bundle-manifest.yaml"), []byte(manifest), 0o600))

	if fromVersion == "" {
		require.NoError(t, os.WriteFile(filepath.Join(m.dir, "latest"), []byte(version+"\n"), 0o600))
	}
}

// installedImage reads the image ref one converged service's container
// runs, "" when the container is not there (yet).
func installedImage(t *testing.T, env []string, service string) string {
	t.Helper()
	out, err := dockerCmd(t, env, ".", 30*time.Second,
		"inspect", "-f", "{{.Config.Image}}", composeProject+"-"+service+"-1")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// dockerCmd runs a docker CLI child through the suite's clean
// environment with a bounded timeout, returning combined output.
func dockerCmd(t *testing.T, env []string, dir string, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// inviteTokenFromFile parses the persisted invite's "  token: <value>"
// line (the same shape apply prints).
func inviteTokenFromFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "token:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("bootstrap-invite.txt carries the token:\n%s", raw)
	return ""
}
