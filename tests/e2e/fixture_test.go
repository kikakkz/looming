// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e_test is the bundle-level end-to-end suite (AD-25's e2e
// layer, AD-34-neutral): it drives the REAL looming binary and
// the REAL component containers over real I/O through the onboarding
// scenario spine — apply → invite → admin → member/key/quota →
// gateway forward → guide page toggle → pull-join → teardown.
//
// The suite imports no component code: the bundle is a black box
// reached through its published surfaces (the CLI, the
// component HTTP APIs, the docker CLI). Every fixture file lives under
// t.TempDir(); nothing touches /etc or any host state beyond the
// "looming" compose project and its prefixed resources.
package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// composeProject is the fixed project name the renderer converges
// under (render.Project): the single prefix this suite may leave on
// the host, and the filter its stray-container assertions use.
const composeProject = "looming"

// The scenario's published ports. The topology.yaml the fixture writes
// is the single source of truth — these constants only seed it.
const (
	portGateway  = 18080
	portIdentity = 18081
	portTopology = 18082
	portPostgres = 15432
)

// staticUpstreamAuth is the gateway's static fallback credential
// (GATEWAY_UPSTREAM_AUTH). The member key is issued WITHOUT an engine
// provisioned (slice C fallback), so every forwarded request must
// carry THIS value southbound — the fake upstream asserts it.
const staticUpstreamAuth = "e2e-static-upstream-auth"

// e2eModel is the allowlisted model id the member's forward request
// uses; the gateway-front allowlist is re-applied once the member's
// principal id exists (the StaticAllowlist keys subjects by id).
const e2eModel = "loom-e2e-model"

// fixture is the shared scenario state: one instance per
// TestBootstrapScenario run, passed to every step in order.
type fixture struct {
	repoRoot   string // the bundle checkout: compose build contexts
	bundleRoot string // t.TempDir(): topology.yaml, env files, pgdata
	cli        string // the built looming binary
	binDir     string

	pgUser     string
	pgPassword string

	pgEnv        string
	identityEnv  string
	gatewayEnv   string
	topologydEnv string

	topologyServiceToken string
	gatewayIdentityToken string
	bootstrapKey         string
	keyMasterKey         string // base64, 32 bytes

	upstream     *fakeUpstream
	upstreamPort int

	// memberAllowlist is empty on first boot; step 4 sets it and
	// re-applies.
	memberAllowlist string

	// dockerEnv is the child-process environment for every docker and
	// CLI invocation: the ambient environment plus DOCKER_CONFIG
	// pointed at an empty client config. The ambient docker client
	// config carries a proxy that is dead on some build hosts; both
	// BuildKit (build-time) and compose (run-time containers) would
	// inherit it and every outbound connection from a container would
	// die against it. The suite is self-contained — its docker calls
	// need no registry auth — so a clean config is strictly safer.
	dockerEnv []string

	// teardownCompose is the rendered host compose the cleanup
	// converges down.
	teardownCompose string

	// scenario outputs shared between steps.
	inviteToken        string
	adminSession       string
	memberID           string
	memberSession      string
	memberKey          string
	hostCredentialPath string
	joinedHostID       string
}

// newFixture builds the scenario fixture: docker availability (loud
// skip), the looming binary, the fake upstream, the secret env files,
// and the topology.yaml — everything first boot needs.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	requireDocker(t)

	f := &fixture{
		repoRoot:             repoRoot(t),
		bundleRoot:           t.TempDir(),
		pgUser:               "looming",
		topologyServiceToken: randomHex(t, 24),
		gatewayIdentityToken: randomHex(t, 24),
		bootstrapKey:         randomHex(t, 24),
	}
	f.pgPassword = randomHex(t, 24)
	f.keyMasterKey = base64.StdEncoding.EncodeToString(randomBytes(t, 32))
	f.binDir = t.TempDir()
	f.cli = filepath.Join(f.binDir, "looming")

	f.upstream = newFakeUpstream(t)
	f.upstreamPort = f.upstream.port

	buildCLI(t, f.repoRoot, f.cli)
	f.writeEnvFiles(t)
	f.writeDockerConfig(t)
	f.writeTopology(t, "public")

	// The CLI-rendered compose of the declared host — the channel for
	// every docker compose call the suite makes (logs, teardown).
	// Nothing is written to disk.
	f.teardownCompose = f.dryRunCompose(t)

	t.Cleanup(func() {
		if t.Failed() {
			t.Log("scenario failed — container logs follow")
			t.Log(f.composeLogs(f.teardownCompose, "gateway-front", "identityd", "topologyd", "bundle-postgres"))
		}
		// Teardown: the whole project goes down, whatever the scenario
		// reached; nothing under the project prefix may survive.
		if out, err := f.composeDown(); err != nil {
			t.Logf("teardown: compose down (best effort): %v\n%s", err, out)
		}
		assertNoStrayContainers(t)
		// The postgres container wrote its data dir as the in-container
		// postgres user; hand the tree back to the invoking user so
		// t.TempDir's RemoveAll can finish.
		f.chownPostgresData(t)
	})

	return f
}

