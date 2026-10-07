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

// stubBindings is the BindingRepository double.
type stubBindings struct {
	byPair  map[string]string // issuer+"\x00"+subject -> principalID
	created []string
}

func (s *stubBindings) ByIssuerSubject(_ context.Context, issuer, subject string) (string, error) {
	id, ok := s.byPair[issuer+"\x00"+subject]
	if !ok {
		return "", principaldomain.ErrNotFound
	}
	return id, nil
}

func (s *stubBindings) Create(_ context.Context, issuer, subject, principalID string) error {
	if s.byPair == nil {
		s.byPair = map[string]string{}
	}
	s.byPair[issuer+"\x00"+subject] = principalID
	s.created = append(s.created, principalID)
	return nil
}

const testIssuer = "https://idp.example.com"

func newOIDC(v TokenVerifier, repo *stubRepo, b *stubBindings) *OIDCProvider {
	if b == nil {
		b = &stubBindings{byPair: map[string]string{}}
	}
	return &OIDCProvider{
		LocalProvider: NewLocalProvider(nil, time.Hour, nil, time.Now),
		verifier:      v,
		repo:          repo,
		bindings:      b,
		issuer:        testIssuer,
		usernameClaim: "email",
		autoRegister:  true,
		clock:         time.Now,
	}
}

func TestVerifyExternalTokenUnknownToken(t *testing.T) {
	p := newOIDC(stubVerifier{err: errors.New("bad sig")}, &stubRepo{byUsername: map[string]*principaldomain.Principal{}}, nil)
	if _, err := p.VerifyExternalToken(context.Background(), "garbage"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("want ErrInvalidCredential, got %v", err)
	}
}

func TestVerifyExternalTokenAutoRegister(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{}}
	bindings := &stubBindings{byPair: map[string]string{}}
	p := newOIDC(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-123",
		Claims:  map[string]any{"email": "Jane.Doe@Example.com", "name": "Jane Doe"},
	}}, repo, bindings)

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
	// The binding is written and becomes the resolution authority.
	if len(bindings.created) != 1 || bindings.created[0] != got.ID {
		t.Fatalf("binding must be created for the new principal, got %v", bindings.created)
	}
	// Second sight resolves through the binding without re-registering.
	if _, err := p.VerifyExternalToken(context.Background(), "any-token"); err != nil {
		t.Fatalf("second sight: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("second sight must not re-register, got %d", len(repo.created))
	}
}

// TestVerifyExternalTokenUsernameCollisionIsNotIdentityCollision pins
// the Critical security property (CodeRabbit review on PR #141): two
// IdP accounts deriving the same username receive DISTINCT
// principals, bound to their own issuer+subject pairs.
func TestVerifyExternalTokenUsernameCollisionIsNotIdentityCollision(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{}}
	bindings := &stubBindings{byPair: map[string]string{}}
	p := newOIDC(stubVerifier{}, repo, bindings)

	first, err := p.VerifyExternalToken(context.Background(), "tok-1")
	if err != nil {
		t.Fatalf("first account: %v", err)
	}
	// Simulate a second verified identity whose claim derives the same
	// username through a different subject.
	p.verifier = stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-OTHER", Claims: map[string]any{"email": "jane.doe@second.example"},
	}}
	second, err := p.VerifyExternalToken(context.Background(), "tok-2")
	if err != nil {
		t.Fatalf("second account: %v", err)
	}
	if first == second {
		t.Fatalf("colliding derived usernames must not share a principal")
	}
	if len(repo.created) != 2 {
		t.Fatalf("want two principals, got %d", len(repo.created))
	}
	suffixed := false
	for _, created := range repo.created {
		if created.Username != "jane.doe" {
			suffixed = true
		}
	}
	if !suffixed {
		t.Fatalf("the colliding registration must take a suffixed username, got %v / %v",
			repo.created[0].Username, repo.created[1].Username)
	}
	if len(bindings.created) != 2 {
		t.Fatalf("each identity gets its own binding, got %v", bindings.created)
	}
}

func TestVerifyExternalTokenBoundPrincipalDisabledFailsClosed(t *testing.T) {
	bindings := &stubBindings{byPair: map[string]string{
		testIssuer + "\x00sub-123": "p-1",
	}}
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{
		"jane.doe": {ID: "p-1", Username: "jane.doe", Status: principaldomain.StatusDisabled},
	}}
	p := newOIDC(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-123", Claims: map[string]any{"email": "jane.doe@example.com"},
	}}, repo, bindings)
	if _, err := p.VerifyExternalToken(context.Background(), "tok"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("disabled principal must fail closed, got %v", err)
	}
}

func TestVerifyExternalTokenNoAutoRegister(t *testing.T) {
	repo := &stubRepo{byUsername: map[string]*principaldomain.Principal{}}
	p := newOIDC(stubVerifier{identity: VerifiedIdentity{
		Subject: "sub-9", Claims: map[string]any{"email": "new@example.com"},
	}}, repo, nil)
	p.autoRegister = false
	if _, err := p.VerifyExternalToken(context.Background(), "tok"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("unknown identity without auto-register must fail, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no principal may be created when auto-register is off")
	}
}
