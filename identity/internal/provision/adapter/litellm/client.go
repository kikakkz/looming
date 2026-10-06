// SPDX-License-Identifier: Apache-2.0

// Package litellm is the EngineProvisioner adapter for a LiteLLM proxy
// admin channel. The wire shapes are pinned against the LiteLLM source
// (checked 2026-10-06):
//
//   - POST /key/generate — GenerateKeyRequest{key_alias, max_budget,
//     budget_duration} → GenerateKeyResponse{key, key_alias}
//   - POST /key/update — UpdateKeyRequest{key | key_alias, ...}
//   - POST /key/delete — KeyRequest{keys | key_aliases}
//
// https://docs.litellm.ai/docs/proxy/virtual_keys
// https://github.com/BerriAI/litellm/blob/main/litellm/proxy/_types.py
//
// The httptest-faked unit tests pin this contract; a real-LiteLLM e2e
// run is part of the bundle e2e slice (#107 follow-up / release flow).
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	provisionport "github.com/kikakkz/looming/identity/internal/provision/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
)

// errSnippetMax bounds engine error bodies quoted into wrapped errors,
// so a hostile or broken engine cannot balloon logs (CWE-117 adjacent).
const errSnippetMax = 256

// Client is a LiteLLM EngineProvisioner. The master key authorizes
// against the proxy's admin channel and never leaves this adapter
// except as the Authorization header on those calls.
type Client struct {
	baseURL   string
	masterKey string
	http      *http.Client
}

// compile-time conformance: Client is an EngineProvisioner.
var _ provisionport.EngineProvisioner = (*Client)(nil)

// NewClient wires the client. httpClient nil falls back to
// http.DefaultClient; cmd injects a bounded client (AD-25: no globals
// in production wiring).
func NewClient(baseURL, masterKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		masterKey: masterKey,
		http:      httpClient,
	}
}

// generateResponse is the subset of GenerateKeyResponse this adapter
// consumes.
type generateResponse struct {
	Key      string `json:"key"`
	KeyAlias string `json:"key_alias"`
}

// Create provisions a new virtual key. The alias anchors identity:
// re-creating after a rollback lands the same alias, and update/delete
// target it — the credential value itself is never re-sent. A nil quota
// provisions an unlimited key (the no-quota-row default); a tokens unit
// has no LiteLLM mapping and fails fast without any engine call.
func (c *Client) Create(ctx context.Context, alias string, quota *quotadomain.Quota) (string, string, error) {
	body := map[string]any{"key_alias": alias}
	if quota != nil {
		budget, err := budgetFields(*quota)
		if err != nil {
			return "", "", err
		}
		body["max_budget"] = budget.maxBudget
		body["budget_duration"] = budget.duration
	}
	var resp generateResponse
	if err := c.call(ctx, http.MethodPost, "/key/generate", body, &resp); err != nil {
		return "", "", err
	}
	if resp.Key == "" {
		return "", "", fmt.Errorf("%w: generate response carried no key", provisiondomain.ErrProvisionFailed)
	}
	ref := resp.KeyAlias
	if ref == "" {
		// Older proxies may not echo the alias; the alias we sent is the
		// reference by definition.
		ref = alias
	}
	return ref, resp.Key, nil
}

// SetBudget projects a quota change onto an existing credential
// (UpdateKeyRequest targets the alias).
func (c *Client) SetBudget(ctx context.Context, ref string, quota quotadomain.Quota) error {
	budget, err := budgetFields(quota)
	if err != nil {
		return err
	}
	body := map[string]any{
		"key_alias":       ref,
		"max_budget":      budget.maxBudget,
		"budget_duration": budget.duration,
	}
	return c.call(ctx, http.MethodPost, "/key/update", body, &struct{}{})
}

// Delete removes a credential by alias (KeyRequest.key_aliases). The
// credential VALUE is never sent — the reference is sufficient.
func (c *Client) Delete(ctx context.Context, ref string) error {
	body := map[string]any{"key_aliases": []string{ref}}
	return c.call(ctx, http.MethodPost, "/key/delete", body, &struct{}{})
}

// budgetFields maps the quota vocabulary onto LiteLLM's budget fields:
// usd amounts are integer cents → float dollars (max_budget); the
// window maps to an N-day budget_duration. tokens has no LiteLLM
// mapping in phase 1 — a typed refusal, not a silent wrong number.
type budgetMapping struct {
	maxBudget float64
	duration  string
}

func budgetFields(q quotadomain.Quota) (budgetMapping, error) {
	switch q.Unit {
	case quotadomain.UnitUSD:
		return budgetMapping{maxBudget: float64(q.Amount) / 100, duration: fmt.Sprintf("%dd", q.WindowDays)}, nil
	case quotadomain.UnitTokens:
		return budgetMapping{}, fmt.Errorf("%w: tokens", provisiondomain.ErrUnsupportedUnit)
	default:
		return budgetMapping{}, fmt.Errorf("%w: unknown unit %q", provisiondomain.ErrProvisionFailed, q.Unit)
	}
}

// call posts one admin request and decodes the response. Every engine
// failure — transport, non-2xx, or undecodable body — collapses into
// the ErrProvisionFailed family so the orchestration boundary maps one
// 502 shape; details stay in the message, logged server-side.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encode %s request: %v", provisiondomain.ErrProvisionFailed, path, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: build %s request: %v", provisiondomain.ErrProvisionFailed, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.masterKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", provisiondomain.ErrProvisionFailed, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %s read response: %v", provisiondomain.ErrProvisionFailed, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: %s: status %d: %s", provisiondomain.ErrProvisionFailed, path, resp.StatusCode, snippet(raw))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %s decode response: %v", provisiondomain.ErrProvisionFailed, path, err)
	}
	return nil
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > errSnippetMax {
		s = s[:errSnippetMax] + "…"
	}
	return s
}
