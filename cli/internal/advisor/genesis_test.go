// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"

	"github.com/kikakkz/looming/cli/internal/profile"
)

// genesisKeyFixture is the admin-provided bootstrap credential every
// test seeds; assertions grep for it to prove the redaction line.
const genesisKeyFixture = "genesis-fixture-key-0123456789"

var genesisSyncNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// fakeHome points HOME at a temp dir so the advisor state, credentials,
// and profiles never touch the developer's real ~/.looming.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// seedGenesisCredentials stores the fixture genesis key plus, when
// withAdmin, a default profile + live admin session the stage-2 flow
// needs (identity URL pointing at the given fake).
func seedGenesisCredentials(t *testing.T, identityURL string, withAdmin bool) {
	t.Helper()
	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	creds.SetGenesis(genesisKeyFixture)
	if withAdmin {
		creds.SetSession("default", profile.Session{Token: "admin-session", ExpiresAt: time.Now().Add(time.Hour)})
		require.NoError(t, creds.Save())
		require.NoError(t, profile.Save(&profile.Profile{
			Name: "default", GatewayURL: "http://gw.example.com:8080",
			IdentityURL: identityURL, CredentialKey: "default",
		}))
		return
	}
	require.NoError(t, creds.Save())
}

// genesisConfig renders a topology with a genesis section and — when
// envFile is non-empty — a gateway-front placement declaring it.
func genesisConfig(t *testing.T, endpoint, envFile string) *config.Config {
	t.Helper()
	envLine := ""
	if envFile != "" {
		envLine = "    env_file: " + envFile + "\n"
	}
	raw := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: gw-1, address: 10.0.0.11}]
placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
` + envLine + `
genesis: {endpoint: "` + endpoint + `"}
`
	cfg, err := config.Load(writeGenesisTopology(t, raw))
	require.NoError(t, err)
	return cfg
}

func writeGenesisTopology(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func stateFilePath(home string) string {
	return filepath.Join(home, advisorDirName, genesisStateFileName)
}

// TestNotifyApplyConvergedRecordsSignal: the apply hook's contract —
// a successful converge is the gateway-ready signal (#143), persisted
// for the next sync.
func TestNotifyApplyConvergedRecordsSignal(t *testing.T) {
	home := fakeHome(t)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	state, err := loadGenesisState()
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, state.Stage)
	assert.NotEmpty(t, state.ConvergedAt)
	_, err = os.Stat(stateFilePath(home))
	require.NoError(t, err)
}

func TestLoadGenesisStateRejectsCorruptFile(t *testing.T) {
	home := fakeHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, advisorDirName), 0o700))
	require.NoError(t, os.WriteFile(stateFilePath(home), []byte("{not json"), 0o600))
	_, err := loadGenesisState()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse state")
}

// TestSyncStage1WithoutConvergeStaysDirect: an empty cluster (or any
// pre-converge moment) is stage 1 — the channel calls genesis directly
// and the sync advances nothing.
func TestSyncStage1WithoutConvergeStaysDirect(t *testing.T) {
	fakeHome(t)
	seedGenesisCredentials(t, "", false)

	report, err := syncGenesis(context.Background(), genesisConfig(t, "https://genesis.example.com/v1", ""), genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, report.Stage)
	require.Len(t, report.Notes, 2)
	assert.Contains(t, report.Notes[1], "no apply converge recorded")
}

func TestSyncWithoutGenesisSectionReportsNothingToAdvance(t *testing.T) {
	fakeHome(t)
	cfg, err := config.Load(writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`))
	require.NoError(t, err)
	report, err := syncGenesis(context.Background(), cfg, genesisSyncNow)
	require.NoError(t, err)
	assert.Contains(t, report.Notes[1], "nothing to advance")
}

// TestSyncGapWithoutDeclaredEnvFile: apply converged and the key is
// stored, but the gateway-front placement declares no env_file — the
// existing surfaces cannot carry the credential to the gateway, the
// sync says so plainly, and the channel stays direct (fail safe, no
// invented convention).
func TestSyncGapWithoutDeclaredEnvFile(t *testing.T) {
	fakeHome(t)
	seedGenesisCredentials(t, "", false)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	report, err := syncGenesis(context.Background(), genesisConfig(t, "https://genesis.example.com/v1", ""), genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, report.Stage)
	assert.Contains(t, report.Notes[1], "stage 2 blocked")
	assert.Contains(t, report.Notes[1], "env_file")
}

