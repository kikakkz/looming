// SPDX-License-Identifier: Apache-2.0

// Package identityclient is the CLI's thin client for identityd's
// self API (register/login/key/quota) — the user-face half of the
// onboarding scenario (cli-l1 §3). JSON shapes are defined here
// independently (AD-34: no cross-component imports).
package identityclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to one identityd.
type Client struct {
	base string
	http *http.Client
}

// New wires a client; base is the identity URL from the profile.
func New(base string) *Client {
	return &Client{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is the surfaced error shape; Code carries the server's error
// code for command-level handling.
type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("identity: %s (%s, http %d)", e.Code, e.Message, e.StatusCode)
}

// IsCode reports whether err is an identity Error with the given code.
func IsCode(err error, code string) bool {
	apiErr, ok := err.(*Error)
	return ok && apiErr.Code == code
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doAuth(ctx, method, path, "", body, out)
}

// doAuth attaches a Bearer token when non-empty.
func (c *Client) doAuth(ctx context.Context, method, path, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("identity: marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("identity: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("identity: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		// The error envelope nests under "error" ({error:{code,message}}).
		var envelope struct {
			Err apiError `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope)
		return &Error{StatusCode: resp.StatusCode, Code: envelope.Err.Code, Message: envelope.Err.Message}
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
			return fmt.Errorf("identity: decode response: %w", err)
		}
	}
	return nil
}

// Session is a login result.
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Login exchanges local credentials for a session token.
func (c *Client) Login(ctx context.Context, username, password string) (*Session, error) {
	var out Session
	err := c.do(ctx, http.MethodPost, "/v1/self/login",
		map[string]string{"username": username, "password": password}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// LoginExternal exchanges an OIDC ID token for a session token.
func (c *Client) LoginExternal(ctx context.Context, idToken string) (*Session, error) {
	var out Session
	err := c.do(ctx, http.MethodPost, "/v1/self/login",
		map[string]string{"id_token": idToken}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Register creates a self-registration (per the deployment's policy).
func (c *Client) Register(ctx context.Context, username, password, inviteToken string) error {
	body := map[string]string{"username": username, "password": password}
	if inviteToken != "" {
		body["invite_token"] = inviteToken
	}
	return c.do(ctx, http.MethodPost, "/v1/self/register", body, nil)
}

// IssuedKey is a freshly issued LoomingKey (raw value present).
type IssuedKey struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// IssueKey creates a LoomingKey for the session's principal.
func (c *Client) IssueKey(ctx context.Context, session *Session, name string) (*IssuedKey, error) {
	var out IssuedKey
	err := c.doAuth(ctx, http.MethodPost, "/v1/self/keys", session.Token,
		map[string]string{"name": name}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Quota is the self quota view (identity slice C).
type Quota struct {
	Amount     int64  `json:"amount"`
	Unit       string `json:"unit"`
	WindowDays int    `json:"window_days"`
}

// QuotaResponse carries the quota or its absence.
type QuotaResponse struct {
	Exists bool
	Quota  Quota
}

// SelfQuota reads the session principal's quota (404 no_quota -> Exists false).
func (c *Client) SelfQuota(ctx context.Context, session *Session) (*QuotaResponse, error) {
	var out Quota
	err := c.doAuth(ctx, http.MethodGet, "/v1/self/quota", session.Token, nil, &out)
	if IsCode(err, "no_quota") {
		return &QuotaResponse{Exists: false}, nil
	}
	if err != nil {
		return nil, err
	}
	return &QuotaResponse{Exists: true, Quota: out}, nil
}
