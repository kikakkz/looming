// SPDX-License-Identifier: Apache-2.0
package litellm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
)

// fakeEngine records requests and serves canned responses — the unit
// layer pins the wire shape against the real LiteLLM admin API
// (https://docs.litellm.ai/docs/proxy/virtual_keys, litellm/proxy/
// _types.py GenerateKeyRequest/GenerateKeyResponse/UpdateKeyRequest/
// KeyRequest).
type fakeEngine struct {
	t *testing.T

	lastMethod string
	lastPath   string
	lastAuth   string
	lastBody   map[string]any

	status   int
	response string
}

func (f *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	f.lastMethod = r.Method
	f.lastPath = r.URL.Path
	f.lastAuth = r.Header.Get("Authorization")
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		f.t.Fatalf("read request body: %v", err)
	}
	f.lastBody = map[string]any{}
	if err := json.Unmarshal(body, &f.lastBody); err != nil {
		f.t.Fatalf("request body must be JSON, got %q", body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.response))
}

func newTestClient(t *testing.T, engine *fakeEngine) (*Client, *fakeEngine) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(engine.serve))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "master-key", srv.Client()), engine
}

func usdQuota(t *testing.T, amount int64, window int) *quotadomain.Quota {
	t.Helper()
	q, err := quotadomain.NewQuota("p-1", amount, quotadomain.UnitUSD, window, "admin-1", time.Now())
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	return q
}

func TestCreateSendsGenerateRequest(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK,
		response: `{"key": "sk-litellm-value", "key_alias": "looming-k-1"}`}
	client, engine := newTestClient(t, engine)

	ref, value, err := client.Create(context.Background(), "looming-k-1", usdQuota(t, 500, 30))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ref != "looming-k-1" || value != "sk-litellm-value" {
		t.Fatalf("ref/value mismatch: %q %q", ref, value)
	}
	if engine.lastMethod != http.MethodPost || engine.lastPath != "/key/generate" {
		t.Fatalf("wire target mismatch: %s %s", engine.lastMethod, engine.lastPath)
	}
	if engine.lastAuth != "Bearer master-key" {
		t.Fatalf("master key must ride the Authorization header, got %q", engine.lastAuth)
	}
	if engine.lastBody["key_alias"] != "looming-k-1" {
		t.Fatalf("key_alias mismatch: %v", engine.lastBody)
	}
	// usd amounts are integer cents; LiteLLM max_budget is dollars.
	if engine.lastBody["max_budget"] != float64(5) {
		t.Fatalf("max_budget must be cents/100 dollars, got %v", engine.lastBody["max_budget"])
	}
	if engine.lastBody["budget_duration"] != "30d" {
		t.Fatalf("budget_duration mismatch: %v", engine.lastBody["budget_duration"])
	}
}

func TestCreateWithoutQuotaOmitsBudget(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK,
		response: `{"key": "sk-unlimited", "key_alias": "looming-k-2"}`}
	client, engine := newTestClient(t, engine)

	ref, value, err := client.Create(context.Background(), "looming-k-2", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ref != "looming-k-2" || value != "sk-unlimited" {
		t.Fatalf("ref/value mismatch: %q %q", ref, value)
	}
	if _, ok := engine.lastBody["max_budget"]; ok {
		t.Fatalf("a nil quota must provision an unlimited key, got budget %v", engine.lastBody["max_budget"])
	}
	if _, ok := engine.lastBody["budget_duration"]; ok {
		t.Fatalf("a nil quota must not set a budget window: %v", engine.lastBody)
	}
}

func TestCreateRefFallsBackToSentAlias(t *testing.T) {
	// Older proxies may not echo key_alias; the alias we sent is the
	// reference by definition (it is the update/delete target).
	engine := &fakeEngine{t: t, status: http.StatusOK, response: `{"key": "sk-v"}`}
	client, _ := newTestClient(t, engine)

	ref, _, err := client.Create(context.Background(), "looming-k-3", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ref != "looming-k-3" {
		t.Fatalf("ref must fall back to the sent alias, got %q", ref)
	}
}

func TestCreateRejectsTokensUnitWithoutCallingEngine(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK, response: `{"key": "sk-x"}`}
	client, engine := newTestClient(t, engine)

	tokens, err := quotadomain.NewQuota("p-1", 100, quotadomain.UnitTokens, 30, "admin-1", time.Now())
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	_, _, createErr := client.Create(context.Background(), "looming-k-4", tokens)
	if !errors.Is(createErr, provisiondomain.ErrUnsupportedUnit) {
		t.Fatalf("want ErrUnsupportedUnit, got %v", createErr)
	}
	if !errors.Is(createErr, provisiondomain.ErrProvisionFailed) {
		t.Fatal("ErrUnsupportedUnit must refine ErrProvisionFailed")
	}
	if engine.lastPath != "" {
		t.Fatalf("an unsupported unit must fail before any engine call, saw %s", engine.lastPath)
	}
}

