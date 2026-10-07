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
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	provisionport "github.com/kikakkz/looming/identity/internal/provision/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
	quotaport "github.com/kikakkz/looming/identity/internal/quota/port"

	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
	keyport "github.com/kikakkz/looming/identity/internal/key/port"
)

var testNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// --- test doubles (AD-25: unit layer, no network/disk/clock) ---

type fakeKeyRepo struct {
	mu        sync.Mutex
	byID      map[string]*keydomain.LoomingKey
	mapRows   map[string]*provisiondomain.IdentityMap
	createErr error
	count     int
	countErr  error
	revokeErr error
}

func newFakeKeyRepo() *fakeKeyRepo {
	return &fakeKeyRepo{
		byID:    map[string]*keydomain.LoomingKey{},
		mapRows: map[string]*provisiondomain.IdentityMap{},
	}
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

// CreateWithProvision simulates the issuance transaction: both rows
// persist or neither. createErr models any identity-write failure.
func (f *fakeKeyRepo) CreateWithProvision(_ context.Context, k *keydomain.LoomingKey, m *provisiondomain.IdentityMap) error {
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
	if _, ok := f.mapRows[m.KeyID+"|"+m.Engine]; ok {
		return provisiondomain.ErrConflict
	}
	kc := *k
	f.byID[k.ID] = &kc
	mc := *m
	f.mapRows[m.KeyID+"|"+m.Engine] = &mc
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

func (f *fakePrincipalRepo) SetRoles(context.Context, *principaldomain.Principal) (*principaldomain.Principal, error) {
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
var _ provisionport.EngineProvisioner = (*fakeProvisioner)(nil)
var _ provisionport.MapRepository = (*fakeMapRepo)(nil)
var _ quotaport.Repository = (*fakeQuotaRepo)(nil)

// --- engine provisioning doubles (slice C) ---

// fakeProvisioner records Create/SetBudget/Delete calls and models the
// engine's failure modes.
type fakeProvisioner struct {
	mu        sync.Mutex
	creates   []string
	deletes   []string
	quotas    map[string]*quotadomain.Quota
	createErr error
	deleteErr error
}

func newFakeProvisioner() *fakeProvisioner {
	return &fakeProvisioner{quotas: map[string]*quotadomain.Quota{}}
}

func (f *fakeProvisioner) Create(_ context.Context, alias string, quota *quotadomain.Quota) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", "", f.createErr
	}
	f.creates = append(f.creates, alias)
	f.quotas[alias] = quota
	return "ref-" + alias, "sk-value-" + alias, nil
}

func (f *fakeProvisioner) SetBudget(context.Context, string, quotadomain.Quota) error {
	return errors.New("not implemented in key tests")
}

func (f *fakeProvisioner) Delete(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, ref)
	return f.deleteErr
}

// fakeMapRepo mirrors the identity_map rows the provision repository
// reads; the key repo's transaction writes them in production, tests
// seed both sides explicitly.
type fakeMapRepo struct {
	mu        sync.Mutex
	rows      map[string]*provisiondomain.IdentityMap
	listErr   error
	deleteErr error
}

func newFakeMapRepo() *fakeMapRepo {
	return &fakeMapRepo{rows: map[string]*provisiondomain.IdentityMap{}}
}

func (f *fakeMapRepo) ListByKey(_ context.Context, keyID string) ([]*provisiondomain.IdentityMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []*provisiondomain.IdentityMap{}
	for _, m := range f.rows {
		if m.KeyID == keyID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeMapRepo) Delete(_ context.Context, keyID, engine string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.rows, keyID+"|"+engine)
	return nil
}

func (f *fakeMapRepo) ListByPrincipal(context.Context, string, int, int) ([]*provisiondomain.IdentityMap, int64, error) {
	return nil, 0, errors.New("not implemented in key tests")
}

type fakeQuotaRepo struct {
	byID map[string]*quotadomain.Quota
}

func (f *fakeQuotaRepo) Upsert(context.Context, *quotadomain.Quota) error {
	return errors.New("not implemented in key tests")
}

func (f *fakeQuotaRepo) ByPrincipal(_ context.Context, principalID string) (*quotadomain.Quota, error) {
	q, ok := f.byID[principalID]
	if !ok {
		return nil, quotadomain.ErrNoQuota
	}
	cp := *q
	return &cp, nil
}

