// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	policydomain "github.com/kikakkz/looming/identity/internal/policy/domain"
	policyport "github.com/kikakkz/looming/identity/internal/policy/port"
	"github.com/kikakkz/looming/identity/internal/principal/domain"
)

var testNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// --- test doubles (AD-25: unit layer, no network/disk/clock) ---

type fakeRepo struct {
	byID       map[string]*domain.Principal
	byUsername map[string]string // username -> id
	createErr  error
	listErr    error
	updateErr  error
	staleRead  bool // next UpdateStatus simulates a lost version race
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		byID:       map[string]*domain.Principal{},
		byUsername: map[string]string{},
	}
}

func (f *fakeRepo) Create(_ context.Context, p *domain.Principal) error {
	if f.createErr != nil {
		return f.createErr
	}
	if _, taken := f.byUsername[p.Username]; taken {
		return domain.ErrUsernameTaken
	}
	cp := *p
	f.byID[p.ID] = &cp
	f.byUsername[p.Username] = p.ID
	return nil
}

func (f *fakeRepo) ByID(_ context.Context, id string) (*domain.Principal, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakeRepo) ByUsername(_ context.Context, username string) (*domain.Principal, error) {
	id, ok := f.byUsername[username]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return f.ByID(context.Background(), id)
}

func (f *fakeRepo) List(_ context.Context, limit, offset int) ([]*domain.Principal, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	all := make([]*domain.Principal, 0, len(f.byID))
	for _, p := range f.byID {
		cp := *p
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID < all[j].ID
	})
	if offset > len(all) {
		return nil, int64(len(f.byID)), nil
	}
	page := all[offset:]
	if limit > 0 && limit < len(page) {
		page = page[:limit]
	}
	return page, int64(len(f.byID)), nil
}

func (f *fakeRepo) UpdateStatus(_ context.Context, p *domain.Principal) (*domain.Principal, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	stored, ok := f.byID[p.ID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if f.staleRead {
		f.staleRead = false
		return nil, domain.ErrConflict
	}
	if stored.Version != p.Version {
		return nil, domain.ErrConflict
	}
	cp := *p
	cp.Version++
	f.byID[p.ID] = &cp
	return &cp, nil
}

func (f *fakeRepo) Count(context.Context) (int64, error) {
	return int64(len(f.byID)), nil
}

type fakeInvites struct {
	byHash   map[string]*domain.InviteToken
	markErr  error
	notFound bool
}

func newFakeInvites() *fakeInvites {
	return &fakeInvites{byHash: map[string]*domain.InviteToken{}}
}

func (f *fakeInvites) Create(_ context.Context, tok *domain.InviteToken) error {
	cp := *tok
	f.byHash[string(tok.TokenHash)] = &cp
	return nil
}

func (f *fakeInvites) ByHash(_ context.Context, hash []byte) (*domain.InviteToken, error) {
	if f.notFound {
		return nil, domain.ErrNotFound
	}
	tok, ok := f.byHash[string(hash)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *tok
	return &cp, nil
}

func (f *fakeInvites) MarkUsed(_ context.Context, hash []byte, at time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	tok, ok := f.byHash[string(hash)]
	if !ok || tok.UsedAt != nil {
		return domain.ErrConflict
	}
	tok.UsedAt = &at
	return nil
}

type fakePolicy struct {
	policy *policydomain.Policy
	err    error
}

func (f *fakePolicy) Get(context.Context) (*policydomain.Policy, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.policy == nil {
		return nil, policydomain.ErrNotFound
	}
	return f.policy, nil
}

func (f *fakePolicy) Set(context.Context, *policydomain.Policy) error { return nil }

var _ policyport.Store = (*fakePolicy)(nil)

type fakeHasher struct{}

func (fakeHasher) Hash(password string) (string, error) { return "argon2:" + password, nil }

func policyOf(mode policydomain.Mode) *fakePolicy {
	return &fakePolicy{policy: &policydomain.Policy{
		ID: policydomain.SingletonID, Mode: mode, UpdatedBy: "t", UpdatedAt: testNow,
	}}
}

func newTestService(repo *fakeRepo, invites *fakeInvites, pol *fakePolicy) *Service {
	return NewService(repo, invites, pol, fakeHasher{}, bytes.NewReader(make([]byte, 32)), func() time.Time { return testNow })
}

// --- use-case tests ---

func TestRegisterAdminOnlyForbidden(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	_, err := svc.Register(context.Background(), RegisterInput{
		Username: "ker", Password: "correct horse battery", DisplayName: "Ker",
	})
	if !errors.Is(err, ErrSelfRegistrationForbidden) {
		t.Fatalf("want ErrSelfRegistrationForbidden, got %v", err)
	}
}

func TestRegisterFailsClosedOnPolicyReadError(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), &fakePolicy{err: errors.New("db down")})
	_, err := svc.Register(context.Background(), RegisterInput{
		Username: "ker", Password: "correct horse battery",
	})
	if err == nil || errors.Is(err, ErrSelfRegistrationForbidden) {
		t.Fatalf("policy read failure must deny registration distinctly, got %v", err)
	}
}