func TestCreateMapsEngineFailuresToTypedErrors(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		response string
	}{
		{"5xx", http.StatusInternalServerError, `{"error": {"message": "boom"}}`},
		{"4xx", http.StatusBadRequest, `{"error": {"message": "bad alias"}}`},
		{"malformed json", http.StatusOK, `not-json`},
		{"missing key", http.StatusOK, `{"key_alias": "looming-k-1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeEngine{t: t, status: tc.status, response: tc.response}
			client, _ := newTestClient(t, engine)
			_, _, err := client.Create(context.Background(), "looming-k-1", nil)
			if !errors.Is(err, provisiondomain.ErrProvisionFailed) {
				t.Fatalf("want ErrProvisionFailed, got %v", err)
			}
		})
	}
}

func TestSetBudgetSendsUpdateRequest(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK, response: `{}`}
	client, engine := newTestClient(t, engine)

	err := client.SetBudget(context.Background(), "looming-k-1", *usdQuota(t, 700, 90))
	if err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	if engine.lastMethod != http.MethodPost || engine.lastPath != "/key/update" {
		t.Fatalf("wire target mismatch: %s %s", engine.lastMethod, engine.lastPath)
	}
	if engine.lastBody["key_alias"] != "looming-k-1" {
		t.Fatalf("UpdateKeyRequest targets by key_alias, got %v", engine.lastBody)
	}
	if engine.lastBody["max_budget"] != float64(7) || engine.lastBody["budget_duration"] != "90d" {
		t.Fatalf("budget fields mismatch: %v", engine.lastBody)
	}
}

func TestSetBudgetRejectsTokensUnit(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK, response: `{}`}
	client, engine := newTestClient(t, engine)
	tokens, _ := quotadomain.NewQuota("p-1", 100, quotadomain.UnitTokens, 1, "admin-1", time.Now())

	if err := client.SetBudget(context.Background(), "ref", *tokens); !errors.Is(err, provisiondomain.ErrUnsupportedUnit) {
		t.Fatalf("want ErrUnsupportedUnit, got %v", err)
	}
	if engine.lastPath != "" {
		t.Fatalf("an unsupported unit must fail before any engine call, saw %s", engine.lastPath)
	}
}

func TestSetBudgetMapsFailures(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusBadGateway, response: `bad`}
	client, _ := newTestClient(t, engine)
	if err := client.SetBudget(context.Background(), "ref", *usdQuota(t, 1, 1)); !errors.Is(err, provisiondomain.ErrProvisionFailed) {
		t.Fatalf("want ErrProvisionFailed, got %v", err)
	}
}

func TestDeleteSendsDeleteRequest(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusOK, response: `{}`}
	client, engine := newTestClient(t, engine)

	if err := client.Delete(context.Background(), "looming-k-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if engine.lastMethod != http.MethodPost || engine.lastPath != "/key/delete" {
		t.Fatalf("wire target mismatch: %s %s", engine.lastMethod, engine.lastPath)
	}
	// KeyRequest deletes by keys or key_aliases; the reference is an
	// alias, never the credential value.
	aliases, ok := engine.lastBody["key_aliases"].([]any)
	if !ok || len(aliases) != 1 || aliases[0] != "looming-k-1" {
		t.Fatalf("delete must target key_aliases, got %v", engine.lastBody)
	}
	if _, ok := engine.lastBody["keys"]; ok {
		t.Fatalf("the credential value must never be sent to the engine, got %v", engine.lastBody)
	}
}

func TestDeleteMapsFailures(t *testing.T) {
	engine := &fakeEngine{t: t, status: http.StatusServiceUnavailable, response: `down`}
	client, _ := newTestClient(t, engine)
	if err := client.Delete(context.Background(), "ref"); !errors.Is(err, provisiondomain.ErrProvisionFailed) {
		t.Fatalf("want ErrProvisionFailed, got %v", err)
	}
}