// composeDown removes the rendered project. Best effort by contract —
// the stray-container assertion is the teardown check that counts.
func (f *fixture) composeDown() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker",
		"compose", "-p", composeProject, "-f", "-", "down", "--remove-orphans")
	cmd.Stdin = strings.NewReader(f.teardownCompose)
	cmd.Env = f.dockerEnv
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	return out.String(), cmd.Run()
}

// chownPostgresData returns the bind-mounted data dir to the invoking
// user after the container that populated it is gone.
func (f *fixture) chownPostgresData(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	uid, gid := os.Getuid(), os.Getgid()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--user", "root", "--entrypoint", "chown",
		"-v", filepath.Join(f.bundleRoot, "pgdata")+":/d",
		"postgres:16-alpine", "-R", fmt.Sprintf("%d:%d", uid, gid), "/d")
	cmd.Env = f.dockerEnv
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "chown postgres data dir back to the invoking user: %s", out)
}

// repoRoot locates the bundle checkout from this file's position
// (tests/e2e/bootstrap_test.go → two levels up), independent of the
// invoking working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	require.NoError(t, err)
	return root
}

// requireDocker loud-skips the whole scenario without a docker daemon
// — the suite IS the real-container run; there is no fallback.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("e2e: docker daemon unavailable (%v) — the bundle e2e suite drives real containers and cannot run here", err)
	}
}

// buildCLI compiles the real looming binary once per run — far cheaper
// than `go run` per invocation, and the same binary the scenario drives
// for apply, token, and join. The build runs inside the cli component
// (the polyglot layout's module boundary), not the repo root.
func buildCLI(t *testing.T, repoRoot, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/looming")
	cmd.Dir = filepath.Join(repoRoot, "cli")
	cmd.Env = os.Environ()
	outBuf, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build looming: %s", outBuf)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	return hex.EncodeToString(randomBytes(t, n))
}

// writeDockerConfig installs the empty client config the suite's
// docker children point DOCKER_CONFIG at (see the dockerEnv field).
// The user's cli-plugins (compose!) are symlinked in — plugin
// discovery scans $DOCKER_CONFIG/cli-plugins, and a bare config would
// silently lose the compose plugin.
func (f *fixture) writeDockerConfig(t *testing.T) {
	t.Helper()
	dir := filepath.Join(f.bundleRoot, "dockerconfig")
	plugins := filepath.Join(dir, "cli-plugins")
	require.NoError(t, os.MkdirAll(plugins, 0o700))
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), ".docker", "cli-plugins"),
		"/usr/local/lib/docker/cli-plugins",
		"/usr/libexec/docker/cli-plugins",
	}
	found := false
	for _, src := range candidates {
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			require.NoError(t, os.Symlink(filepath.Join(src, e.Name()), filepath.Join(plugins, e.Name())))
		}
		found = true
		break
	}
	require.True(t, found, "locate the docker cli-plugins directory (compose must be discoverable)")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}\n"), 0o600))
	f.dockerEnv = append(os.Environ(), "DOCKER_CONFIG="+dir)
}

// writeEnvFiles lays down the operator-prepared secret files — the
// phase-1 secret channel. Values never enter the rendered compose.
func (f *fixture) writeEnvFiles(t *testing.T) {
	t.Helper()
	writeEnv := func(name string, pairs ...string) string {
		path := filepath.Join(f.bundleRoot, name)
		content := new(bytes.Buffer)
		for _, p := range pairs {
			fmt.Fprintf(content, "%s\n", p)
		}
		require.NoError(t, os.WriteFile(path, content.Bytes(), 0o600))
		return path
	}
	f.pgEnv = writeEnv("postgres.env",
		"POSTGRES_USER="+f.pgUser,
		"POSTGRES_PASSWORD="+f.pgPassword)
	f.identityEnv = writeEnv("identityd.env",
		"IDENTITY_BOOTSTRAP_KEY="+f.bootstrapKey,
		"IDENTITY_GATEWAY_TOKEN="+f.gatewayIdentityToken,
		"IDENTITY_KEY_MASTER_KEY="+f.keyMasterKey)
	f.gatewayEnv = writeEnv("gateway.env",
		"GATEWAY_TOPOLOGY_TOKEN="+f.topologyServiceToken,
		"GATEWAY_IDENTITY_TOKEN="+f.gatewayIdentityToken)
	f.topologydEnv = writeEnv("topologyd.env",
		"TOPOLOGY_SERVICE_TOKEN="+f.topologyServiceToken)
}

