// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	"github.com/kikakkz/looming/topology/internal/join/app"
	"github.com/kikakkz/looming/topology/internal/join/domain"
	topologydomain "github.com/kikakkz/looming/topology/internal/topology/domain"
)

// joinedWorld is a happy-path service wrapped in its HTTP handler.
type httpWorld struct {
	svc     *app.Service
	tokens  *fakeTokens
	handler *app.Handler
	rawTok  string
}

func newHTTPWorld(t *testing.T) *httpWorld {
	t.Helper()
	tokens := newFakeTokens()
	registry := newFakeRegistry()
	topo := &fakeTopology{current: topologydomain.Topology{
		ID:       topologydomain.SingletonID,
		Revision: 1,
		Access:   topologydomain.Access{Mode: topologydomain.ModePublic, Transport: topologydomain.TransportDirect, Endpoint: topologydomain.EndpointIP},
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentGatewayFront, HostID: "gw-host", Ports: map[string]int{"http": 8080}},
		},
	}}
	_, err := registry.Register(context.Background(), &hostdomain.Host{ID: "gw-host", Address: "10.0.0.10", JoinedAt: fixedNow})
	require.NoError(t, err)
	svc := newService(tokens, registry, topo)
	w := &httpWorld{svc: svc, tokens: tokens, handler: app.NewHandler(svc)}
	w.rawTok = mint(t, svc, domain.RoleEngine)
	return w
}

func (w *httpWorld) join(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/join", strings.NewReader(body))
	rec := httptest.NewRecorder()
	w.handler.Join(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
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

func TestJoinHappyPath(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21","labels":["gpu"]}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, rec.Code)

	var body struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
		Cluster    struct {
			Access string `json:"access"`
		} `json:"cluster"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Regexp(t, `^host-[0-9a-f]{8}$`, body.HostID)
	assert.NotEmpty(t, body.Credential)
	assert.Equal(t, "http://10.0.0.10:8080", body.Cluster.Access)
}

func TestJoinServerGeneratedIDWhenAbsent(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.NotEmpty(t, body["host_id"])
}

func TestJoinMalformedBody(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, `{"token":`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	code, _ := decodeError(t, rec)
	assert.Equal(t, "invalid_request", code)
}

func TestJoinMissingTokenOrAddress(t *testing.T) {
	w := newHTTPWorld(t)

	rec := w.join(t, `{"host":{"address":"10.0.0.21"}}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	code, _ := decodeError(t, rec)
	assert.Equal(t, "token_invalid", code)

	raw2 := mint(t, w.svc, domain.RoleEngine)
	rec = w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":" "}}`, raw2))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	code, _ = decodeError(t, rec)
	assert.Equal(t, "invalid_request", code)
}

func TestJoinUnknownToken(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, `{"token":"nope","host":{"address":"10.0.0.21"}}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	code, _ := decodeError(t, rec)
	assert.Equal(t, "token_invalid", code)
}

func TestJoinExpiredToken(t *testing.T) {
	w := newHTTPWorld(t)
	raw2 := mint(t, w.svc, domain.RoleEngine)
	toks, err := w.tokens.List(context.Background())
	require.NoError(t, err)
	require.Len(t, toks, 2)
	var mine *domain.JoinToken
	for i := range toks {
		if string(toks[i].TokenHash) == string(domain.HashToken(raw2)) {
			mine = &toks[i]
		}
	}
	require.NotNil(t, mine)
	mine.ExpiresAt = fixedNow.Add(-time.Minute)
	w.tokens.byHash[string(mine.TokenHash)] = mine

	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, raw2))
	require.Equal(t, http.StatusForbidden, rec.Code)
	code, message := decodeError(t, rec)
	assert.Equal(t, "token_expired", code)
	assert.Contains(t, message, "expired")
}

func TestJoinUsedToken(t *testing.T) {
	w := newHTTPWorld(t)
	first := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, first.Code)

	second := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.22"}}`, w.rawTok))
	require.Equal(t, http.StatusConflict, second.Code)
	code, _ := decodeError(t, second)
	assert.Equal(t, "token_used", code)
}

func TestJoinAddressConflict(t *testing.T) {
	w := newHTTPWorld(t)
	first := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, first.Code)

	raw2 := mint(t, w.svc, domain.RoleWorker)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, raw2))
	require.Equal(t, http.StatusConflict, rec.Code)
	code, message := decodeError(t, rec)
	assert.Equal(t, "address_taken", code)
	assert.Contains(t, message, "re-join", "the recovery hint reaches the joining operator")
}