// TestSyncWritesEnvFileAfterConverge: the converge signal seen, the
// sync lands the genesis credential in the declared env file (0600) —
// and stops there: the running gateway has not picked the file up, so
// the switch waits for a later converge.
func TestSyncWritesEnvFileAfterConverge(t *testing.T) {
	fakeHome(t)
	seedGenesisCredentials(t, "", false)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	envFile := filepath.Join(t.TempDir(), "gateway.env")
	endpoint := "https://genesis.example.com/v1"
	report, err := syncGenesis(context.Background(), genesisConfig(t, endpoint, envFile), genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, report.Stage)
	assert.Contains(t, report.Notes[1], "stage 2 prepared")

	// #nosec G304 -- the test reads the env file it just wrote.
	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.True(t, padvisor.GatewayEnvMatches(string(content), endpoint, genesisKeyFixture))
	info, err := os.Stat(envFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the env file is secret material at rest")

	state, err := loadGenesisState()
	require.NoError(t, err)
	assert.NotEmpty(t, state.EnvWrittenAt)
}

// TestSyncWithoutKeyCannotPrepareEnvFile: the credential store is the
// single source — no key, no write; the operator gets the recovery
// command, not a half-written file.
func TestSyncWithoutKeyCannotPrepareEnvFile(t *testing.T) {
	fakeHome(t)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	envFile := filepath.Join(t.TempDir(), "gateway.env")
	report, err := syncGenesis(context.Background(), genesisConfig(t, "https://genesis.example.com/v1", envFile), genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, report.Stage)
	assert.Contains(t, report.Notes[1], "genesis set")
	_, statErr := os.Stat(envFile)
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

// fakeIdentity serves the three call sites the stage-2 flow uses:
// admin provision, service login, self key issuance. Variants bend
// each endpoint for the failure-matrix tests.
func fakeIdentity(t *testing.T, bend func(kind string, w http.ResponseWriter, r *http.Request) bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/principals":
			if bend != nil && bend("provision", w, r) {
				return
			}
			if r.Header.Get("Authorization") != "Bearer admin-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": "p-svc", "username": servicePrincipalUsername, "kind": "service", "status": "active",
			})
		case "/v1/self/login":
			if bend != nil && bend("login", w, r) {
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != servicePrincipalUsername {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token": "svc-session", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/self/keys":
			if bend != nil && bend("issue", w, r) {
				return
			}
			if r.Header.Get("Authorization") != "Bearer svc-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "k-svc", "key": "lk-service-raw"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// completeSwitchFixture seeds everything stage 2 needs: admin session,
// default profile pointing at the fake identity, a converge signal,
// and — when preparedByOperator — an env file the operator already
// wrote (so no post-write converge is owed).
func completeSwitchFixture(t *testing.T, preparedByOperator bool) (*config.Config, string) {
	t.Helper()
	identityURL := fakeIdentity(t, nil)
	home := fakeHome(t)
	seedGenesisCredentials(t, identityURL, true)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	endpoint := "https://genesis.example.com/v1"
	envFile := filepath.Join(home, "gateway.env")
	if preparedByOperator {
		require.NoError(t, os.WriteFile(envFile, []byte(padvisor.RenderGatewayEnv(endpoint, genesisKeyFixture)), 0o600))
	}
	return genesisConfig(t, endpoint, envFile), envFile
}

// TestSyncCompletesSwitchWithOperatorPreparedEnvFile: the seamless
// path — the operator prepared the env file before the signalling
// converge, so one sync provisions the service identity, flips the
// stage, and erases the local genesis key. The redaction line is
// asserted file by file: the key survives ONLY in the gateway env file.
func TestSyncCompletesSwitchWithOperatorPreparedEnvFile(t *testing.T) {
	cfg, envFile := completeSwitchFixture(t, true)
	report, err := syncGenesis(context.Background(), cfg, genesisSyncNow)
	require.NoError(t, err)
	require.Equal(t, padvisor.StageGateway, report.Stage)
	assert.Contains(t, report.Notes[1], "stage 2 complete")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	// #nosec G304 -- the test reads the store file the sync wrote.
	rawCreds, err := os.ReadFile(filepath.Join(home, ".looming", "credentials.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(rawCreds), genesisKeyFixture, "erasure leaves no residue in the store")
	assert.Contains(t, string(rawCreds), "lk-service-raw", "the service LoomingKey is stored under its well-known name")

	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	_, err = creds.GenesisKey()
	require.Error(t, err, "the genesis key must be gone from the store")

	state, err := loadGenesisState()
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageGateway, state.Stage)
	assert.NotEmpty(t, state.SwitchedAt)
	assert.NotEmpty(t, state.ErasedAt)
	// #nosec G304 -- the test reads the state file the sync wrote.
	rawState, err := os.ReadFile(stateFilePath(home))
	require.NoError(t, err)
	assert.NotContains(t, string(rawState), genesisKeyFixture, "the state file never carries secrets")

	for _, note := range report.Notes {
		assert.NotContains(t, note, genesisKeyFixture, "notes never quote key material")
	}
	// #nosec G304 -- the test reads the env file the lifecycle owns.
	envContent, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Contains(t, string(envContent), genesisKeyFixture, "the env file IS the credential's stage-2 home")
}

// TestSyncCompletesAfterConvergeFollowingCLIWrite: the CLI-written env
// file owes a later converge before the switch may complete — the
// state machine's ordering gate, end to end.
func TestSyncCompletesAfterConvergeFollowingCLIWrite(t *testing.T) {
	cfg, _ := completeSwitchFixture(t, false)

	report, err := syncGenesis(context.Background(), cfg, genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, report.Stage)
	assert.Contains(t, report.Notes[1], "stage 2 prepared")

	pending, err := syncGenesis(context.Background(), cfg, genesisSyncNow.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageDirect, pending.Stage, "no converge after the write — the switch must wait")
	assert.Contains(t, pending.Notes[1], "awaited")

	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow.Add(2*time.Minute)))
	done, err := syncGenesis(context.Background(), cfg, genesisSyncNow.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageGateway, done.Stage)
}

// TestSyncRetriesErasureAfterSwitched: a previous run flipped the
// stage but died before erasing — the next sync finishes the job.
func TestSyncRetriesErasureAfterSwitched(t *testing.T) {
	cfg, _ := completeSwitchFixture(t, true)
	require.NoError(t, saveGenesisState(padvisor.GenesisState{
		Stage: padvisor.StageGateway, ConvergedAt: "2026-10-11T10:00:00Z", SwitchedAt: "2026-10-11T10:00:00Z",
	}))
	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	creds.SetGenesis(genesisKeyFixture)
	require.NoError(t, creds.Save())

	report, err := syncGenesis(context.Background(), cfg, genesisSyncNow)
	require.NoError(t, err)
	assert.Equal(t, padvisor.StageGateway, report.Stage)
	assert.Contains(t, report.Notes[1], "erased")

	state, err := loadGenesisState()
	require.NoError(t, err)
	assert.NotEmpty(t, state.ErasedAt)
	again, err := profile.LoadCredentials()
	require.NoError(t, err)
	_, err = again.GenesisKey()
	require.Error(t, err)
}

// TestSyncServicePrincipalTakenSurfacesRecovery: identity's 409 maps to
// the operator-facing recovery path, not a raw API error.
func TestSyncServicePrincipalTakenSurfacesRecovery(t *testing.T) {
	identityURL := fakeIdentity(t, func(kind string, w http.ResponseWriter, _ *http.Request) bool {
		if kind == "provision" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "username_taken", "message": "taken"},
			})
			return true
		}
		return false
	})
	fakeHome(t)
	seedGenesisCredentials(t, identityURL, true)
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	endpoint := "https://genesis.example.com/v1"
	envFile := filepath.Join(t.TempDir(), "gateway.env")
	require.NoError(t, os.WriteFile(envFile, []byte(padvisor.RenderGatewayEnv(endpoint, genesisKeyFixture)), 0o600))
	_, err := syncGenesis(context.Background(), genesisConfig(t, endpoint, envFile), genesisSyncNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovery")
	assert.Equal(t, padvisor.StageDirect, mustStateStage(t))
}

