// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	hostport "github.com/kikakkz/looming/platform/go/hostport"
	app "github.com/kikakkz/looming/platform/go/joinapp"
	domain "github.com/kikakkz/looming/platform/go/joindomain"
	port "github.com/kikakkz/looming/platform/go/joinport"
	topologydomain "github.com/kikakkz/looming/platform/go/topologydomain"
	topologyport "github.com/kikakkz/looming/platform/go/topologyport"
)

var ctx = context.Background()

var fixedNow = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// scriptedRNG serves deterministic randomness.
type scriptedRNG struct {
	buf []byte
	off int
}

func (s *scriptedRNG) Read(p []byte) (int, error) {
	if s.off+len(p) > len(s.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, s.buf[s.off:])
	s.off += n
	return n, nil
}

func newRNG() *scriptedRNG {
	buf := make([]byte, 256)
	for i := range buf {
		buf[i] = byte(i + 1)
	}
	return &scriptedRNG{buf: buf}
}

// fakeTokens is the TokenStore port against memory.
type fakeTokens struct {
	byHash map[string]*domain.JoinToken
	// failMarkUsed makes the guarded consume race deterministic: the
	// pre-check passes but the guarded write loses.
	failMarkUsed bool
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{byHash: map[string]*domain.JoinToken{}}
}

func hashKey(h []byte) string { return string(h) }

func (f *fakeTokens) Create(_ context.Context, t *domain.JoinToken) error {
	cp := *t
	f.byHash[hashKey(t.TokenHash)] = &cp
	return nil
}

func (f *fakeTokens) ByHash(_ context.Context, hash []byte) (*domain.JoinToken, error) {
	if t, ok := f.byHash[hashKey(hash)]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, domain.ErrTokenNotFound
}

func (f *fakeTokens) MarkUsed(_ context.Context, hash []byte, usedAt time.Time) error {
	if f.failMarkUsed {
		return domain.ErrTokenUsed
	}
	t, ok := f.byHash[hashKey(hash)]
	if !ok || t.UsedAt != nil {
		return domain.ErrTokenUsed
	}
	t.UsedAt = &usedAt
	return nil
}

func (f *fakeTokens) List(context.Context) ([]domain.JoinToken, error) {
	var out []domain.JoinToken
	for _, t := range f.byHash {
		out = append(out, *t)
	}
	return out, nil
}

// fakeRegistry is the host Registry port against memory.
type fakeRegistry struct {
	byID      map[string]*hostdomain.Host
	byAddress map[string]*hostdomain.Host
	updateErr error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{byID: map[string]*hostdomain.Host{}, byAddress: map[string]*hostdomain.Host{}}
}

func (f *fakeRegistry) Register(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	if prev, taken := f.byAddress[h.Address]; taken && prev.ID != h.ID {
		return nil, errors.Join(hostdomain.ErrAddressTaken, errOccupant(prev.ID))
	}
	f.byID[h.ID] = h
	f.byAddress[h.Address] = h
	return h, nil
}

type occupantError string

func (e occupantError) Error() string { return "held by host " + string(e) }
func errOccupant(id string) error     { return occupantError(id) }

func (f *fakeRegistry) ByID(_ context.Context, id string) (*hostdomain.Host, error) {
	if h, ok := f.byID[id]; ok {
		cp := *h
		return &cp, nil
	}
	return nil, hostdomain.ErrNotFound
}

func (f *fakeRegistry) ByAddress(_ context.Context, address string) (*hostdomain.Host, error) {
	if h, ok := f.byAddress[address]; ok {
		cp := *h
		return &cp, nil
	}
	return nil, hostdomain.ErrNotFound
}

func (f *fakeRegistry) Update(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if prev, taken := f.byAddress[h.Address]; taken && prev.ID != h.ID {
		return nil, hostdomain.ErrAddressTaken
	}
	if old, ok := f.byID[h.ID]; ok && old.Address != h.Address {
		delete(f.byAddress, old.Address)
	}
	f.byID[h.ID] = h
	f.byAddress[h.Address] = h
	return h, nil
}

// fakeTopology is the topology Store port against memory.
type fakeTopology struct {
	current topologydomain.Topology
	err     error
}

func (f *fakeTopology) Save(context.Context, topologydomain.Topology) error { return nil }

func (f *fakeTopology) Current(context.Context) (topologydomain.Topology, error) {
	if f.err != nil {
		return topologydomain.Topology{}, f.err
	}
	return f.current, nil
}

func newService(tokens *fakeTokens, registry *fakeRegistry, topo *fakeTopology) *app.Service {
	return app.NewService(tokens, registry, topo, newRNG(), func() time.Time { return fixedNow })
}

