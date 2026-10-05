// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"

	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
	keyport "github.com/kikakkz/looming/identity/internal/key/port"
)

var testNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// --- test doubles (AD-25: unit layer, no network/disk/clock) ---

type fakeKeyRepo struct {
	mu        sync.Mutex
	byID      map[string]*keydomain.LoomingKey
	createErr error
	count     int
	countErr  error
	revokeErr error
}

func newFakeKeyRepo() *fakeKeyRepo {
	return &fakeKeyRepo{byID: map[string]*keydomain.LoomingKey{}}
}

func (f *fakeKeyRepo) Create(_ context.Context, k *keydomain.LoomingKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.byID {
		if existing.KeyHash == k.KeyHash {
			return keydomain.ErrConflict
		}
	}
	cp := *k
	f.byID[k.ID] = &cp
	return nil
}

func (f *fakeKeyRepo) ByID(_ context.Context, id string) (*keydomain.LoomingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.byID[id]
	if !ok {
		return nil, keydomain.ErrNotFound
	}
	cp := *k
	return &cp, nil
}

func (f *fakeKeyRepo) ListByPrincipal(_ context.Context, principalID string) ([]*keydomain.LoomingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*keydomain.LoomingKey{}
	for _, k := range f.byID {
		if k.PrincipalID == principalID {
			cp := *k
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeKeyRepo) ByHash(_ context.Context, hash [32]byte) (*keydomain.LoomingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.byID {
		if k.KeyHash == hash {
			cp := *k
			return &cp, nil
		}
	}
	return nil, keydomain.ErrNotFound
}

func (f *fakeKeyRepo) Revoke(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	k, ok := f.byID[id]
	if !ok || k.Status == keydomain.StatusRevoked {
		return keydomain.ErrAlreadyRevoked
	}
	k.Status = keydomain.StatusRevoked
	k.RevokedAt = &now
	return nil
}

func (f *fakeKeyRepo) CountSince(_ context.Context, _ string, _ time.Time) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.count, nil
}

// fakePrincipalRepo implements only what the key service reads
// (principal exists + active); every other method fails the test.
type fakePrincipalRepo struct {
	byID map[string]*principaldomain.Principal
}

func activePrincipal(id string) *principaldomain.Principal {
	return &principaldomain.Principal{
		ID: id, Username: "user-" + id, Kind: principaldomain.KindHuman,
		Status: principaldomain.StatusActive, Roles: []string{principaldomain.RoleMember},
		Version: 1, CreatedAt: testNow, UpdatedAt: testNow,
	}
}

func (f *fakePrincipalRepo) Create(context.Context, *principaldomain.Principal) error {
	return errors.New("not implemented")
}

func (f *fakePrincipalRepo) ByID(_ context.Context, id string) (*principaldomain.Principal, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, principaldomain.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePrincipalRepo) ByUsername(context.Context, string) (*principaldomain.Principal, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePrincipalRepo) List(context.Context, int, int) ([]*principaldomain.Principal, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (f *fakePrincipalRepo) UpdateStatus(context.Context, *principaldomain.Principal) (*principaldomain.Principal, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePrincipalRepo) Count(context.Context) (int64, error) {
	return 0, errors.New("not implemented")
}

func (f *fakePrincipalRepo) ExistsAdmin(context.Context) (bool, error) {
	return false, errors.New("not implemented")
}

func (f *fakePrincipalRepo) CreateWithInviteConsume(context.Context, *principaldomain.Principal, []byte, time.Time) error {
	return errors.New("not implemented")
}

// fakeSealer reverses the bytes — reversible, deterministic, and
// visibly not the production cipher.
type fakeSealer struct{ err error }

func (f *fakeSealer) Seal(plaintext []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[len(plaintext)-1-i] = b
	}
	return out, nil
}

func (f *fakeSealer) Open(sealed []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]byte, len(sealed))
	for i, b := range sealed {
		out[len(sealed)-1-i] = b
	}
	return out, nil
}

type fakeNotifier struct{ bumps int }

func (f *fakeNotifier) Bump() { f.bumps++ }

