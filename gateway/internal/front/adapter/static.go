// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the front layer's driven implementations:
// request parsing and queue/drain plumbing. Authentication is
// identity-backed (the control plane's IdentityAuthenticator
// implements the port — slice B retired the GATEWAY_KEYS stand-in) and
// model permissions ride the control plane's permission projection
// (slice D retired the GATEWAY_ALLOWLISTS stand-in).
package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// chatCompletionsRequest is the minimal body shape the extractor needs.
type chatCompletionsRequest struct {
	Model string `json:"model"`
}

// BodyModelExtractor reads the request body once, restores it for
// upstream forwarding, and returns the model id.
type BodyModelExtractor struct {
	MaxBodyBytes int64
}

var errMalformedBody = errors.New("malformed request body")

func (b BodyModelExtractor) Extract(r *http.Request) (string, error) {
	limit := b.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		return "", errMalformedBody
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	var req chatCompletionsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", errMalformedBody
	}
	return req.Model, nil
}

var _ port.ModelExtractor = BodyModelExtractor{}