// mint creates a usable token through the service and returns its raw form.
func mint(t *testing.T, svc *app.Service, role string) string {
	t.Helper()
	raw, _, err := svc.Mint(ctx, role, "looming", time.Hour)
	require.NoError(t, err)
	return raw
}

func TestMintHappyPath(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	raw, tok, err := svc.Mint(ctx, domain.RoleEngine, "looming", 24*time.Hour)
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.Equal(t, domain.RoleEngine, tok.Role)
	stored, err := tokens.ByHash(ctx, tok.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleEngine, stored.Role)
}

func TestMintRejectsBadRoleAndPersistsNothing(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	_, _, err := svc.Mint(ctx, "superuser", "looming", time.Hour)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidRole))
	assert.Empty(t, tokens.byHash, "a rejected mint must persist nothing")
}

func TestMintRejectsNonPositiveTTL(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	_, _, err := svc.Mint(ctx, domain.RoleEngine, "looming", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ttl")
}

func TestConsumeJoinsHostAndMarksTokenUsed(t *testing.T) {
	tokens := newFakeTokens()
	registry := newFakeRegistry()
	svc := newService(tokens, registry, &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)

	res, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21", Labels: []string{"gpu"}})
	require.NoError(t, err)
	assert.Regexp(t, `^host-[0-9a-f]{8}$`, res.Host.ID, "server-generated id shape")
	assert.Equal(t, "10.0.0.21", res.Host.Address)
	assert.Contains(t, res.Host.RoleLabels, "gpu")
	assert.Contains(t, res.Host.RoleLabels, "role=engine", "the token role is recorded on the host's labels")
	assert.NotEmpty(t, res.Credential)
	assert.True(t, res.Host.CredentialMatches(res.Credential))

	stored, err := registry.ByID(ctx, res.Host.ID)
	require.NoError(t, err)
	assert.True(t, stored.CredentialMatches(res.Credential), "the credential hash is persisted on the host")

	tok, err := tokens.ByHash(ctx, domain.HashToken(raw))
	require.NoError(t, err)
	assert.NotNil(t, tok.UsedAt, "the token is consumed exactly once")
}

func TestConsumeHonoursOperatorSuppliedID(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleWorker)
	res, err := svc.Consume(ctx, raw, app.HostInput{ID: "host-operator1", Address: "10.0.0.22"})
	require.NoError(t, err)
	assert.Equal(t, "host-operator1", res.Host.ID)
}

func TestConsumeUnknownToken(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	_, err := svc.Consume(ctx, "no-such-token", app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenNotFound))
}

func TestConsumeExpiredToken(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	raw, tok, err := svc.Mint(ctx, domain.RoleEngine, "op", time.Hour)
	require.NoError(t, err)
	tok.ExpiresAt = fixedNow.Add(-time.Minute)
	tokens.byHash[hashKey(tok.TokenHash)] = tok

	_, err = svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenExpired))
}

func TestConsumeUsedToken(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	_, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)

	_, err = svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.99"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed))
}

func TestConsumeRacedMarkUsedFails(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	tokens.failMarkUsed = true

	_, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed), "a raced guarded consume is a used-token conflict, not a 500")
}

func TestConsumeAddressConflictCarriesRecoveryHint(t *testing.T) {
	registry := newFakeRegistry()
	svc := newService(newFakeTokens(), registry, &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	first, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)

	raw2 := mint(t, svc, domain.RoleEngine)
	_, err = svc.Consume(ctx, raw2, app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, hostdomain.ErrAddressTaken))
	assert.Contains(t, err.Error(), "re-join", "the conflict names the recovery path")
	assert.Contains(t, err.Error(), first.Host.ID, "the conflict names the occupying host")

	_, err = registry.ByAddress(ctx, "10.0.0.21")
	require.NoError(t, err, "the occupant is untouched")
}

func TestConsumeIDConflictCarriesRecoveryHint(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	_, err := svc.Consume(ctx, raw, app.HostInput{ID: "host-fixed", Address: "10.0.0.21"})
	require.NoError(t, err)

	raw2 := mint(t, svc, domain.RoleEngine)
	_, err = svc.Consume(ctx, raw2, app.HostInput{ID: "host-fixed", Address: "10.0.0.22"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrHostConflict))
	assert.Contains(t, err.Error(), "re-join")
}

func TestConsumeInvalidAddressIsRejected(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	_, err := svc.Consume(ctx, raw, app.HostInput{Address: "  "})
	require.Error(t, err)
	assert.True(t, errors.Is(err, hostdomain.ErrInvalidAddress))
}