type fakeRand struct{ next byte }

func (f *fakeRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = f.next
		f.next++
	}
	return len(p), nil
}

func newTestService(repo *fakeKeyRepo, principals *fakePrincipalRepo, sealer keyport.Sealer) *Service {
	return NewService(repo, principals, sealer, &fakeRand{}, func() time.Time { return testNow }, 10, time.Hour)
}

func ownerCtx() context.Context { return context.Background() }

func TestIssueCreatesKeyAndBumpsRevision(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	notifier := &fakeNotifier{}
	svc := newTestService(repo, principals, &fakeSealer{})
	svc.SetRevisionNotifier(notifier)

	k, secret, err := svc.Issue(ownerCtx(), "p-1", "ci")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if k.PrincipalID != "p-1" || k.Name != "ci" || k.Status != keydomain.StatusActive {
		t.Fatalf("issued key mismatch: %+v", k)
	}
	if secret == "" || string(secret)[:3] != "lk-" {
		t.Fatalf("issue must return the raw secret once, got %q", secret)
	}
	stored, err := repo.ByID(ownerCtx(), k.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if string(stored.Sealed) == string(secret) {
		t.Fatal("at rest only the sealed blob may be stored, never the raw secret")
	}
	if stored.KeyHash != secret.Hash() {
		t.Fatal("the stored hash must be the secret's SHA-256")
	}
	if k.Prefix == "" || k.Last4 == "" {
		t.Fatal("display columns must be derived")
	}
	if notifier.bumps != 1 {
		t.Fatalf("issue must bump the feed revision once, got %d", notifier.bumps)
	}
}

func TestIssueRejectsUnknownPrincipal(t *testing.T) {
	svc := newTestService(newFakeKeyRepo(), &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{}}, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "ghost", ""); !errors.Is(err, ErrPrincipalNotFound) {
		t.Fatalf("want ErrPrincipalNotFound, got %v", err)
	}
}

func TestIssueRejectsInactivePrincipal(t *testing.T) {
	p := activePrincipal("p-1")
	p.Status = principaldomain.StatusDisabled
	svc := newTestService(newFakeKeyRepo(), &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": p}}, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); !errors.Is(err, ErrPrincipalInactive) {
		t.Fatalf("want ErrPrincipalInactive, got %v", err)
	}
}

func TestIssueEnforcesRateLimit(t *testing.T) {
	repo := newFakeKeyRepo()
	repo.count = 10
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	svc := newTestService(repo, principals, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); !errors.Is(err, ErrIssueLimit) {
		t.Fatalf("want ErrIssueLimit, got %v", err)
	}
	repo.count = 9
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); err != nil {
		t.Fatalf("below the limit must issue, got %v", err)
	}
}

func TestIssueSurfacesCreateAndSealFailures(t *testing.T) {
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}

	repo := newFakeKeyRepo()
	repo.createErr = errors.New("db down")
	svc := newTestService(repo, principals, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); err == nil {
		t.Fatal("a create failure must surface")
	}

	svc = newTestService(newFakeKeyRepo(), principals, &fakeSealer{err: errors.New("seal failed")})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); err == nil {
		t.Fatal("a sealer failure must surface")
	}
}

func TestListIsMaskedByConstruction(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	svc := newTestService(repo, principals, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", "one"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	items, err := svc.List(ownerCtx(), "p-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 key, got %d", len(items))
	}
	// Masked-by-default is enforced by the aggregate shape: list views
	// carry display columns only; the sealed blob and hash stay
	// service-internal (http_test asserts the wire shape).
	if items[0].Name != "one" || items[0].Prefix == "" || items[0].Last4 == "" {
		t.Fatalf("masked view mismatch: %+v", items[0])
	}
}