func mustStateStage(t *testing.T) padvisor.Stage {
	t.Helper()
	state, err := loadGenesisState()
	require.NoError(t, err)
	return state.Stage
}

// TestSyncPropagatesServiceFlowFailures: provision/login/issue
// failures surface named, and the lifecycle stays put.
func TestSyncPropagatesServiceFlowFailures(t *testing.T) {
	cases := []struct {
		name string
		bend func(kind string, w http.ResponseWriter, r *http.Request) bool
		want string
	}{
		{"login fails", func(kind string, w http.ResponseWriter, _ *http.Request) bool {
			if kind == "login" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid_credentials", "message": "no"}})
				return true
			}
			return false
		}, "service principal login"},
		{"issue fails", func(kind string, w http.ResponseWriter, _ *http.Request) bool {
			if kind == "issue" {
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "issue_limit", "message": "slow down"}})
				return true
			}
			return false
		}, "issue service LoomingKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identityURL := fakeIdentity(t, tc.bend)
			fakeHome(t)
			seedGenesisCredentials(t, identityURL, true)
			require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

			endpoint := "https://genesis.example.com/v1"
			envFile := filepath.Join(t.TempDir(), "gateway.env")
			require.NoError(t, os.WriteFile(envFile, []byte(padvisor.RenderGatewayEnv(endpoint, genesisKeyFixture)), 0o600))
			_, err := syncGenesis(context.Background(), genesisConfig(t, endpoint, envFile), genesisSyncNow)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, padvisor.StageDirect, mustStateStage(t))
		})
	}
}