func TestConsumeEndpointHintFromGatewayFront(t *testing.T) {
	topo := &fakeTopology{current: topologydomain.Topology{
		ID:       topologydomain.SingletonID,
		Revision: 1,
		Access:   topologydomain.Access{Mode: topologydomain.ModePublic, Transport: topologydomain.TransportDirect, Endpoint: topologydomain.EndpointIP},
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentGatewayFront, HostID: "gw-host", Ports: map[string]int{"http": 8080}},
		},
	}}
	registry := newFakeRegistry()
	_, err := registry.Register(ctx, &hostdomain.Host{ID: "gw-host", Address: "10.0.0.10", JoinedAt: fixedNow})
	require.NoError(t, err)

	svc := newService(newFakeTokens(), registry, topo)
	raw := mint(t, svc, domain.RoleEngine)
	res, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)
	assert.Equal(t, "http://10.0.0.10:8080", res.Endpoint, "the hint is the gateway-front's reachable URL")
}

func TestConsumeEndpointHintEmptyWithoutTopology(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{err: topologydomain.ErrNoTopology})
	raw := mint(t, svc, domain.RoleEngine)
	res, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)
	assert.Empty(t, res.Endpoint, "no declared topology means no hint — join still succeeds")
}

func TestRejoinUpdatesAddressAndLabels(t *testing.T) {
	registry := newFakeRegistry()
	svc := newService(newFakeTokens(), registry, &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	joined, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21", Labels: []string{"gpu"}})
	require.NoError(t, err)

	newAddress := "10.0.0.31"
	updated, err := svc.Rejoin(ctx, joined.Host.ID, joined.Credential, &newAddress, []string{"gpu", "ssd"})
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.31", updated.Address)
	assert.Equal(t, []string{"gpu", "ssd"}, updated.RoleLabels)
	assert.True(t, updated.CredentialMatches(joined.Credential), "re-join never rotates the credential")

	byOld, err := registry.ByAddress(ctx, "10.0.0.21")
	assert.True(t, errors.Is(err, hostdomain.ErrNotFound), "the old address is released")
	_ = byOld
}

func TestRejoinKeepsAddressAndLabelsWhenOmitted(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	joined, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21", Labels: []string{"gpu"}})
	require.NoError(t, err)

	updated, err := svc.Rejoin(ctx, joined.Host.ID, joined.Credential, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.21", updated.Address)
	assert.Equal(t, []string{"gpu", "role=engine"}, updated.RoleLabels)
}

func TestRejoinWrongCredentialIsUnauthenticated(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	joined, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)

	_, err = svc.Rejoin(ctx, joined.Host.ID, "wrong-credential", nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrUnauthenticated))
}

func TestRejoinUnknownHostIsUnauthenticated(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	_, err := svc.Rejoin(ctx, "host-nope", "any-credential", nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrUnauthenticated), "unknown host and bad credential are indistinguishable")
}

func TestRejoinAddressConflict(t *testing.T) {
	registry := newFakeRegistry()
	svc := newService(newFakeTokens(), registry, &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	_, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)
	raw2 := mint(t, svc, domain.RoleEngine)
	second, err := svc.Consume(ctx, raw2, app.HostInput{Address: "10.0.0.22"})
	require.NoError(t, err)

	taken := "10.0.0.21"
	_, err = svc.Rejoin(ctx, second.Host.ID, second.Credential, &taken, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, hostdomain.ErrAddressTaken))
	assert.Contains(t, err.Error(), "re-join")
}

func TestListTokens(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	mint(t, svc, domain.RoleEngine)
	mint(t, svc, domain.RoleWorker)

	list, err := svc.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestRevokeSetsUsedAt(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)

	prefix := domain.HashTokenHex(raw)[:12]
	revoked, err := svc.RevokeByPrefix(ctx, prefix)
	require.NoError(t, err)
	assert.NotNil(t, revoked.UsedAt)

	// A revoked token cannot join.
	_, err = svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed))
}

func TestRevokeUnknownPrefix(t *testing.T) {
	svc := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{})
	_, err := svc.RevokeByPrefix(ctx, "deadbeef0000")
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrTokenPrefixNotFound))
}

func TestRevokeAmbiguousPrefix(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	mint(t, svc, domain.RoleEngine)
	mint(t, svc, domain.RoleWorker)

	// "" matches everything seeded so far.
	_, err := svc.RevokeByPrefix(ctx, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrTokenPrefixAmbiguous))
}