func TestRegisterSelfRegisterGoesPending(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(policydomain.ModeSelfRegisterWithApproval))
	p, err := svc.Register(context.Background(), RegisterInput{
		Username: "Ker", Password: "correct horse battery", DisplayName: "Ker",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.Status != domain.StatusPending {
		t.Fatalf("self-register must start pending, got %s", p.Status)
	}
	if p.Username != "ker" {
		t.Fatalf("username must be normalized lowercase, got %q", p.Username)
	}
	if p.Kind != domain.KindHuman {
		t.Fatalf("self-register is human-only, got %s", p.Kind)
	}
	if p.PasswordHash != "argon2:correct horse battery" {
		t.Fatalf("password must be hashed before storage, got %q", p.PasswordHash)
	}
}

func TestRegisterInviteConsumesToken(t *testing.T) {
	invites := newFakeInvites()
	svc := newTestService(newFakeRepo(), invites, policyOf(policydomain.ModeInvite))
	raw, tok, err := svc.CreateInvite(context.Background(), "admin-1", time.Hour)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	p, err := svc.Register(context.Background(), RegisterInput{
		Username: "ker", Password: "correct horse battery", InviteToken: raw,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if p.Status != domain.StatusActive {
		t.Fatalf("invite registration starts active, got %s", p.Status)
	}
	stored, err := invites.ByHash(context.Background(), tok.TokenHash)
	if err != nil {
		t.Fatalf("ByHash: %v", err)
	}
	if stored.UsedAt == nil || !stored.UsedAt.Equal(testNow) {
		t.Fatalf("invite must be consumed at registration, got %+v", stored.UsedAt)
	}
}

func TestRegisterInviteRejectsBadToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		setup func(*fakeInvites)
	}{
		{"missing", "", nil},
		{"unknown", "bogus", nil},
		{"already used", "used", func(f *fakeInvites) {
			f.notFound = false
			now := testNow
			used := now.Add(-time.Minute)
			f.byHash[string(domain.HashToken("used"))] = &domain.InviteToken{
				ExpiresAt: now.Add(time.Hour), UsedAt: &used,
			}
		}},
		{"expired", "late", func(f *fakeInvites) {
			f.byHash[string(domain.HashToken("late"))] = &domain.InviteToken{
				ExpiresAt: testNow.Add(-time.Hour),
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invites := newFakeInvites()
			if tc.setup != nil {
				tc.setup(invites)
			}
			svc := newTestService(newFakeRepo(), invites, policyOf(policydomain.ModeInvite))
			_, err := svc.Register(context.Background(), RegisterInput{
				Username: "ker", Password: "correct horse battery", InviteToken: tc.token,
			})
			if !errors.Is(err, domain.ErrInvalidInvite) {
				t.Fatalf("want ErrInvalidInvite, got %v", err)
			}
		})
	}
}

func TestRegisterInviteRacedConsumeDenied(t *testing.T) {
	invites := newFakeInvites()
	invites.markErr = domain.ErrConflict
	svc := newTestService(newFakeRepo(), invites, policyOf(policydomain.ModeInvite))
	raw, _, err := svc.CreateInvite(context.Background(), "admin-1", time.Hour)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	_, err = svc.Register(context.Background(), RegisterInput{
		Username: "ker", Password: "correct horse battery", InviteToken: raw,
	})
	if !errors.Is(err, domain.ErrInvalidInvite) {
		t.Fatalf("lost the consume race must surface as ErrInvalidInvite, got %v", err)
	}
}

func TestRegisterShortPasswordRejected(t *testing.T) {
	for _, mode := range []policydomain.Mode{policydomain.ModeInvite, policydomain.ModeSelfRegisterWithApproval} {
		svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(mode))
		_, err := svc.Register(context.Background(), RegisterInput{
			Username: "ker", Password: "short",
		})
		if !errors.Is(err, ErrPasswordTooShort) {
			t.Fatalf("mode %s: want ErrPasswordTooShort, got %v", mode, err)
		}
	}
}

func TestRegisterUsernameTaken(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(policydomain.ModeSelfRegisterWithApproval))
	in := RegisterInput{Username: "ker", Password: "correct horse battery"}
	if _, err := svc.Register(context.Background(), in); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err := svc.Register(context.Background(), in)
	if !errors.Is(err, domain.ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
}

func TestProvisionAnyModeStartsActive(t *testing.T) {
	for _, mode := range []policydomain.Mode{
		policydomain.ModeAdminOnly, policydomain.ModeInvite, policydomain.ModeSelfRegisterWithApproval,
	} {
		svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(mode))
		p, err := svc.Provision(context.Background(), ProvisionInput{
			Username: "svc-bot", Password: "correct horse battery",
			Kind: domain.KindService,
		})
		if err != nil {
			t.Fatalf("mode %s: Provision: %v", mode, err)
		}
		if p.Status != domain.StatusActive || p.Kind != domain.KindService {
			t.Fatalf("mode %s: want active service principal, got %s/%s", mode, p.Status, p.Kind)
		}
		if diff := cmp.Diff([]string{domain.RoleMember}, p.Roles); diff != "" {
			t.Fatalf("mode %s: roles mismatch (-want +got):\n%s", mode, diff)
		}
	}
}

