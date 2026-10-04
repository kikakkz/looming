// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the front layer's driven implementations:
// request parsing and static config-backed stand-ins for the identity
// projection (replaced by the real cache wiring in a later slice).
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// StaticAuthenticator validates keys against a configured map — the
// slice-1 stand-in for the identity context's cache projection.
type StaticAuthenticator struct {
	Keys map[string]string // key → subject
}

func (s StaticAuthenticator) Authenticate(_ context.Context, key string) (string, error) {
	subject, ok := s.Keys[key]
	if !ok {
		return "", errors.New("unknown key")
	}
	return subject, nil
}

// StaticAllowlist serves configured per-subject model lists until the
// control-plane projection cache feeds the port.
type StaticAllowlist struct {
	ModelsBySubject map[string][]string
}

func (s StaticAllowlist) Models(_ context.Context, subject string) ([]string, error) {
	return s.ModelsBySubject[subject], nil
}

var _ port.Authenticator = StaticAuthenticator{}
var _ port.SubjectAllowlist = StaticAllowlist{}

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