func TestRevealOwnerScoped(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{
		"p-1": activePrincipal("p-1"),
		"p-2": activePrincipal("p-2"),
	}}
	svc := newTestService(repo, principals, &fakeSealer{})
	k, secret, err := svc.Issue(ownerCtx(), "p-1", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := svc.Reveal(ownerCtx(), "p-1", k.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if got != secret {
		t.Fatalf("reveal must return the raw secret, got %q want %q", got, secret)
	}
	// Repeatable: the second reveal works too.
	again, err := svc.Reveal(ownerCtx(), "p-1", k.ID)
	if err != nil || again != secret {
		t.Fatalf("reveal must be repeatable, got %q (%v)", again, err)
	}
	// Another principal probing this key sees a plain not-found: no
	// existence leak across the owner boundary.
	if _, err := svc.Reveal(ownerCtx(), "p-2", k.ID); !errors.Is(err, keydomain.ErrNotFound) {
		t.Fatalf("cross-owner reveal must be ErrNotFound, got %v", err)
	}
}

func TestAdminRevealSkipsOwnerScope(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	svc := newTestService(repo, principals, &fakeSealer{})
	k, secret, err := svc.Issue(ownerCtx(), "p-1", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := svc.AdminReveal(ownerCtx(), k.ID)
	if err != nil {
		t.Fatalf("AdminReveal: %v", err)
	}
	if got != secret {
		t.Fatalf("admin reveal mismatch: %q", got)
	}
}

func TestRevealSurfacesOpenFailure(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	good := newTestService(repo, principals, &fakeSealer{})
	k, _, err := good.Issue(ownerCtx(), "p-1", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	broken := newTestService(repo, principals, &fakeSealer{err: errors.New("open failed")})
	if _, err := broken.Reveal(ownerCtx(), "p-1", k.ID); err == nil {
		t.Fatal("an open failure must surface")
	}
}

func TestRevokeOwnerScopedAndOneWay(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{
		"p-1": activePrincipal("p-1"),
		"p-2": activePrincipal("p-2"),
	}}
	notifier := &fakeNotifier{}
	svc := newTestService(repo, principals, &fakeSealer{})
	svc.SetRevisionNotifier(notifier)
	k, _, err := svc.Issue(ownerCtx(), "p-1", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := svc.Revoke(ownerCtx(), "p-2", k.ID); !errors.Is(err, keydomain.ErrNotFound) {
		t.Fatalf("cross-owner revoke must be ErrNotFound, got %v", err)
	}
	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); !errors.Is(err, keydomain.ErrAlreadyRevoked) {
		t.Fatalf("second revoke must be ErrAlreadyRevoked, got %v", err)
	}
	if notifier.bumps != 2 {
		t.Fatalf("issue+revoke must bump twice, got %d", notifier.bumps)
	}
}

func TestAdminRevokeAndAdminList(t *testing.T) {
	repo := newFakeKeyRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	svc := newTestService(repo, principals, &fakeSealer{})
	if _, _, err := svc.Issue(ownerCtx(), "p-1", "a"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	items, err := svc.AdminList(ownerCtx(), "p-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("AdminList: %v (%d)", err, len(items))
	}
	if err := svc.AdminRevoke(ownerCtx(), items[0].ID); err != nil {
		t.Fatalf("AdminRevoke: %v", err)
	}
	if err := svc.AdminRevoke(ownerCtx(), uuid.NewString()); !errors.Is(err, keydomain.ErrAlreadyRevoked) {
		t.Fatalf("admin revoke of a missing key collapses to ErrAlreadyRevoked, got %v", err)
	}
}

func TestFailedIssueDoesNotBump(t *testing.T) {
	repo := newFakeKeyRepo()
	repo.count = 99
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
	notifier := &fakeNotifier{}
	svc := newTestService(repo, principals, &fakeSealer{})
	svc.SetRevisionNotifier(notifier)
	if _, _, err := svc.Issue(ownerCtx(), "p-1", ""); err == nil {
		t.Fatal("over-limit issue must fail")
	}
	if notifier.bumps != 0 {
		t.Fatalf("a denied issue must not bump the feed revision, got %d", notifier.bumps)
	}
}

var _ principalport.Repository = (*fakePrincipalRepo)(nil)
var _ keyport.Repository = (*fakeKeyRepo)(nil)
var _ io.Reader = (*fakeRand)(nil)