func TestProvisionWithRoles(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	p, err := svc.Provision(context.Background(), ProvisionInput{
		Username: "root", Password: "correct horse battery",
		Kind: domain.KindHuman, Roles: []string{domain.RoleAdmin},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if diff := cmp.Diff([]string{domain.RoleAdmin}, p.Roles); diff != "" {
		t.Fatalf("roles mismatch (-want +got):\n%s", diff)
	}
}

func TestApproveFlow(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, newFakeInvites(), policyOf(policydomain.ModeSelfRegisterWithApproval))
	p, err := svc.Register(context.Background(), RegisterInput{
		Username: "ker", Password: "correct horse battery",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := svc.Approve(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got.Status != domain.StatusActive {
		t.Fatalf("want active, got %s", got.Status)
	}
	if got.Version != p.Version+1 {
		t.Fatalf("approve must bump version %d -> %d", p.Version, got.Version)
	}
	_, err = svc.Approve(context.Background(), p.ID)
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second approve must fail with ErrInvalidTransition, got %v", err)
	}
}

func TestApproveUnknownPrincipal(t *testing.T) {
	svc := newTestService(newFakeRepo(), newFakeInvites(), policyOf(policydomain.ModeSelfRegisterWithApproval))
	_, err := svc.Approve(context.Background(), "nope")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSetStatusFlow(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	p, err := svc.Provision(context.Background(), ProvisionInput{
		Username: "ker", Password: "correct horse battery", Kind: domain.KindHuman,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	got, err := svc.SetStatus(context.Background(), p.ID, domain.StatusDisabled)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got.Status != domain.StatusDisabled {
		t.Fatalf("want disabled, got %s", got.Status)
	}
	got, err = svc.SetStatus(context.Background(), p.ID, domain.StatusActive)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if got.Status != domain.StatusActive {
		t.Fatalf("want active, got %s", got.Status)
	}
	_, err = svc.SetStatus(context.Background(), p.ID, domain.StatusPending)
	if !errors.Is(err, domain.ErrInvalidStatus) {
		t.Fatalf("SetStatus(pending) must be rejected, got %v", err)
	}
}

func TestSetStatusPropagatesConflict(t *testing.T) {
	repo := newFakeRepo()
	repo.staleRead = true
	svc := newTestService(repo, newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	p, err := svc.Provision(context.Background(), ProvisionInput{
		Username: "ker", Password: "correct horse battery", Kind: domain.KindHuman,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	_, err = svc.SetStatus(context.Background(), p.ID, domain.StatusDisabled)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want ErrConflict on lost version race, got %v", err)
	}
}

func TestGetAndList(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, newFakeInvites(), policyOf(policydomain.ModeAdminOnly))
	for _, username := range []string{"u-one", "u-two", "u-three"} {
		if _, err := svc.Provision(context.Background(), ProvisionInput{
			Username: username, Password: "correct horse battery", Kind: domain.KindHuman,
		}); err != nil {
			t.Fatalf("Provision %s: %v", username, err)
		}
	}
	got, err := svc.Get(context.Background(), "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v (%+v)", err, got)
	}
	items, total, err := svc.List(context.Background(), 2, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 || len(items) != 2 {
		t.Fatalf("want page 2 of 3, got %d of %d", len(items), total)
	}
	items, total, err = svc.List(context.Background(), 2, 2)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if total != 3 || len(items) != 1 {
		t.Fatalf("want page 1 of 3, got %d of %d", len(items), total)
	}
}

func TestCreateInviteDefaultsTTL(t *testing.T) {
	invites := newFakeInvites()
	svc := newTestService(newFakeRepo(), invites, policyOf(policydomain.ModeInvite))
	raw, tok, err := svc.CreateInvite(context.Background(), "admin-1", 0)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if raw == "" {
		t.Fatal("raw token must be returned once")
	}
	if !tok.ExpiresAt.Equal(testNow.Add(DefaultInviteTTL)) {
		t.Fatalf("zero ttl must default to %s, expires %s", DefaultInviteTTL, tok.ExpiresAt)
	}
}
