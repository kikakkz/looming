// SPDX-License-Identifier: Apache-2.0

// Package app is the join capability's use-case layer: the pull-join
// flow (topology-l1 §3 adding-a-host journey) — token mint for the
// admin side, token consume + host registration for the joining side,
// and the credential-authenticated re-join that refreshes a host's
// address/labels. T2's decisions live here; the host capability stays a
// plain registry.
package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	hostport "github.com/kikakkz/looming/platform/go/hostport"
	domain "github.com/kikakkz/looming/platform/go/joindomain"
	port "github.com/kikakkz/looming/platform/go/joinport"
	topologydomain "github.com/kikakkz/looming/platform/go/topologydomain"
	topologyport "github.com/kikakkz/looming/platform/go/topologyport"
)

// Use-case sentinels. ErrUnauthenticated deliberately covers both the
// unknown-host and wrong-credential cases — re-join must not leak which
// half failed (topology-l1 §5 Host row). ErrHostConflict marks a join
// whose operator-supplied host id is already registered: like the
// address conflict, recovery is re-join with the existing credential.
var (
	ErrUnauthenticated = errors.New("topology: invalid host id or credential")
	ErrHostConflict    = errors.New("topology: host id already registered")
	// ErrTokenPrefixNotFound / ErrTokenPrefixAmbiguous mark a revoke
	// whose prefix matches nothing or more than one token — the admin
	// must sharpen the prefix instead of revoking blindly.
	ErrTokenPrefixNotFound  = errors.New("topology: no join token matches prefix")
	ErrTokenPrefixAmbiguous = errors.New("topology: join token prefix is ambiguous")
)

// gatewayFrontPort is the named placement port the access hint reads —
// the same contract key the renderer's gateway-front contract declares.
const gatewayFrontPort = "http"

// Service orchestrates join-token mint/consume and re-join over the
// token store, the host registry, and the topology snapshot (the access
// endpoint hint). rng and clock are injected (AD-25).
type Service struct {
	tokens   port.TokenStore
	registry hostport.Registry
	topology topologyport.Store
	rng      io.Reader
	clock    func() time.Time
}

// NewService wires the service.
func NewService(tokens port.TokenStore, registry hostport.Registry, topology topologyport.Store, rng io.Reader, clock func() time.Time) *Service {
	return &Service{tokens: tokens, registry: registry, topology: topology, rng: rng, clock: clock}
}

// Mint creates a join token for the admin side. The raw token is
// returned exactly once — the caller prints it and never persists it.
func (s *Service) Mint(ctx context.Context, role, createdBy string, ttl time.Duration) (raw string, tok *domain.JoinToken, err error) {
	raw, tok, err = domain.GenerateToken(role, createdBy, ttl, s.rng, s.clock())
	if err != nil {
		return "", nil, err
	}
	if err := s.tokens.Create(ctx, tok); err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}

// List returns every join token for the admin-side `token list`.
func (s *Service) List(ctx context.Context) ([]domain.JoinToken, error) {
	return s.tokens.List(ctx)
}

// RevokeByPrefix revokes the token whose hash hex starts with prefix.
// Zero or multiple matches fail with ErrTokenPrefixNotFound /
// ErrTokenPrefixAmbiguous; an already-used token fails with
// domain.ErrTokenUsed (revoke consumes the token — the guarded write
// backstops the check under concurrency).
func (s *Service) RevokeByPrefix(ctx context.Context, prefix string) (*domain.JoinToken, error) {
	tokens, err := s.tokens.List(ctx)
	if err != nil {
		return nil, err
	}
	var (
		match *domain.JoinToken
		count int
	)
	for i := range tokens {
		if hasHexPrefix(tokens[i].TokenHash, prefix) {
			match = &tokens[i]
			count++
		}
	}
	switch {
	case count == 0:
		return nil, fmt.Errorf("%w: %q", ErrTokenPrefixNotFound, prefix)
	case count > 1:
		return nil, fmt.Errorf("%w: %q matches %d tokens", ErrTokenPrefixAmbiguous, prefix, count)
	}
	if err := s.tokens.MarkUsed(ctx, match.TokenHash, s.clock()); err != nil {
		return nil, err
	}
	match.UsedAt = timePtr(s.clock())
	return match, nil
}

// HostInput is the joining host's self-description: an optional
// operator-supplied id (the server mints host-<uuid8> when absent), its
// reachable address, and free-form labels.
type HostInput struct {
	ID      string
	Address string
	Labels  []string
}

// JoinResult is one successful join: the registered host (with its
// freshly minted, already-persisted credential hash), the plaintext
// credential returned exactly once, and the cluster access endpoint
// hint (empty when no topology is declared).
type JoinResult struct {
	Host       *hostdomain.Host
	Credential string
	Endpoint   string
}

