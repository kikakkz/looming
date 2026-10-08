// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the ctl-side caller of topologyd's join API — the joining
// host's transport for `looming join`. It speaks the v1 envelope
// contract and surfaces non-2xx answers as *Error with the server's
// code and message.
type Client struct {
	base string
	http *http.Client
}

// NewClient wires a client against a topologyd base URL, e.g.
// "http://10.0.0.11:8081". The timeout bounds a wedged first host.
func NewClient(baseURL string) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// JoinRequest is the POST /v1/join payload.
type JoinRequest struct {
	Token  string
	HostID string
	// Address is the joining host's reachable address (required).
	Address string
	Labels  []string
}

// JoinResponse is the 201 payload: the registered host's identity, its
// persistent credential (plaintext exactly once), and the cluster hint.
type JoinResponse struct {
	HostID     string
	Credential string
	Access     string
}

// Error is one non-2xx join answer: the server's code and message.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("join: topologyd answered %d %s: %s", e.Status, e.Code, e.Message)
}

// maxBodyBytes caps request and response bodies.
const maxBodyBytes = 1 << 20

// Join executes the pull-join: present the one-time token and the
// host's self-description, receive the persistent credential.
func (c *Client) Join(ctx context.Context, in JoinRequest) (*JoinResponse, error) {
	payload, err := json.Marshal(map[string]any{
		"token": in.Token,
		"host": map[string]any{
			"id":      in.HostID,
			"address": in.Address,
			"labels":  in.Labels,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("join: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/join", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("join: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("join: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("join: read response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, decodeError(resp.StatusCode, body)
	}

	var out struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
		Cluster    struct {
			Access string `json:"access"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.HostID == "" || out.Credential == "" {
		return nil, fmt.Errorf("join: topologyd answered 201 without a usable body")
	}
	return &JoinResponse{HostID: out.HostID, Credential: out.Credential, Access: out.Cluster.Access}, nil
}

// decodeError maps an error envelope onto *Error; a malformed body is
// still an *Error with the status and no detail.
func decodeError(status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error.Code == "" {
		return &Error{Status: status, Code: "unknown", Message: "no error detail"}
	}
	return &Error{Status: status, Code: env.Error.Code, Message: env.Error.Message}
}