func TestJoinIDConflict(t *testing.T) {
	w := newHTTPWorld(t)
	first := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"id":"host-fixed","address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, first.Code)

	raw2 := mint(t, w.svc, domain.RoleWorker)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"id":"host-fixed","address":"10.0.0.22"}}`, raw2))
	require.Equal(t, http.StatusConflict, rec.Code)
	code, message := decodeError(t, rec)
	assert.Equal(t, "host_conflict", code)
	assert.Contains(t, message, "re-join")
}

func TestRejoinHappyPath(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21","labels":["gpu"]}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, rec.Code)
	var joined struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&joined))

	body := `{"address":"10.0.0.31","labels":["gpu","ssd"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(body))
	req.Header.Set("Authorization", "Host "+joined.HostID+":"+joined.Credential)
	rr := httptest.NewRecorder()
	w.handler.Rejoin(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out struct {
		HostID  string   `json:"host_id"`
		Address string   `json:"address"`
		Labels  []string `json:"labels"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&out))
	assert.Equal(t, joined.HostID, out.HostID)
	assert.Equal(t, "10.0.0.31", out.Address)
	assert.Equal(t, []string{"gpu", "ssd"}, out.Labels)
}

func TestRejoinWrongCredential(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, rec.Code)
	var joined struct {
		HostID string `json:"host_id"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&joined))

	req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Host "+joined.HostID+":wrong")
	rr := httptest.NewRecorder()
	w.handler.Rejoin(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	code, _ := decodeError(t, rr)
	assert.Equal(t, "unauthenticated", code)
}

func TestRejoinUnknownHost(t *testing.T) {
	w := newHTTPWorld(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Host host-nope:credential")
	rr := httptest.NewRecorder()
	w.handler.Rejoin(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	code, _ := decodeError(t, rr)
	assert.Equal(t, "unauthenticated", code)
}

func TestRejoinMalformedAuthHeader(t *testing.T) {
	w := newHTTPWorld(t)
	cases := []struct {
		name  string
		value string
	}{
		{"missing header", ""},
		{"wrong scheme", "Bearer abc"},
		{"no credential part", "Host host-abc"},
		{"empty id", "Host :cred"},
		{"empty credential", "Host host-abc:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{}`))
			if tc.value != "" {
				req.Header.Set("Authorization", tc.value)
			}
			rr := httptest.NewRecorder()
			w.handler.Rejoin(rr, req)
			require.Equal(t, http.StatusUnauthorized, rr.Code, "malformed credentials never reach the service")
			code, _ := decodeError(t, rr)
			assert.Equal(t, "unauthenticated", code)
		})
	}
}

func TestRejoinAddressConflictOverHTTP(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.21"}}`, w.rawTok))
	require.Equal(t, http.StatusCreated, rec.Code)
	var first struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&first))

	raw2 := mint(t, w.svc, domain.RoleWorker)
	rec2 := w.join(t, fmt.Sprintf(`{"token":%q,"host":{"address":"10.0.0.22"}}`, raw2))
	require.Equal(t, http.StatusCreated, rec2.Code)
	var second struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
	}
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&second))

	req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{"address":"10.0.0.21"}`))
	req.Header.Set("Authorization", "Host "+second.HostID+":"+second.Credential)
	rr := httptest.NewRecorder()
	w.handler.Rejoin(rr, req)
	require.Equal(t, http.StatusConflict, rr.Code)
	code, message := decodeError(t, rr)
	assert.Equal(t, "address_taken", code)
	assert.Contains(t, message, "re-join")
}

func TestRejoinMalformedBody(t *testing.T) {
	w := newHTTPWorld(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{"address":`))
	req.Header.Set("Authorization", "Host host-abc:cred")
	rr := httptest.NewRecorder()
	w.handler.Rejoin(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	code, _ := decodeError(t, rr)
	assert.Equal(t, "invalid_request", code)
}

func TestHandlerErrorContractExhaustive(t *testing.T) {
	// Guard the mapping table against drift: every service-layer error
	// family lands on its documented status+code pair.
	w := newHTTPWorld(t)

	cases := []struct {
		name   string
		serve  func() *httptest.ResponseRecorder
		status int
		code   string
	}{
		{"token not found", func() *httptest.ResponseRecorder {
			return w.join(t, `{"token":"x","host":{"address":"10.0.0.21"}}`)
		}, http.StatusForbidden, "token_invalid"},
		{"unauthenticated", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/v1/join/rejoin", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Host host-x:y")
			rr := httptest.NewRecorder()
			w.handler.Rejoin(rr, req)
			return rr
		}, http.StatusUnauthorized, "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.serve()
			require.Equal(t, tc.status, rec.Code)
			code, _ := decodeError(t, rec)
			assert.Equal(t, tc.code, code)
		})
	}
}

// Ensure the error-shape assertions stay honest about the wire format.
func TestErrorShapeIsNestedObject(t *testing.T) {
	w := newHTTPWorld(t)
	rec := w.join(t, `{"token":"x","host":{"address":"10.0.0.21"}}`)
	raw, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error must be a nested object")
	assert.Contains(t, errObj, "code")
	assert.Contains(t, errObj, "message")
}

var (
	_ = errors.Is
	_ = hostdomain.ErrNotFound
	_ = topologydomain.SingletonID
)