// provisionedService wires the optional provision step the way cmd does
// when IDENTITY_ENGINE_URL is set.
func provisionedService(repo *fakeKeyRepo, principals *fakePrincipalRepo, prov *fakeProvisioner, maps *fakeMapRepo, quotas quotaport.Repository) *Service {
	svc := newTestService(repo, principals, &fakeSealer{})
	svc.SetEngineProvisioner(prov, maps, quotas, "litellm")
	return svc
}

func onePrincipal() *fakePrincipalRepo {
	return &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipal("p-1")}}
}

func TestIssueWithProvisioningPersistsBothRows(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	k, secret, err := svc.Issue(ownerCtx(), "p-1", "engine")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(prov.creates) != 1 || prov.creates[0] != "looming-"+k.ID {
		t.Fatalf("Create must be anchored on the key id, got %v", prov.creates)
	}
	// No quota row: the credential is unlimited.
	if q := prov.quotas["looming-"+k.ID]; q != nil {
		t.Fatalf("no quota row must provision unlimited, got %+v", q)
	}
	entry, ok := repo.mapRows[k.ID+"|litellm"]
	if !ok {
		t.Fatal("the map entry must ride the issuance transaction")
	}
	if entry.CredentialRef != "ref-looming-"+k.ID {
		t.Fatalf("map entry must carry the engine reference, got %q", entry.CredentialRef)
	}
	if string(entry.CredentialEnc) == "sk-value-looming-"+k.ID {
		t.Fatal("the map entry must seal the credential value, never the plaintext")
	}
	if string(secret)[:3] != "lk-" {
		t.Fatalf("the LoomingKey secret is unchanged by provisioning, got %q", secret)
	}
}

func TestIssueWithQuotaProvisionsBudgetedCredential(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	quotas := &fakeQuotaRepo{byID: map[string]*quotadomain.Quota{
		"p-1": {PrincipalID: "p-1", Amount: 500, Unit: quotadomain.UnitUSD, WindowDays: 30, UpdatedBy: "admin-1", UpdatedAt: testNow},
	}}
	svc := provisionedService(repo, onePrincipal(), prov, maps, quotas)

	k, _, err := svc.Issue(ownerCtx(), "p-1", "engine")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	q := prov.quotas["looming-"+k.ID]
	if q == nil || q.Amount != 500 || q.WindowDays != 30 {
		t.Fatalf("the principal's quota must reach the provisioner, got %+v", q)
	}
}

func TestIssuePersistFailureRollsBackEngineCredential(t *testing.T) {
	repo := newFakeKeyRepo()
	repo.createErr = errors.New("db down")
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	if _, _, err := svc.Issue(ownerCtx(), "p-1", "engine"); err == nil {
		t.Fatal("an identity-write failure must surface")
	}
	if len(prov.deletes) != 1 {
		t.Fatalf("rollback must delete the engine credential exactly once, got %v", prov.deletes)
	}
	if len(repo.byID) != 0 || len(repo.mapRows) != 0 {
		t.Fatal("nothing identity-side may persist when the write fails")
	}
}

func TestIssueProvisionFailureSurfacesSentinelAndStoresNothing(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	prov.createErr = errors.New("engine 500")
	maps := newFakeMapRepo()
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	if _, _, err := svc.Issue(ownerCtx(), "p-1", "engine"); !errors.Is(err, ErrProvisionFailed) {
		t.Fatalf("want ErrProvisionFailed, got %v", err)
	}
	if len(prov.deletes) != 0 {
		t.Fatalf("a failed create must not be rolled back (nothing was created), got %v", prov.deletes)
	}
	if len(repo.byID) != 0 || len(repo.mapRows) != 0 {
		t.Fatal("nothing identity-side may persist when provisioning fails")
	}
}

// flakySealer fails after n successful Seals — models the transient
// failure that can hit the credential seal after the LoomingKey seal
// already succeeded.
type flakySealer struct {
	calls     int
	failAfter int
}