// TestSyncServiceKeyNeedsAdminSession: no live operator session, no
// service identity — the error names the recovery (re-onboard).
func TestSyncServiceKeyNeedsAdminSession(t *testing.T) {
	identityURL := fakeIdentity(t, nil)
	fakeHome(t)
	seedGenesisCredentials(t, identityURL, false) // key but no admin session/profile
	require.NoError(t, notifyApplyConvergedAt(genesisSyncNow))

	endpoint := "https://genesis.example.com/v1"
	envFile := filepath.Join(t.TempDir(), "gateway.env")
	require.NoError(t, os.WriteFile(envFile, []byte(padvisor.RenderGatewayEnv(endpoint, genesisKeyFixture)), 0o600))
	_, err := syncGenesis(context.Background(), genesisConfig(t, endpoint, envFile), genesisSyncNow)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "onboard")
}

// TestModelChannelDirectAndGateway: the resolved channel speaks
// through whichever stage the lifecycle reached — both constructions
// serve the recorded-response fake.
func TestModelChannelDirectAndGateway(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		home := fakeHome(t)
		server := chatFake(t, "direct-answer")
		seedGenesisCredentials(t, "", false)
		cfg := genesisConfig(t, server.URL+"/v1", "")

		writeGenesisStateAt(t, home, padvisor.GenesisState{Stage: padvisor.StageDirect})
		client, err := modelChannel(cfg)
		require.NoError(t, err)
		assertChatAnswer(t, client, "direct-answer")
	})

	t.Run("gateway", func(t *testing.T) {
		home := fakeHome(t)
		server := chatFake(t, "gateway-answer")
		seedGenesisCredentials(t, server.URL, false)
		// The service LoomingKey the switched channel rides.
		creds, err := profile.LoadCredentials()
		require.NoError(t, err)
		creds.SetLoomingKey(serviceCredentialName, "lk-service-raw")
		require.NoError(t, creds.Save())
		require.NoError(t, profile.Save(&profile.Profile{
			Name: "default", GatewayURL: server.URL, IdentityURL: server.URL, CredentialKey: "default",
		}))
		writeGenesisStateAt(t, home, padvisor.GenesisState{Stage: padvisor.StageGateway})

		client, err := modelChannel(genesisConfig(t, "https://genesis.example.com/v1", ""))
		require.NoError(t, err)
		assertChatAnswer(t, client, "gateway-answer")
	})
}

func writeGenesisStateAt(t *testing.T, home string, state padvisor.GenesisState) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, advisorDirName), 0o700))
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stateFilePath(home), data, 0o600))
}

