// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errModelChannel marks model-channel failures so callers can tell a transport
// or protocol error apart from a rejected proposal.
var errModelChannel = errors.New("advisor: model channel")

// DefaultModel is the request model when neither the genesis section
// nor the caller names one — OpenAI-compatible servers treat "default"
// as their configured fallback.
const DefaultModel = "default"

// ChatMessage is one OpenAI-compatible chat-completions message — the
// converged request shape the gateway and third-party endpoints serve
// (advisor-l1 §6: the channel speaks one protocol in both stages).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the OpenAI-compatible chat completions request body.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

// ChatResponse is the slice of the chat completions response the
// advisor consumes: the first choice's message content.
type ChatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

// LLMClient is the model-channel seam (advisor-l1 §6, AD-38 decision
// 7: the interface lives in the platform kit). The CLI owns the
// interaction and the lifecycle; every CI test drives a
// recorded-response fake through this seam — no model call is testable
// in CI.
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// openAIClient is the one OpenAI-compatible chat-completions transport
// both stages share: POST {endpoint}/chat/completions with the stage's
// credential as the Bearer header. The credential never appears in any
// error the client returns (Redact scrubs the rare server that echoes
// it back).
type openAIClient struct {
	endpoint string
	auth     string
	http     *http.Client
}

var _ LLMClient = (*openAIClient)(nil)

// NewGenesisClient wires the stage-1 direct channel (#143): the
// operator's genesis endpoint plus api_key. The api_key rides the wire
// — plain HTTP is accepted only for loopback endpoints (the CI fakes),
// the GATEWAY_UPSTREAM https-gate precedent; anything crossed-hosts
// must be https.
func NewGenesisClient(endpoint, apiKey string, httpClient *http.Client) (*openAIClient, error) {
	u, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%w: genesis endpoint %q must be https (the api key rides this link; loopback test endpoints exempt)", errModelChannel, endpoint)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("%w: genesis api key is empty", errModelChannel)
	}
	return newOpenAIClient(u, apiKey, httpClient), nil
}

// NewGatewayClient wires the stage-2 channel: the cluster gateway's
// OpenAI-compatible front, authenticated with the service identity's
// LoomingKey. No scheme gate stands here, the gateway binary's own
// identity-link precedent: this is the deployment's trusted-network
// link every local agent already rides (http on the LAN is the norm;
// identity validates the key).
func NewGatewayClient(endpoint, loomingKey string, httpClient *http.Client) (*openAIClient, error) {
	u, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(loomingKey) == "" {
		return nil, fmt.Errorf("%w: service LoomingKey is empty", errModelChannel)
	}
	return newOpenAIClient(u, loomingKey, httpClient), nil
}

// parseEndpoint validates the absolute http(s) base URL
// …/chat/completions is appended to.
func parseEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	switch {
	case strings.TrimSpace(endpoint) == "":
		return nil, fmt.Errorf("%w: endpoint is empty", errModelChannel)
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, fmt.Errorf("%w: endpoint %q must be an absolute http(s) URL", errModelChannel, endpoint)
	}
	return u, nil
}

func newOpenAIClient(u *url.URL, auth string, httpClient *http.Client) *openAIClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &openAIClient{endpoint: strings.TrimSuffix(u.String(), "/"), auth: auth, http: httpClient}
}

// isLoopbackHost reports whether host is the machine itself — the
// trusted-network exemption for plain-HTTP test endpoints.
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Chat posts one chat completions request and returns the first
// choice's message. A non-2xx status, an unreadable body, or a
// response without choices is one redacted error — the credential
// never leaks through the failure path.
func (c *openAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = DefaultModel
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %v", errModelChannel, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", errModelChannel, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.auth)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errModelChannel, redactErr(err, c.auth))
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", errModelChannel, redactErr(err, c.auth))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: endpoint returned %s: %s", errModelChannel, resp.Status, Redact(string(payload), c.auth))
	}
	var out ChatResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", errModelChannel, redactErr(err, c.auth))
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("%w: endpoint returned no choices", errModelChannel)
	}
	return &out, nil
}

// redactedMask replaces the secret wherever Redact puts it.
const redactedMask = "***"

// Redact replaces every occurrence of secret in s with the fixed mask
// — the output-safety guarantee for channel credentials (#143: the
// genesis key never reaches logs, session records, or operator
// output). An empty secret is a no-op so callers can redact
// unconditionally.
func Redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, redactedMask)
}

// redactErr renders err with the secret scrubbed — the error-path half
// of the redaction guarantee.
func redactErr(err error, secret string) error {
	if err == nil {
		return nil
	}
	return errors.New(Redact(err.Error(), secret))
}