func (f *flakySealer) Seal(p []byte) ([]byte, error) {
	f.calls++
	if f.calls > f.failAfter {
		return nil, errors.New("seal failed")
	}
	out := make([]byte, len(p))
	for i, b := range p {
		out[len(p)-1-i] = b
	}
	return out, nil
}

func (f *flakySealer) Open(sealed []byte) ([]byte, error) {
	out := make([]byte, len(sealed))
	for i, b := range sealed {
		out[len(sealed)-1-i] = b
	}
	return out, nil
}

func TestIssueSealerFailureRollsBackEngineCredential(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	// The LoomingKey seal (call 1) succeeds; the credential seal
	// (call 2) fails — the engine credential must roll back through the
	// same immediate-rollback path as a persistence failure.
	svc := newTestService(repo, onePrincipal(), &flakySealer{failAfter: 1})
	svc.SetEngineProvisioner(prov, maps, &fakeQuotaRepo{}, "litellm")

	if _, _, err := svc.Issue(ownerCtx(), "p-1", "engine"); err == nil {
		t.Fatal("a seal failure must surface")
	}
	if len(prov.creates) != 1 {
		t.Fatalf("the engine credential must have been created before the seal, got %v", prov.creates)
	}
	if len(prov.deletes) != 1 {
		t.Fatalf("a seal failure must roll the engine credential back, got %v", prov.deletes)
	}
	if len(repo.byID) != 0 || len(repo.mapRows) != 0 {
		t.Fatal("nothing identity-side may persist when the seal fails")
	}
}

func TestRevokeDeletesMapEntryAndEngineCredential(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	k, _, err := svc.Issue(ownerCtx(), "p-1", "engine")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Mirror the row into the provision repository's view (production:
	// both adapters read the same identity_map table).
	entry := repo.mapRows[k.ID+"|litellm"]
	maps.rows[k.ID+"|litellm"] = entry

	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(maps.rows) != 0 {
		t.Fatalf("revocation must delete the map entry, got %v", maps.rows)
	}
	if len(prov.deletes) != 1 || prov.deletes[0] != entry.CredentialRef {
		t.Fatalf("revocation must delete the engine credential, got %v", prov.deletes)
	}
	// The issuance-side fake mirrors the same identity_map table the
	// MapRepository reads in production; the delete above is the single
	// source of truth (the integration tests pin the real shared table).
}

func TestRevokeSurvivesEngineDeleteFailure(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	k, _, err := svc.Issue(ownerCtx(), "p-1", "engine")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	entry := repo.mapRows[k.ID+"|litellm"]
	maps.rows[k.ID+"|litellm"] = entry
	prov.deleteErr = errors.New("engine down")

	// Revocation still succeeds: the map entry dies first so the feed
	// stops serving the credential; the orphan engine credential is
	// inert because nothing references it.
	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); err != nil {
		t.Fatalf("Revoke must survive an engine delete failure, got %v", err)
	}
	if len(maps.rows) != 0 {
		t.Fatal("the map entry must die even when the engine delete fails")
	}
}

func TestRevokeWithoutProvisionerLeavesMapAlone(t *testing.T) {
	repo := newFakeKeyRepo()
	svc := newTestService(repo, onePrincipal(), &fakeSealer{})
	k, _, err := svc.Issue(ownerCtx(), "p-1", "plain")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(repo.mapRows) != 0 {
		t.Fatal("without an engine no map rows may exist")
	}
}

func TestRevokeFallsBackToAliasDeleteWhenMapListFails(t *testing.T) {
	repo := newFakeKeyRepo()
	prov := newFakeProvisioner()
	maps := newFakeMapRepo()
	maps.listErr = errors.New("db down")
	svc := provisionedService(repo, onePrincipal(), prov, maps, &fakeQuotaRepo{})

	k, _, err := svc.Issue(ownerCtx(), "p-1", "engine")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	prov.deletes = nil

	if err := svc.Revoke(ownerCtx(), "p-1", k.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// The map is unreadable, but the credential's deterministic alias
	// must still be deleted — a revoked key's engine credential cannot
	// be allowed to outlive the revocation.
	if len(prov.deletes) != 1 || prov.deletes[0] != "looming-"+k.ID {
		t.Fatalf("fallback delete must target the deterministic alias, got %v", prov.deletes)
	}
}