// writeTopology renders the operator's topology.yaml. Everything the
// scenario asserts flows from THIS file: ports, placements, secrets
// channels, bootstrap section, access mode.
func (f *fixture) writeTopology(t *testing.T, accessMode string) {
	t.Helper()

	doc := fmt.Sprintf(`version: 1
access: {mode: %s, transport: direct, endpoint: "127.0.0.1"}
cluster: {name: e2e cluster}
bootstrap: {admin_email: admin@example.com}
state:
  postgres:
    image: postgres:16-alpine
    env_file: %s
    data_dir: %s
    port: %d
hosts:
  - {id: local, address: 127.0.0.1}
placements:
  - component: gateway-front
    host: local
    ports: {http: %d}
    config:
        upstream: "http://host.docker.internal:%d"
        upstream_auth: %s
        upstream_insecure: "1"
        identity_url: "http://identityd:%d"
        identity_insecure: "1"
        guide_ttl: 5s
        catalog: %q
    env_file: %s
    extra_hosts: ["host.docker.internal:host-gateway"]
  - component: identityd
    host: local
    ports: {http: %d}
    config:
        database_url: "postgres://%s:%s@bundle-postgres:5432/identity?sslmode=disable"
    env_file: %s
  - component: topologyd
    host: local
    ports: {http: %d}
    config:
        database_url: "postgres://%s:%s@bundle-postgres:5432/topology?sslmode=disable"
    env_file: %s
`,
		accessMode,
		f.pgEnv, filepath.Join(f.bundleRoot, "pgdata"), portPostgres,
		portGateway, f.upstreamPort, staticUpstreamAuth, portIdentity,
		e2eModel,
		f.gatewayEnv,
		portIdentity, f.pgUser, f.pgPassword, f.identityEnv,
		portTopology, f.pgUser, f.pgPassword, f.topologydEnv,
	)

	path := filepath.Join(f.bundleRoot, "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
}

// dryRunCompose runs the real binary's render pass and extracts the
// declared host's compose document — the suite's docker channel. The
// dry-run print shape is: a `--- <host> (<hash>) ---` header line, the
// YAML body, then the `next:` trailer; the body between them is the
// artifact.
func (f *fixture) dryRunCompose(t *testing.T) string {
	t.Helper()
	stdout, err := f.runCLI(t, 2*time.Minute,
		"apply", "--dry-run", "--config", f.topologyPath(), "--bundle-root", f.repoRoot)
	require.NoError(t, err, "apply --dry-run failed:\n%s", stdout)

	lines := strings.Split(stdout, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "--- local (") {
			start = i + 1
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "dry-run output lacks the local host artifact:\n%s", stdout)
	var body []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "next:") || strings.HasPrefix(line, "--- ") {
			break
		}
		body = append(body, line)
	}
	compose := strings.TrimSpace(strings.Join(body, "\n"))
	require.Contains(t, compose, "bundle-postgres:", "the host compose must carry the state plane:\n%s", compose)
	return compose
}

func (f *fixture) topologyPath() string { return filepath.Join(f.bundleRoot, "topology.yaml") }

// topologyURLLocal is the CLI-facing topology database URL: apply runs
// on the host, so loopback + the published port.
func (f *fixture) topologyDBURL() string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/topology?sslmode=disable", f.pgUser, f.pgPassword, portPostgres)
}

// identityBase is the operator-facing identityd base URL (host side).
func (f *fixture) identityBase() string {
	return fmt.Sprintf("http://127.0.0.1:%d", portIdentity)
}

func (f *fixture) gatewayBase() string { return fmt.Sprintf("http://127.0.0.1:%d", portGateway) }

func (f *fixture) topologydBase() string { return fmt.Sprintf("http://127.0.0.1:%d", portTopology) }

// runCLI runs the looming binary with a bounded timeout and returns
// combined stdout+stderr plus the exit error (nil on exit 0).
func (f *fixture) runCLI(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	return f.runCLIEnv(t, timeout, nil, args...)
}

