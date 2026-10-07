// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBootstrapScenario is the bundle's end-to-end proof (topology-l1
// §3 admin bootstrap + end-user onboarding journeys): one sequential
// run through the real ctl and the real containers, each step
// asserting its own contract. Steps share the fixture; a failure
// stops the scenario (fail fast) and the cleanup still tears the
// project down.
//
// First boot is fully operator-free since #130/#131: apply brings the
// state plane up, creates every declared component database (the
// identityd placement's database), converges the containers, waits for
// identityd's HTTP surface, and prints the bootstrap invite — the
// suite no longer pre-creates the identity database or forces
// --print-invite.
func TestBootstrapScenario(t *testing.T) {
	f := newFixture(t)

	t.Run("step1_apply_first_boot", func(t *testing.T) { step1ApplyFirstBoot(t, f) })
	t.Run("step2_bootstrap_invite_admin", func(t *testing.T) { step2BootstrapInviteAdmin(t, f) })
	t.Run("step3_member_key_quota", func(t *testing.T) { step3MemberKeyQuota(t, f) })
	t.Run("step4_gateway_forward", func(t *testing.T) { step4GatewayForward(t, f) })
	t.Run("step5_guide_page_toggle", func(t *testing.T) { step5GuidePageToggle(t, f) })
	t.Run("step6_pull_join", func(t *testing.T) { step6PullJoin(t, f) })
	// Step 7 (teardown + no-stray-containers) is the fixture cleanup,
	// registered first so it runs last.
}