// Consume is the pull-join use case: validate and atomically consume
// the token, register the host with its fresh persistent credential,
// and answer with the cluster hint. Error contract: domain.ErrTokenNotFound,
// domain.ErrTokenExpired, domain.ErrTokenUsed (raced consumes included),
// hostdomain.ErrAddressTaken / ErrHostConflict (both carry the re-join
// recovery hint), hostdomain.ErrInvalidAddress. Consumption is
// irreversible: a host-validation or registration failure after the
// guarded consume does not restore the token — the retry presents a
// fresh one (the one-time invariant, topology-l1 §5).
func (s *Service) Consume(ctx context.Context, raw string, in HostInput) (*JoinResult, error) {
	tok, err := s.tokens.ByHash(ctx, domain.HashToken(raw))
	if err != nil {
		return nil, err
	}
	if err = tok.Consume(s.clock()); err != nil {
		return nil, err
	}
	if err = s.tokens.MarkUsed(ctx, tok.TokenHash, s.clock()); err != nil {
		return nil, err
	}

	hostID := in.ID
	if hostID == "" {
		hostID, err = domain.GenerateHostID(s.rng)
		if err != nil {
			return nil, err
		}
	} else if _, lookupErr := s.registry.ByID(ctx, hostID); lookupErr == nil {
		return nil, fmt.Errorf("%w: %q belongs to a registered host; re-join with its existing credential", ErrHostConflict, hostID)
	} else if !errors.Is(lookupErr, hostdomain.ErrNotFound) {
		return nil, lookupErr
	}

	credential, hash, err := hostdomain.GenerateCredential(s.rng)
	if err != nil {
		return nil, err
	}
	labels := append(append([]string(nil), in.Labels...), "role="+tok.Role)
	host, err := hostdomain.NewHost(hostID, in.Address, labels, s.clock())
	if err != nil {
		return nil, err
	}
	host.CredentialHash = hash
	if _, err := s.registry.Register(ctx, host); err != nil {
		return nil, s.recoveryHint(err)
	}
	return &JoinResult{Host: host, Credential: credential, Endpoint: s.endpointHint(ctx)}, nil
}

// Rejoin authenticates a registered host by its persistent credential
// and refreshes its address/labels: address nil keeps the current one;
// labels nil keeps the current set (an empty non-nil slice clears it).
// Wrong credential and unknown host both fail with ErrUnauthenticated;
// an address move onto another host's address fails with
// hostdomain.ErrAddressTaken (recovery hint attached).
func (s *Service) Rejoin(ctx context.Context, hostID, credential string, address *string, labels []string) (*hostdomain.Host, error) {
	host, err := s.registry.ByID(ctx, hostID)
	if errors.Is(err, hostdomain.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if !host.CredentialMatches(credential) {
		return nil, ErrUnauthenticated
	}

	if address != nil {
		host.Address = *address
	}
	if labels != nil {
		host.RoleLabels = append([]string(nil), labels...)
	}
	updated, err := s.registry.Update(ctx, host)
	if errors.Is(err, hostdomain.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, s.recoveryHint(err)
	}
	return updated, nil
}

// endpointHint derives the cluster access hint from the declared
// topology: the gateway-front placement's host address plus its http
// port. Every failure mode (no topology, no gateway front, no port, no
// host row) degrades to an empty hint — a join must never fail for
// want of onboarding trivia.
func (s *Service) endpointHint(ctx context.Context) string {
	topo, err := s.topology.Current(ctx)
	if err != nil {
		return ""
	}
	for _, p := range topo.Placements {
		if p.Component != topologydomain.ComponentGatewayFront {
			continue
		}
		port, ok := p.Ports[gatewayFrontPort]
		if !ok {
			return ""
		}
		host, err := s.registry.ByID(ctx, p.HostID)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("http://%s:%d", host.Address, port)
	}
	return ""
}

// recoveryHint annotates an address-uniqueness failure with the
// operator-facing recovery path (topology-l1 §3 journey 2): the
// credential already went to the original joiner, so the fix is
// re-join — never a second credential mint.
func (s *Service) recoveryHint(err error) error {
	if errors.Is(err, hostdomain.ErrAddressTaken) {
		return fmt.Errorf("%w: re-join with the existing host credential (POST /v1/join/rejoin) — a second credential is never minted", err)
	}
	return err
}

// hasHexPrefix reports whether the hash's hex encoding starts with
// prefix (case-insensitive). An empty prefix matches everything; an
// odd-length prefix can never match a byte-aligned encoding.
func hasHexPrefix(hash []byte, prefix string) bool {
	if prefix == "" {
		return true
	}
	if len(prefix)%2 != 0 {
		return false
	}
	n := len(prefix) / 2
	if n > len(hash) {
		return false
	}
	encoded := hex.EncodeToString(hash[:n])
	for i := 0; i < len(encoded); i++ {
		if a, b := encoded[i], prefix[i]; a != b && a != lowerASCII(b) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'F' {
		return c + ('a' - 'A')
	}
	return c
}

func timePtr(t time.Time) *time.Time { return &t }