// runCLIEnv is runCLI with extra environment entries (HOME redirection
// for the join step is the consumer).
func (f *fixture) runCLIEnv(t *testing.T, timeout time.Duration, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.cli, args...)
	cmd.Dir = f.repoRoot // compose build contexts resolve against the bundle root
	cmd.Env = append(append([]string{}, f.dockerEnv...), extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// composeLogs captures the tail of every named service's logs for
// failure forensics.
func (f *fixture) composeLogs(compose string, services ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := []string{"compose", "-p", composeProject, "-f", "-", "logs", "--tail", "200"}
	args = append(args, services...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader(compose)
	cmd.Env = f.dockerEnv
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run() // best effort: logs are forensics, not the assertion
	return out.String()
}

// assertNoStrayContainers pins the teardown contract: nothing prefixed
// by the compose project may survive the scenario.
func assertNoStrayContainers(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=com.docker.compose.project="+composeProject).Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)),
		"stray containers under project %q remain after teardown", composeProject)
}

// --- HTTP helpers ---

// postJSON POSTs a JSON body and returns the status plus decoded body.
func postJSON(t *testing.T, client *http.Client, url string, headers map[string]string, payload any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded) // error bodies may carry the envelope; callers assert on status when nil
	return resp.StatusCode, decoded
}

// putJSON PUTs a JSON body and returns the status plus decoded body.
func putJSON(t *testing.T, client *http.Client, url string, headers map[string]string, payload any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	return resp.StatusCode, decoded
}

// postRaw POSTs a JSON body and returns the status plus raw body —
// for assertions on payload-preserving proxies.
func postRaw(t *testing.T, client *http.Client, url string, headers map[string]string, payload any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	return resp.StatusCode, body
}

// getJSON GETs a URL and returns the status plus decoded body.
func getJSON(t *testing.T, client *http.Client, url string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	status, body := get(t, client, url, headers)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	return status, decoded
}

// tryGet is the poll-tolerant form of get: a transport error (the
// container is mid-restart or not yet accepting) reports ok=false so
// the waitFor condition retries instead of killing the test.
func tryGet(client *http.Client, url string, headers map[string]string) (status int, body []byte, ok bool) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, false
	}
	return resp.StatusCode, body, true
}

// tryPostRaw is the poll-tolerant form of postRaw (see tryGet).
func tryPostRaw(client *http.Client, url string, headers map[string]string, payload any) (status int, body []byte, ok bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, false
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, false
	}
	return resp.StatusCode, body, true
}

// get fetches a URL and returns the status and body.
func get(t *testing.T, client *http.Client, url string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	return resp.StatusCode, body
}

// waitFor polls cond until it returns true or the timeout elapses —
// containers converge at docker's pace, so assertions poll instead of
// racing. cond may fail the test only via require inside the final
// call; here it returns a bool and the step asserts afterwards.
func waitFor(t *testing.T, timeout, interval time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out (%s) waiting for %s", timeout, what)
		}
		time.Sleep(interval)
	}
}

// --- the fake upstream ---

// fakeUpstream is the OpenAI-compatible engine double the gateway
// forwards to. It runs ON THE HOST (the gateway container reaches it
// through the host-gateway alias) and asserts the southbound
// credential contract: every request must carry the STATIC fallback
// auth, because the member key is issued unprovisioned (slice C
// fallback — absence of engine_credential in the feed).
type fakeUpstream struct {
	t    *testing.T
	srv  *httptest.Server
	port int

	mu         sync.Mutex
	requests   int
	lastAuth   string
	sawModel   string
	authFailed bool
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	// Bind all interfaces, not loopback: the gateway container dials
	// the host through the bridge gateway address (host-gateway), and
	// a loopback-only listener would never see those packets. The
	// port is ephemeral and the body asserts the auth contract, so
	// the exposure is inert beyond this test run.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)

	fu := &fakeUpstream{t: t}
	fu.srv = httptest.NewUnstartedServer(http.HandlerFunc(fu.handle))
	fu.srv.Listener = ln
	fu.srv.Start()
	fu.port = ln.Addr().(*net.TCPAddr).Port

	t.Cleanup(fu.srv.Close)
	return fu
}

func (fu *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	fu.mu.Lock()
	fu.requests++
	fu.lastAuth = auth
	if auth != "Bearer "+staticUpstreamAuth {
		fu.authFailed = true
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fu.sawModel = body.Model
	fu.mu.Unlock()

	if auth != "Bearer "+staticUpstreamAuth {
		http.Error(w, "upstream auth contract violated", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{
	  "id": "chatcmpl-e2e-canned",
	  "object": "chat.completion",
	  "created": 1720000000,
	  "model": "` + e2eModel + `",
	  "choices": [{"index": 0, "message": {"role": "assistant", "content": "pong"}, "finish_reason": "stop"}],
	  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`))
}

func (fu *fakeUpstream) count() int {
	fu.mu.Lock()
	defer fu.mu.Unlock()
	return fu.requests
}

func (fu *fakeUpstream) contractOK() bool {
	fu.mu.Lock()
	defer fu.mu.Unlock()
	return !fu.authFailed && fu.sawModel == e2eModel
}
