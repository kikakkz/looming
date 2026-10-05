// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/guide/app"
	"github.com/kikakkz/looming/topology/internal/guide/domain"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	topologydomain "github.com/kikakkz/looming/topology/internal/topology/domain"
)

var handlerNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type handlerGuides struct {
	guide domain.Guide
	has   bool
	err   error
}

func (f *handlerGuides) Current(context.Context) (domain.Guide, error) {
	if f.err != nil {
		return domain.Guide{}, f.err
	}
	if !f.has {
		return domain.Guide{}, domain.ErrNoGuide
	}
	return f.guide, nil
}

func (f *handlerGuides) Save(_ context.Context, g domain.Guide) error {
	f.guide, f.has = g, true
	return nil
}

type handlerTopology struct {
	current topologydomain.Topology
}

func (f *handlerTopology) Current(context.Context) (topologydomain.Topology, error) {
	return f.current, nil
}

func (f *handlerTopology) Save(context.Context, topologydomain.Topology) error { return nil }

type handlerRegistry struct{ hosts map[string]*hostdomain.Host }

func (f *handlerRegistry) Register(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	return h, nil
}

func (f *handlerRegistry) ByID(_ context.Context, id string) (*hostdomain.Host, error) {
	return f.hosts[id], nil
}

func (f *handlerRegistry) ByAddress(_ context.Context, _ string) (*hostdomain.Host, error) {
	return nil, nil
}

func (f *handlerRegistry) Update(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	return h, nil
}

// newGuideHandlerWorld wires a service over scripted fakes plus the
// HTTP handler under test.
func newGuideHandlerWorld(t *testing.T) (*handlerGuides, *app.Handler) {
	t.Helper()
	guides := &handlerGuides{}
	svc := app.NewService(guides, &handlerTopology{}, &handlerRegistry{}, func() time.Time { return handlerNow })
	return guides, app.NewHandler(svc, "service-token-1")
}

func doGET(h *app.Handler, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/internal/guide", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.Guide(rec, req)
	return rec
}

func storedGuide() domain.Guide {
	return domain.Render(domain.Facts{
		Revision:       5,
		AccessPublic:   true,
		ClusterName:    "prod cluster",
		CLIDownloadURL: "https://releases.example.com/looming",
		IdentityURL:    "http://10.0.0.12:8081",
		GatewayURL:     "http://10.0.0.11:8080",
	}, handlerNow)
}

func TestGuideEndpointServesSnapshotVerbatim(t *testing.T) {
	guides, h := newGuideHandlerWorld(t)
	guides.guide, guides.has = storedGuide(), true

	rec := doGET(h, "Bearer service-token-1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	// The wire shape is the snapshot, verbatim — the gateway consumes
	// exactly these keys.
	for _, k := range []string{"cluster_name", "access_public", "cli_download_url", "identity_url", "gateway_url", "steps", "register_hint"} {
		assert.Contains(t, body, k)
	}
	assert.Equal(t, "prod cluster", body["cluster_name"])
	assert.Equal(t, true, body["access_public"])
	assert.Len(t, body["steps"], 4)
}

func TestGuideEndpointCarriesAccessPublicFalse(t *testing.T) {
	guides, h := newGuideHandlerWorld(t)
	g := storedGuide()
	g.Snapshot.AccessPublic = false
	guides.guide, guides.has = g, true

	rec := doGET(h, "Bearer service-token-1")
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, false, body["access_public"], "the flag is not secret: the gateway decides visibility")
}

func TestGuideEndpointAuthShapes(t *testing.T) {
	guides, h := newGuideHandlerWorld(t)
	guides.guide, guides.has = storedGuide(), true

	t.Run("wrong token is 401", func(t *testing.T) {
		rec := doGET(h, "Bearer nope")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		code, _ := decodeGuideError(t, rec)
		assert.Equal(t, "unauthenticated", code)
	})
	t.Run("missing header is 401", func(t *testing.T) {
		rec := doGET(h, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("garbage scheme is 401", func(t *testing.T) {
		rec := doGET(h, "Basic service-token-1")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestGuideEndpointUnavailableWithoutServiceToken(t *testing.T) {
	svc := app.NewService(&handlerGuides{}, &handlerTopology{}, &handlerRegistry{}, func() time.Time { return handlerNow })
	h := app.NewHandler(svc, "")

	rec := doGET(h, "Bearer anything")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	code, _ := decodeGuideError(t, rec)
	assert.Equal(t, "guide_unavailable", code)
}

func TestGuideEndpointNotFoundWhenNeverRendered(t *testing.T) {
	_, h := newGuideHandlerWorld(t)
	rec := doGET(h, "Bearer service-token-1")
	require.Equal(t, http.StatusNotFound, rec.Code)
	code, _ := decodeGuideError(t, rec)
	assert.Equal(t, "not_found", code)
}

func TestGuideEndpointStoreErrorIs500(t *testing.T) {
	guides, h := newGuideHandlerWorld(t)
	guides.err = errors.New("db exploded")
	rec := doGET(h, "Bearer service-token-1")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func decodeGuideError(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	return body.Error.Code, body.Error.Message
}