// chatFake is the recorded-response fake the LLMClient seam exists
// for — advisor-l1 §6's test pyramid rule.
func chatFake(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": answer}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertChatAnswer(t *testing.T, client padvisor.LLMClient, want string) {
	t.Helper()
	resp, err := client.Chat(context.Background(), padvisor.ChatRequest{
		Messages: []padvisor.ChatMessage{{Role: "user", Content: "propose"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Choices)
	assert.Equal(t, want, resp.Choices[0].Message.Content)
}

// TestModelChannelErrorsNameRecovery: both stages fail closed with the
// missing-artifact named.
func TestModelChannelErrorsNameRecovery(t *testing.T) {
	fakeHome(t)
	cfg := genesisConfig(t, "https://genesis.example.com/v1", "")

	_, err := modelChannel(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "genesis set")

	writeGenesisStateAt(t, mustHome(t), padvisor.GenesisState{Stage: padvisor.StageGateway})
	_, err = modelChannel(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service LoomingKey")
}

func mustHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return home
}

// TestGenesisSetCommandStoresKeyWithoutEchoing: the bootstrap input
// path — the key lands in the secret store and the confirmation names
// the endpoint only.
func TestGenesisSetCommandStoresKeyWithoutEchoing(t *testing.T) {
	fakeHome(t)
	cfgPath := writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
genesis: {endpoint: "https://genesis.example.com/v1"}
`)

	var stdout bytes.Buffer
	cmd := NewGenesis(&stdout, &bytes.Buffer{})
	cmd.SetArgs([]string{"set", "--api-key", genesisKeyFixture, "--config", cfgPath})
	require.NoError(t, cmd.Execute())

	out := stdout.String()
	assert.NotContains(t, out, genesisKeyFixture, "the confirmation never echoes the key")
	assert.Contains(t, out, "https://genesis.example.com/v1")

	creds, err := profile.LoadCredentials()
	require.NoError(t, err)
	key, err := creds.GenesisKey()
	require.NoError(t, err)
	assert.Equal(t, genesisKeyFixture, key)
}

func TestGenesisSetRejectsEmptyKeyAndMissingSection(t *testing.T) {
	fakeHome(t)
	cfgPath := writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`)

	cmd := NewGenesis(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"set", "--api-key", " ", "--config", cfgPath})
	require.Error(t, cmd.Execute())

	cmd = NewGenesis(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"set", "--api-key", genesisKeyFixture, "--config", cfgPath})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no genesis section")
}

func TestGenesisSetRefusesAfterSwitch(t *testing.T) {
	home := fakeHome(t)
	writeGenesisStateAt(t, home, padvisor.GenesisState{Stage: padvisor.StageGateway, SwitchedAt: "2026-10-11T10:00:00Z"})
	cfgPath := writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
genesis: {endpoint: "https://genesis.example.com/v1"}
`)
	cmd := NewGenesis(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"set", "--api-key", genesisKeyFixture, "--config", cfgPath})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already rides the gateway")
}

// TestGenesisSyncAndStatusCommands: the operator surface end to end —
// sync prints redacted notes, status reports stage and artifact
// presence without values.
func TestGenesisSyncAndStatusCommands(t *testing.T) {
	fakeHome(t)
	seedGenesisCredentials(t, "", false)
	cfgPath := writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
genesis: {endpoint: "https://genesis.example.com/v1"}
`)

	var syncOut bytes.Buffer
	sync := NewGenesis(&syncOut, &bytes.Buffer{})
	sync.SetArgs([]string{"sync", "--config", cfgPath})
	require.NoError(t, sync.Execute())
	assert.Contains(t, syncOut.String(), "stage 1")
	assert.NotContains(t, syncOut.String(), genesisKeyFixture)

	var statusOut bytes.Buffer
	status := NewGenesis(&statusOut, &bytes.Buffer{})
	status.SetArgs([]string{"status"})
	require.NoError(t, status.Execute())
	for _, want := range []string{"stage: direct", "last converge: never", "genesis api key in store: true", "service LoomingKey in store: false"} {
		assert.Contains(t, statusOut.String(), want)
	}
	assert.False(t, strings.Contains(statusOut.String(), genesisKeyFixture))
}