// step1ApplyFirstBoot runs the real `looming-ctl apply` against the
// declared topology and asserts the first-boot contract: exit 0, the
// printed one-time invite, and every container serving (identityd's
// /v1/self/login answering 400-not-401 proves the API is up, not just
// the port).
//
// The bootstrap invite lands in the FIRST apply's output (#130/#131):
// apply creates the identity database in the state-plane ensure and
// gates the invite POST on identityd's readiness, so the token below
// comes straight from stdout — no operator-side createdb, no
// --print-invite recovery.
func step1ApplyFirstBoot(t *testing.T, f *fixture) {
	stdout, err := f.runCtl(t, 10*time.Minute,
		"apply", "--config", f.topologyPath(), "--bundle-root", f.repoRoot)
	require.NoError(t, err, "first apply must converge:\n%s", stdout)
	assert.Contains(t, stdout, "host local: changed", "first boot converges the declared host:\n%s", stdout)

	client := &http.Client{Timeout: 5 * time.Second}

	f.inviteToken = inviteTokenFrom(stdout)
	require.NotEmpty(t, f.inviteToken,
		"first apply must deliver the bootstrap invite (apply creates the component databases and waits for identityd):\n%s", stdout)
	assert.Contains(t, stdout, "bootstrap invite for admin@example.com",
		"the invite step names the declared admin:\n%s", stdout)
	t.Logf("bootstrap invite printed: %s…", f.inviteToken[:8])

	waitFor(t, 90*time.Second, 2*time.Second, "identityd serving", func() bool {
		// 400-not-401: the route is live and rejects the malformed
		// body; a 401 would mean the server answered before the API
		// was wired, a refused connection means it is not up at all.
		req, _ := http.NewRequest(http.MethodPost, f.identityBase()+"/v1/self/login", strings.NewReader("{"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusBadRequest
	})

	waitFor(t, 90*time.Second, 2*time.Second, "topologyd serving", func() bool {
		// The guide endpoint answers (401 without the service token)
		// once topologyd is live.
		resp, err := client.Get(f.topologydBase() + "/v1/internal/guide")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusUnauthorized
	})

	// The gateway serving its public guide page proves the whole
	// container-side wiring at once: the renderer-derived guide URL
	// (host-gateway alias on the loopback deployment), the token
	// channel, and topologyd's guide endpoint.
	waitFor(t, 90*time.Second, 2*time.Second, "the gateway's public guide page", func() bool {
		status, body := get(t, client, f.gatewayBase()+"/", nil)
		return status == http.StatusOK && strings.Contains(string(body), "e2e cluster")
	})
}

// inviteTokenFrom extracts a token from a ctl command's printed summary
// (the apply invite line and the token-create line share the
// "  token: <value>" shape).
func inviteTokenFrom(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if token, ok := strings.CutPrefix(strings.TrimSpace(line), "token:"); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// step2BootstrapInviteAdmin registers the first admin with the printed
// invite and proves the principal active by logging in.
func step2BootstrapInviteAdmin(t *testing.T, f *fixture) {
	client := &http.Client{Timeout: 10 * time.Second}

	status, body := postJSON(t, client, f.identityBase()+"/v1/self/register", nil, map[string]any{
		"username":     "admin",
		"password":     "admin-password-1234",
		"email":        "admin@example.com",
		"invite_token": f.inviteToken,
	})
	require.Equal(t, http.StatusCreated, status, "bootstrap registration with the printed invite: %v", body)
	assert.Equal(t, "active", body["status"], "the first admin lands active: %v", body)

	status, body = postJSON(t, client, f.identityBase()+"/v1/self/login", nil, map[string]any{
		"username": "admin",
		"password": "admin-password-1234",
	})
	require.Equal(t, http.StatusOK, status, "admin login: %v", body)
	f.adminSession = body["token"].(string)
	require.NotEmpty(t, f.adminSession)
}

// step3MemberKeyQuota exercises the identity plane: the admin
// provisions a member, the member logs in and issues a key (no engine
// configured — key-only issuance per the slice C fallback), the
// masked list never shows the raw secret, and the admin-set quota is
// visible to the member.
func step3MemberKeyQuota(t *testing.T, f *fixture) {
	client := &http.Client{Timeout: 10 * time.Second}
	auth := map[string]string{"Authorization": "Bearer " + f.adminSession}

	status, body := postJSON(t, client, f.identityBase()+"/v1/admin/principals", auth, map[string]any{
		"username":     "member",
		"password":     "member-password-1234",
		"display_name": "E2E Member",
		"kind":         "human",
		"roles":        []string{"member"},
	})
	require.Equal(t, http.StatusCreated, status, "admin provisions the member: %v", body)
	assert.Equal(t, "active", body["status"], "provisioned members land active: %v", body)
	f.memberID = body["id"].(string)

	status, body = postJSON(t, client, f.identityBase()+"/v1/self/login", nil, map[string]any{
		"username": "member",
		"password": "member-password-1234",
	})
	require.Equal(t, http.StatusOK, status, "member login: %v", body)
	f.memberSession = body["token"].(string)

	memberAuth := map[string]string{"Authorization": "Bearer " + f.memberSession}
	status, body = postJSON(t, client, f.identityBase()+"/v1/self/keys", memberAuth, map[string]any{
		"name": "e2e-key",
	})
	require.Equal(t, http.StatusCreated, status, "member issues a key: %v", body)
	f.memberKey = body["key"].(string)
	require.True(t, strings.HasPrefix(f.memberKey, "lk-"), "the raw key is the only plaintext exit: %q", f.memberKey)

	status, body = getJSON(t, client, f.identityBase()+"/v1/self/keys", memberAuth)
	require.Equal(t, http.StatusOK, status, "member lists keys: %v", body)
	items := body["items"].([]any)
	require.Len(t, items, 1)
	view := items[0].(map[string]any)
	assert.Equal(t, "e2e-key", view["name"])
	assert.NotContains(t, view, "key", "the masked list never carries the secret")
	assert.NotEmpty(t, view["prefix"])
	assert.NotEmpty(t, view["last4"])

	status, body = putJSON(t, client, f.identityBase()+"/v1/admin/principals/"+f.memberID+"/quota", auth, map[string]any{
		"amount":      500,
		"unit":        "usd",
		"window_days": 30,
	})
	require.Equal(t, http.StatusOK, status, "admin sets the member quota: %v", body)
	assert.Equal(t, "usd", body["unit"])

	status, body = getJSON(t, client, f.identityBase()+"/v1/self/quota", memberAuth)
	require.Equal(t, http.StatusOK, status, "member reads its quota: %v", body)
	assert.Equal(t, "usd", body["unit"])
	assert.Equal(t, float64(500), body["amount"])
	assert.Equal(t, float64(30), body["window_days"])
}

// step4GatewayForward proves the data plane end to end: the member's
// LoomingKey authorizes a model the allowlist grants, the gateway
// forwards with the STATIC upstream auth (the key was issued
// unprovisioned — slice C fallback), the canned completion comes back,
// and a garbage key is a northbound 401 that never reaches the engine.
func step4GatewayForward(t *testing.T, f *fixture) {
	// The static allowlist keys subjects by principal id, which only
	// exists after provisioning — converge it in with a second apply
	// (this is the render-diff path: the gateway-front service changed,
	// so the container is recreated).
	f.memberAllowlist = f.memberID + "=" + e2eModel
	f.writeTopology(t, "public")
	stdout, err := f.runCtl(t, 10*time.Minute,
		"apply", "--config", f.topologyPath(), "--bundle-root", f.repoRoot)
	require.NoError(t, err, "re-apply with the member allowlist:\n%s", stdout)
	assert.Contains(t, stdout, "host local: changed", "the allowlist change converges the gateway front:\n%s", stdout)

	client := &http.Client{Timeout: 15 * time.Second}
	chat := map[string]any{
		"model":    e2eModel,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	memberHeaders := map[string]string{"Authorization": "Bearer " + f.memberKey}

	var status int
	var body []byte
	waitFor(t, 120*time.Second, 3*time.Second, "the member's forwarded chat completion", func() bool {
		var ok bool
		status, body, ok = tryPostRaw(client, f.gatewayBase()+"/v1/chat/completions", memberHeaders, chat)
		return ok && status == http.StatusOK
	})
	assert.Contains(t, string(body), "chatcmpl-e2e-canned", "the upstream's canned completion rides back: %s", body)

	assert.True(t, f.upstream.contractOK(),
		"every forwarded request carried the static upstream auth for the unprovisioned key and the allowlisted model")
	require.Equal(t, 1, f.upstream.count(), "exactly one upstream request so far")

	status, _ = postRaw(t, client, f.gatewayBase()+"/v1/chat/completions",
		map[string]string{"Authorization": "Bearer lk-garbage-garbage-garbage"}, chat)
	assert.Equal(t, http.StatusUnauthorized, status, "a garbage key is a northbound 401")
	assert.Equal(t, 1, f.upstream.count(), "the denied request never reached the engine")
}

// step5GuidePageToggle proves the guide page contract and convergence
// idempotency: the public page renders the member-facing strings, a
// private apply flips it to a bare 404, and re-applying public
// restores it — through the gateway's TTL cache (5s, set in the
// fixture) rather than a restart.
func step5GuidePageToggle(t *testing.T, f *fixture) {
	client := &http.Client{Timeout: 10 * time.Second}

	// The first page read can land while a previous converge is still
	// settling (the guide fetch is cached and single-flighted, and a
	// cold cache surfaces as a transient 503) — poll like every other
	// container-facing assertion in the suite.
	waitFor(t, 60*time.Second, 2*time.Second, "the public guide page rendering", func() bool {
		status, body, ok := tryGet(client, f.gatewayBase()+"/", nil)
		return ok && status == http.StatusOK &&
			strings.Contains(string(body), "e2e cluster") &&
			strings.Contains(string(body), "Download the CLI") &&
			strings.Contains(string(body), "/v1/self/register") &&
			strings.Contains(string(body), fmt.Sprintf("127.0.0.1:%d", portGateway))
	})

	// access.private: the compose does not change (access is guide
	// data, not service config), so apply's host line is "skipped" —
	// the flip must reach the page through the guide re-render plus
	// the TTL cache expiring, which is the freshness contract.
	f.writeTopology(t, "private")
	stdout, err := f.runCtl(t, 10*time.Minute,
		"apply", "--config", f.topologyPath(), "--bundle-root", f.repoRoot)
	require.NoError(t, err, "apply with access.private:\n%s", stdout)
	assert.Contains(t, stdout, "host local: skipped", "access alone changes no compose service:\n%s", stdout)

	waitFor(t, 60*time.Second, 2*time.Second, "the guide page going dark", func() bool {
		status, _, ok := tryGet(client, f.gatewayBase()+"/", nil)
		return ok && status == http.StatusNotFound
	})

	f.writeTopology(t, "public")
	stdout, err = f.runCtl(t, 10*time.Minute,
		"apply", "--config", f.topologyPath(), "--bundle-root", f.repoRoot)
	require.NoError(t, err, "apply back to access.public:\n%s", stdout)

	waitFor(t, 60*time.Second, 2*time.Second, "the guide page coming back", func() bool {
		status, body, ok := tryGet(client, f.gatewayBase()+"/", nil)
		return ok && status == http.StatusOK && strings.Contains(string(body), "Download the CLI")
	})
}

// step6PullJoin proves the token → join → credential → rejoin spine:
// the admin mints a one-time engine token against the state database,
// the pulling host registers (its address deliberately distinct from
// the first host's — same-IP scale-out is out of scope, see the PR
// body), the persistent credential lands 0600, and presenting it on
// the rejoin endpoint refreshes the host — the HTTP-visible proof of
// registration.
func step6PullJoin(t *testing.T, f *fixture) {
	stdout, err := f.runCtl(t, 2*time.Minute,
		"token", "create", "--role", "engine", "--ttl", "1h", "--database-url", f.topologyDBURL())
	require.NoError(t, err, "token create:\n%s", stdout)
	joinToken := inviteTokenFrom(stdout) // same "  token: <value>" print shape
	require.NotEmpty(t, joinToken)

	joinHome := t.TempDir()
	f.hostCredentialPath = filepath.Join(joinHome, "host.cred")
	stdout, err = f.runCtlEnv(t, 2*time.Minute, []string{"HOME=" + joinHome},
		"join", f.topologydBase(),
		"--token", joinToken,
		"--host-id", "engine-2",
		"--address", "10.0.0.99",
		"--cred-file", f.hostCredentialPath)
	require.NoError(t, err, "pull join:\n%s", stdout)
	assert.Contains(t, stdout, "joined as engine-2", "the join names the registered host:\n%s", stdout)

	info, err := os.Stat(f.hostCredentialPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the persistent credential is 0600")

	raw, err := os.ReadFile(f.hostCredentialPath)
	require.NoError(t, err)
	hostID, credential, ok := strings.Cut(strings.TrimSpace(string(raw)), ":")
	require.True(t, ok && hostID != "" && credential != "", "credential file holds <host-id>:<credential>: %q", raw)
	f.joinedHostID = hostID

	// The HTTP-visible registration fact: the credential
	// authenticates a rejoin (address refresh) — a host topologyd has
	// never seen answers 401.
	client := &http.Client{Timeout: 10 * time.Second}
	status, body := postJSON(t, client, f.topologydBase()+"/v1/join/rejoin",
		map[string]string{"Authorization": "Host " + hostID + ":" + credential},
		map[string]any{"address": "10.0.0.100"})
	require.Equal(t, http.StatusOK, status, "rejoin with the persisted credential: %v", body)
	assert.Equal(t, "10.0.0.100", body["address"], "the rejoin refreshed the address: %v", body)

	stdout, err = f.runCtl(t, 2*time.Minute, "token", "list", "--database-url", f.topologyDBURL())
	require.NoError(t, err, "token list:\n%s", stdout)
	assert.Contains(t, stdout, "used", "the one-time token shows consumed:\n%s", stdout)
}
