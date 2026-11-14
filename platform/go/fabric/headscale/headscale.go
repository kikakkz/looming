// SPDX-License-Identifier: Apache-2.0

package headscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	preAuthKeyPath        = "/api/v1/preauthkey"
	maxResponseBodyBytes  = int64(1 << 20)
	defaultRequestTimeout = 30 * time.Second
	defaultPreAuthKeyTTL  = 24 * time.Hour
)

var (
	ErrRequestFailed     = errors.New("headscale: api request failed")
	ErrMalformedResponse = errors.New("headscale: malformed api response")
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultRequestTimeout},
	}
}

type PreAuthKeyOpts struct {
	Reusable  bool
	Ephemeral bool
	TTL       time.Duration
}

type createPreAuthKeyRequest struct {
	User       uint64  `json:"user"`
	Reusable   bool    `json:"reusable"`
	Ephemeral  bool    `json:"ephemeral"`
	Expiration *string `json:"expiration,omitempty"`
}

type createPreAuthKeyResponse struct {
	PreAuthKey struct {
		Key string `json:"key"`
	} `json:"preAuthKey"`
}

func (c *Client) CreatePreAuthKey(ctx context.Context, userID uint64, opts PreAuthKeyOpts) (string, error) {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultPreAuthKeyTTL
	}
	expiration := time.Now().Add(ttl).UTC().Format(time.RFC3339)
	reqBody := createPreAuthKeyRequest{
		User:       userID,
		Reusable:   opts.Reusable,
		Ephemeral:  opts.Ephemeral,
		Expiration: &expiration,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("headscale: encoding preauthkey request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+preAuthKeyPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("headscale: building preauthkey request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("headscale: posting preauthkey: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return "", fmt.Errorf("headscale: reading preauthkey response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return "", fmt.Errorf("%w: status %d: %s", ErrRequestFailed, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed createPreAuthKeyResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	if parsed.PreAuthKey.Key == "" {
		return "", fmt.Errorf("%w: response carries no preAuthKey.key", ErrMalformedResponse)
	}
	return parsed.PreAuthKey.Key, nil
}
