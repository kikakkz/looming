// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	hostport "github.com/kikakkz/looming/topology/internal/host/port"
	joinapp "github.com/kikakkz/looming/topology/internal/join/app"
	"github.com/kikakkz/looming/topology/internal/join/domain"
	joinport "github.com/kikakkz/looming/topology/internal/join/port"
	topologydomain "github.com/kikakkz/looming/topology/internal/topology/domain"
	topologyport "github.com/kikakkz/looming/topology/internal/topology/port"
)

// stubTokens is the TokenStore port against memory — just enough surface
// for the mux smoke test.
type stubTokens struct {
	tok *domain.JoinToken
}

func (s *stubTokens) Create(_ context.Context, t *domain.JoinToken) error {
	s.tok = t
	return nil
}

func (s *stubTokens) ByHash(_ context.Context, hash []byte) (*domain.JoinToken, error) {
	if s.tok == nil || string(s.tok.TokenHash) != string(hash) {
		return nil, domain.ErrTokenNotFound
	}
	cp := *s.tok
	return &cp, nil
}

func (s *stubTokens) MarkUsed(_ context.Context, _ []byte, usedAt time.Time) error {
	if s.tok.UsedAt != nil {
		return domain.ErrTokenUsed
	}
	s.tok.UsedAt = &usedAt
	return nil
}

func (s *stubTokens) List(context.Context) ([]domain.JoinToken, error) { return nil, nil }

// stubRegistry is the host Registry port against memory.
type stubRegistry struct {
	byID map[string]*hostdomain.Host
}

func newStubRegistry() *stubRegistry {
	return &stubRegistry{byID: map[string]*hostdomain.Host{}}
}

func (s *stubRegistry) Register(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	s.byID[h.ID] = h
	return h, nil
}

func (s *stubRegistry) ByID(_ context.Context, id string) (*hostdomain.Host, error) {
	if h, ok := s.byID[id]; ok {
		cp := *h
		return &cp, nil
	}
	return nil, hostdomain.ErrNotFound
}

func (s *stubRegistry) ByAddress(context.Context, string) (*hostdomain.Host, error) {
	return nil, hostdomain.ErrNotFound
}

func (s *stubRegistry) Update(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	s.byID[h.ID] = h
	return h, nil
}

// stubTopology reports no declared topology — the hint degrades empty.
type stubTopology struct{}

func (stubTopology) Save(context.Context, topologydomain.Topology) error { return nil }
func (stubTopology) Current(context.Context) (topologydomain.Topology, error) {
	return topologydomain.Topology{}, topologydomain.ErrNoTopology
}

type countingRNG struct{ left int }

func (c *countingRNG) Read(p []byte) (int, error) {
	if c.left < len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	for i := range p {
		p[i] = 0x5a
	}
	c.left -= len(p)
	return len(p), nil
}

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	t.Setenv(databaseURLEnv, "postgres://topology:topology@127.0.0.1:5432/topology?sslmode=disable")
	t.Setenv(listenEnv, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig with database URL: %v", err)
	}
	if cfg.listen != defaultListen {
		t.Fatalf("listen must default to %q, got %q", defaultListen, cfg.listen)
	}

	t.Setenv(listenEnv, ":9999")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("loadConfig with explicit listen: %v", err)
	}
	if cfg.listen != ":9999" {
		t.Fatalf("listen must honor %s, got %q", listenEnv, cfg.listen)
	}
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	t.Setenv(databaseURLEnv, "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("a missing TOPOLOGY_DATABASE_URL must fail fast")
	}
}

func TestRouteMuxServesJoinAndRejoin(t *testing.T) {
	tokens := &stubTokens{}
	svc := joinapp.NewService(tokens, newStubRegistry(), stubTopology{}, &countingRNG{left: 1024},
		func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) })
	server := httptest.NewServer(routeMux(joinapp.NewHandler(svc)))
	defer server.Close()

	// Mint straight through the service (the admin-side path) so the
	// consume below has a real token row.
	raw, _, err := svc.Mint(context.Background(), domain.RoleEngine, "test", time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/join", "application/json",
		strings.NewReader(`{"token":"`+raw+`","host":{"address":"10.0.0.21","labels":["gpu"]}}`))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("join must return 201, got %d", resp.StatusCode)
	}
	var joined struct {
		HostID     string `json:"host_id"`
		Credential string `json:"credential"`
	}
	if decErr := json.NewDecoder(resp.Body).Decode(&joined); decErr != nil {
		t.Fatalf("decode join response: %v", decErr)
	}
	if joined.HostID == "" || joined.Credential == "" {
		t.Fatalf("join response must carry host_id and credential: %+v", joined)
	}

	rejoin, err := http.NewRequest(http.MethodPost, server.URL+"/v1/join/rejoin",
		strings.NewReader(`{"labels":["gpu","ssd"]}`))
	if err != nil {
		t.Fatalf("build rejoin: %v", err)
	}
	rejoin.Header.Set("Authorization", "Host "+joined.HostID+":"+joined.Credential)
	rejoinResp, err := http.DefaultClient.Do(rejoin)
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	defer func() { _ = rejoinResp.Body.Close() }()
	if rejoinResp.StatusCode != http.StatusOK {
		t.Fatalf("rejoin must return 200, got %d", rejoinResp.StatusCode)
	}
}

func TestRouteMuxUnknownRoute(t *testing.T) {
	svc := joinapp.NewService(&stubTokens{}, newStubRegistry(), stubTopology{}, &countingRNG{left: 1024}, time.Now)
	server := httptest.NewServer(routeMux(joinapp.NewHandler(svc)))
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route must 404, got %d", resp.StatusCode)
	}
}

// Compile-time guards that the stubs really implement the ports.
var (
	_ joinport.TokenStore = (*stubTokens)(nil)
	_ hostport.Registry   = (*stubRegistry)(nil)
	_ topologyport.Store  = stubTopology{}
	_                     = sha256.Size
	_                     = errors.Is
)