func TestRevokeAlreadyUsed(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	_, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)

	_, err = svc.RevokeByPrefix(ctx, domain.HashTokenHex(raw)[:12])
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed))
}

func TestRejoinUpdateFailurePassesThrough(t *testing.T) {
	registry := newFakeRegistry()
	svc := newService(newFakeTokens(), registry, &fakeTopology{})
	raw := mint(t, svc, domain.RoleEngine)
	joined, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)

	registry.updateErr = errors.New("database gone")
	_, err = svc.Rejoin(ctx, joined.Host.ID, joined.Credential, nil, []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database gone")
	assert.False(t, errors.Is(err, hostdomain.ErrAddressTaken))
}

func TestRevokePrefixMatchingRules(t *testing.T) {
	tokens := newFakeTokens()
	svc := newService(tokens, newFakeRegistry(), &fakeTopology{})
	mint(t, svc, domain.RoleEngine)

	// Odd-length prefixes can never match a byte-aligned hex encoding.
	_, err := svc.RevokeByPrefix(ctx, "abc")
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrTokenPrefixNotFound))

	// Longer than the hash: no match.
	_, err = svc.RevokeByPrefix(ctx, strings.Repeat("ab", 40))
	require.Error(t, err)
	assert.True(t, errors.Is(err, app.ErrTokenPrefixNotFound))

	// Uppercase hex of the same prefix matches (case-insensitive).
	list, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	tok, err := svc.RevokeByPrefix(ctx, strings.ToUpper(list[0].Prefix()))
	require.NoError(t, err)
	assert.NotNil(t, tok.UsedAt)
}

func TestEndpointHintDegradesQuietly(t *testing.T) {
	// Gateway front without its http port: empty hint, join still works.
	topo := &fakeTopology{current: topologydomain.Topology{
		ID:       topologydomain.SingletonID,
		Revision: 1,
		Access:   topologydomain.Access{Mode: topologydomain.ModePublic, Transport: topologydomain.TransportDirect, Endpoint: topologydomain.EndpointIP},
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentGatewayFront, HostID: "gw-host"},
		},
	}}
	svc := newService(newFakeTokens(), newFakeRegistry(), topo)
	raw := mint(t, svc, domain.RoleEngine)
	res, err := svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.NoError(t, err)
	assert.Empty(t, res.Endpoint)

	// Gateway front on a host the registry no longer holds.
	topo2 := &fakeTopology{current: topologydomain.Topology{
		ID:       topologydomain.SingletonID,
		Revision: 1,
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentGatewayFront, HostID: "gw-gone", Ports: map[string]int{"http": 8080}},
		},
	}}
	svc2 := newService(newFakeTokens(), newFakeRegistry(), topo2)
	raw2 := mint(t, svc2, domain.RoleEngine)
	res2, err := svc2.Consume(ctx, raw2, app.HostInput{Address: "10.0.0.22"})
	require.NoError(t, err)
	assert.Empty(t, res2.Endpoint)

	// A store failure degrades the same way.
	svc3 := newService(newFakeTokens(), newFakeRegistry(), &fakeTopology{err: errors.New("store down")})
	raw3 := mint(t, svc3, domain.RoleEngine)
	res3, err := svc3.Consume(ctx, raw3, app.HostInput{Address: "10.0.0.23"})
	require.NoError(t, err)
	assert.Empty(t, res3.Endpoint)
}

func TestConsumeCredentialMintFailureIsPropagated(t *testing.T) {
	// An rng that dies right after token consume + host id mint:
	// token consumption already happened, the error is honest.
	rng := &shortRNG{left: domain.TokenByteLen + 4}
	svc := app.NewService(newFakeTokens(), newFakeRegistry(), &fakeTopology{}, rng, func() time.Time { return fixedNow })
	raw, _, err := svc.Mint(ctx, domain.RoleEngine, "op", time.Hour)
	require.NoError(t, err)

	_, err = svc.Consume(ctx, raw, app.HostInput{Address: "10.0.0.21"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential randomness")
}

// shortRNG hands out exactly left bytes, then fails — determinism for
// the mint-failure path.
type shortRNG struct{ left int }

func (s *shortRNG) Read(p []byte) (int, error) {
	if s.left < len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	for i := range p {
		p[i] = 0x2a
	}
	s.left -= len(p)
	return len(p), nil
}

// silence unused warnings for interface assertions used implicitly.
var (
	_ port.TokenStore    = (*fakeTokens)(nil)
	_ hostport.Registry  = (*fakeRegistry)(nil)
	_ topologyport.Store = (*fakeTopology)(nil)
)
