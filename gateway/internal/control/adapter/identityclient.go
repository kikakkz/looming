// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the control plane's driven implementations:
// the identity HTTP client (feed + validate) and the identity-backed
// authenticator. JSON DTOs are defined here, gateway-side — the
// no-shared-platform-lib rule (AD-34) means each component owns its
// wire shapes independently.
package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kikakkz/looming/gateway/internal/control/app"
)

// Timing contract: plain calls time out quickly; the watch uses its
// own longer deadline (watch timeout + grace) so identity's long-poll
// can hold the full window without the default timeout cutting it.
const (
	defaultClientTimeout = 15 * time.Second
	watchGrace           = 10 * time.Second
)

// feedKeyDTO / feedDTO / validateDTOs are the independently-defined
// gateway-side shapes of identity's /v1/gateway contract.
type feedKeyDTO struct {
	Hash        string `json:"hash"`
	PrincipalID string `json:"principal_id"`
	Status      string `json:"status"`
}

type feedPrincipalDTO struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type feedDTO struct {
	Rev        uint64             `json:"rev"`
	Keys       []feedKeyDTO       `json:"keys"`
	Principals []feedPrincipalDTO `json:"principals"`
}

type validateRequestDTO struct {
	Key string `json:"key"`
}

type validateResponseDTO struct {
	PrincipalID string `json:"principal_id"`
	Status      string `json:"status"`
}

// errIdentityOrigin marks every transport/decode/5xx failure from the
// identity origin: the authenticator fails closed on it, and the
// syncer backs off and retries (no silent drops).
var errIdentityOrigin = errors.New("gateway: identity origin failure")

// ErrKeyUnknown is the validate path's 404: revoked, non-active-owned,
// or never-issued. The authenticator maps it to an auth failure like
// every other origin outcome (fail closed; AD-32 northbound sameness).
var ErrKeyUnknown = fmt.Errorf("%w: key unknown or revoked", errIdentityOrigin)

// IdentityClient is the syncer's FeedSource and the authenticator's
// validate origin, over identity's service-token API.
type IdentityClient struct {
	baseURL      string
	token        string
	http         *http.Client
	watch        *http.Client
	watchTimeout time.Duration
}

// NewIdentityClient wires the client. watchTimeout is identity's
// IDENTITY_WATCH_TIMEOUT value (or the gateway-side default when the
// pair is configured together); the watch client's deadline is
// watchTimeout + watchGrace.
func NewIdentityClient(baseURL, token string, watchTimeout time.Duration) *IdentityClient {
	if watchTimeout <= 0 {
		watchTimeout = 30 * time.Second
	}
	return &IdentityClient{
		baseURL:      baseURL,
		token:        token,
		http:         &http.Client{Timeout: defaultClientTimeout},
		watch:        &http.Client{Timeout: watchTimeout + watchGrace},
		watchTimeout: watchTimeout,
	}
}

var _ app.FeedSource = (*IdentityClient)(nil)

// Snapshot fetches the current full feed projection (no watch).
func (c *IdentityClient) Snapshot(ctx context.Context) (app.FeedResponse, error) {
	return c.getFeed(ctx, c.http, "")
}

// Watch long-polls identity's blocking feed (watch=1&since_rev=N) and
// returns the current snapshot when the revision advances or the watch
// timeout elapses.
func (c *IdentityClient) Watch(ctx context.Context, sinceRev uint64) (app.FeedResponse, error) {
	return c.getFeed(ctx, c.watch, fmt.Sprintf("watch=1&since_rev=%d", sinceRev))
}

func (c *IdentityClient) getFeed(ctx context.Context, client *http.Client, query string) (app.FeedResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return app.FeedResponse{}, fmt.Errorf("%w: bad base URL: %v", errIdentityOrigin, err)
	}
	u.Path = "/v1/gateway/feed"
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return app.FeedResponse{}, fmt.Errorf("%w: build feed request: %v", errIdentityOrigin, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := client.Do(req)
	if err != nil {
		return app.FeedResponse{}, fmt.Errorf("%w: feed fetch: %v", errIdentityOrigin, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return app.FeedResponse{}, fmt.Errorf("%w: feed status %d", errIdentityOrigin, resp.StatusCode)
	}
	var dto feedDTO
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&dto); err != nil {
		return app.FeedResponse{}, fmt.Errorf("%w: feed decode: %v", errIdentityOrigin, err)
	}
	return toFeedResponse(dto)
}

// toFeedResponse maps the wire DTO onto the control-plane shape;
// malformed hash rows fail the whole response (a corrupt projection is
// worse than a retry).
func toFeedResponse(dto feedDTO) (app.FeedResponse, error) {
	out := app.FeedResponse{Rev: dto.Rev, Keys: make([]app.FeedKey, 0, len(dto.Keys))}
	for _, k := range dto.Keys {
		raw, err := base64.StdEncoding.DecodeString(k.Hash)
		if err != nil || len(raw) != 32 {
			return app.FeedResponse{}, fmt.Errorf("%w: feed key hash malformed", errIdentityOrigin)
		}
		var hash [32]byte
		copy(hash[:], raw)
		out.Keys = append(out.Keys, app.FeedKey{Hash: hash, PrincipalID: k.PrincipalID, Status: k.Status})
	}
	return out, nil
}

// Validate resolves a raw LoomingKey at the origin: 200 with the active
// principal, 404 as ErrKeyUnknown, every other failure as an
// errIdentityOrigin wrap (the caller fails closed either way).
func (c *IdentityClient) Validate(ctx context.Context, rawKey string) (string, string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", "", fmt.Errorf("%w: bad base URL: %v", errIdentityOrigin, err)
	}
	u.Path = "/v1/gateway/keys/validate"
	body, err := json.Marshal(validateRequestDTO{Key: rawKey})
	if err != nil {
		return "", "", fmt.Errorf("%w: validate encode: %v", errIdentityOrigin, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("%w: build validate request: %v", errIdentityOrigin, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: validate: %v", errIdentityOrigin, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", "", ErrKeyUnknown
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("%w: validate status %d", errIdentityOrigin, resp.StatusCode)
	}
	var dto validateResponseDTO
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&dto); err != nil {
		return "", "", fmt.Errorf("%w: validate decode: %v", errIdentityOrigin, err)
	}
	return dto.PrincipalID, dto.Status, nil
}
