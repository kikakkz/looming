// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedChatResponse is the CI fake's canned body — the recorded
// response shape every model test drives (advisor-l1 §6: no model call
// is testable in CI).
const recordedChatResponse = `{
  "choices": [{"message": {"role": "assistant", "content": "place postgres on gw-1"}}]
}`

// TestGenesisClientChatPostsOpenAICompletions: the stage-1 channel
// speaks the converged protocol — POST {endpoint}/chat/completions,
// Bearer auth, OpenAI request body, first choice's content back.
func TestGenesisClientChatPostsOpenAICompletions(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	var gotMessages []ChatMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model    string        `json:"model"`
			Messages []ChatMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		gotModel, gotMessages = body.Model, body.Messages
		_, _ = io.WriteString(w, recordedChatResponse)
	}))
	defer server.Close()

	client, err := NewGenesisClient(server.URL+"/v1", "genesis-test-key", server.Client())
	require.NoError(t, err)
	resp, err := client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "propose placements"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Choices)
	assert.Equal(t, "place postgres on gw-1", resp.Choices[0].Message.Content)

	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Equal(t, "Bearer genesis-test-key", gotAuth)
	assert.Equal(t, DefaultModel, gotModel, "an empty request model falls back to the channel default")
	require.Len(t, gotMessages, 1)
	assert.Equal(t, "user", gotMessages[0].Role)
}

// TestGenesisClientHTTPSGate: the api key rides the wire — https is
// required for crossed-hosts endpoints; loopback (the CI fakes) is the
// trusted-network exemption, the GATEWAY_UPSTREAM precedent.
func TestGenesisClientHTTPSGate(t *testing.T) {
	if _, err := NewGenesisClient("http://genesis.example.com/v1", "k", nil); err == nil {
		t.Fatal("plain http for a crossed-hosts endpoint must fail")
	} else {
		assert.Contains(t, err.Error(), "must be https")
	}
	if _, err := NewGenesisClient("http://127.0.0.1:1234/v1", "k", nil); err != nil {
		t.Fatalf("loopback http is the test exemption: %v", err)
	}
	if _, err := NewGenesisClient("https://genesis.example.com/v1", "k", nil); err != nil {
		t.Fatalf("https endpoint must construct: %v", err)
	}
	for _, bad := range []string{"", "genesis.example.com", "/v1", "ftp://x/v1"} {
		if _, err := NewGenesisClient(bad, "k", nil); err == nil {
			t.Fatalf("endpoint %q must fail construction", bad)
		}
	}
	if _, err := NewGenesisClient("https://genesis.example.com/v1", "  ", nil); err == nil {
		t.Fatal("an empty api key must fail construction")
	}
}

// TestGenesisClientErrorRedactsEchoedKey: a misbehaving endpoint that
// echoes the bearer key back in its error body must not leak it into
// the returned error — #143's redaction line.
func TestGenesisClientErrorRedactsEchoedKey(t *testing.T) {
	const key = "genesis-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized key `+key+`"}`)
	}))
	defer server.Close()

	client, err := NewGenesisClient(server.URL, key, server.Client())
	require.NoError(t, err)
	_, err = client.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), key, "the echoed api key must be scrubbed")
	assert.Contains(t, err.Error(), "***")
	assert.Contains(t, err.Error(), "401")
}

// TestGenesisClientProtocolFailures: a non-JSON body and a choices-less
// body are protocol errors, not panics or empty successes.
func TestGenesisClientProtocolFailures(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{not json`)
		}))
		defer server.Close()
		client, err := NewGenesisClient(server.URL, "k", server.Client())
		require.NoError(t, err)
		_, err = client.Chat(context.Background(), ChatRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode response")
	})
	t.Run("no choices", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices": []}`)
		}))
		defer server.Close()
		client, err := NewGenesisClient(server.URL, "k", server.Client())
		require.NoError(t, err)
		_, err = client.Chat(context.Background(), ChatRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no choices")
	})
}

// TestGatewayClientTrustedNetworkLink: the stage-2 channel rides the
// deployment's trusted-network link (http on the LAN is the norm for
// gateway traffic), so no https gate stands; the LoomingKey is the
// bearer and gets the same redaction discipline.
func TestGatewayClientTrustedNetworkLink(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, recordedChatResponse)
	}))
	defer server.Close()

	client, err := NewGatewayClient(server.URL+"/v1", "lk-service-key", server.Client())
	require.NoError(t, err)
	_, err = client.Chat(context.Background(), ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	require.NoError(t, err)
	assert.Equal(t, "Bearer lk-service-key", gotAuth)

	if _, err := NewGatewayClient("http://10.0.0.11:8080/v1", "", nil); err == nil {
		t.Fatal("an empty LoomingKey must fail construction")
	}
}

// TestRedact: the scrubbing primitive — empty secret is a no-op, every
// occurrence is masked, surrounding text survives.
func TestRedact(t *testing.T) {
	assert.Equal(t, "untouched", Redact("untouched", ""))
	assert.Equal(t, "*** did ***", Redact("key did key", "key"))
	assert.Equal(t, "no match", Redact("no match", "absent"))
}
