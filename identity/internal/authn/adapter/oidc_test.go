// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
)

// stubVerifier returns a canned identity or error without a JWKS
// endpoint.
type stubVerifier struct {
	identity VerifiedIdentity
	err      error
}

func (s stubVerifier) Verify(context.Context, string) (VerifiedIdentity, error) {
	return s.identity, s.err
}

// stubRepo is the principal repository surface the provider needs.
type stubRepo struct {
	byUsername map[string]*principaldomain.Principal
	createErr  error
	created    []*principaldomain.Principal
}

func (s *stubRepo) Create(_ context.Context, p *principaldomain.Principal) error {
	if s.createErr != nil {
		return s.createErr
	}
	cp := *p
	s.created = append(s.created, &cp)
	s.byUsername[p.Username] = &cp
	return nil
}

func (s *stubRepo) CreateWithInviteConsume(context.Context, *principaldomain.Principal, []byte, time.Time) error {
	return errors.New("not implemented")
}

func (s *stubRepo) ByID(_ context.Context, id string) (*principaldomain.Principal, error) {
	for _, p := range s.byUsername {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, principaldomain.ErrNotFound
}

func (s *stubRepo) ByUsername(_ context.Context, username string) (*principaldomain.Principal, error) {
	p, ok := s.byUsername[username]
	if !ok {
		return nil, principaldomain.ErrNotFound
	}
	return p, nil
}

func (s *stubRepo) List(context.Context, int, int) ([]*principaldomain.Principal, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (s *stubRepo) UpdateStatus(context.Context, *principaldomain.Principal) (*principaldomain.Principal, error) {
	return nil, errors.New("not implemented")
}

func (s *stubRepo) SetRoles(context.Context, *principaldomain.Principal) (*principaldomain.Principal, error) {
	return nil, errors.New("not implemented")
}

func (s *stubRepo) Count(context.Context) (int64, error) { return 0, nil }

func (s *stubRepo) ExistsAdmin(context.Context) (bool, error) { return false, nil }

func newOIDCForTest(v TokenVerifier, repo *stubRepo) *OIDCProvider {
	return &OIDCProvider{
		LocalProvider: NewLocalProvider(nil, time.Hour, nil, time.Now),
		verifier:      v,
		repo:          repo,
		usernameClaim: "email",
		autoRegister:  true,
		clock:         time.Now,
	}
}

func TestVerifyExternalTokenUnknownToken(t *testing.T) {
	p := newOIDCForTest(stubVerifier{err: errors.New("bad sig")}, &stubRepo{byUsername: map[string]*principaldomain.Principal{}})
	if _, err := p.VerifyExternalToken(context.Background(), "garbage"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("want ErrInvalidCredential, got %v", err)
	}
}

func TestVerifyExternalTokenAutoRegister(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{}}
	p := newOIDCForTest(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-123",
		Claims:  map[string]any{"email": "Jane.Doe@Example.com", "name": "Jane Doe"},
	}}, repo)

	id, err := p.VerifyExternalToken(context.Background(), "any-token")
	if err != nil {
		t.Fatalf("VerifyExternalToken: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("want one auto-registered principal, got %d", len(repo.created))
	}
	got := repo.created[0]
	if got.Username != "jane.doe" {
		t.Fatalf("want derived username jane.doe, got %q", got.Username)
	}
	if got.Status != principaldomain.StatusActive || !got.HasRole(principaldomain.RoleMember) {
		t.Fatalf("auto-registration must land active+member, got %s %v", got.Status, got.Roles)
	}
	if got.ID != id {
		t.Fatalf("returned id %q differs from created %q", id, got.ID)
	}
	// Second sight resolves the existing principal instead of
	// re-registering.
	if _, err := p.VerifyExternalToken(context.Background(), "any-token"); err != nil {
		t.Fatalf("second sight: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("second sight must not re-register, got %d", len(repo.created))
	}
}

func TestVerifyExternalTokenDisabledPrincipalFailsClosed(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{
		"jane.doe": {ID: "p-1", Username: "jane.doe", Status: principaldomain.StatusDisabled},
	}}
	p := newOIDCForTest(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-123", Claims: map[string]any{"email": "jane.doe@example.com"},
	}}, repo)
	if _, err := p.VerifyExternalToken(context.Background(), "tok"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("disabled principal must fail closed, got %v", err)
	}
}

func TestVerifyExternalTokenNoAutoRegister(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{}}
	p := newOIDCForTest(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-9", Claims: map[string]any{"email": "new@example.com"},
	}}, repo)
	p.autoRegister = false
	if _, err := p.VerifyExternalToken(context.Background(), "tok"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("unknown identity without auto-register must fail, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no principal may be created when auto-register is off")
	}
}
