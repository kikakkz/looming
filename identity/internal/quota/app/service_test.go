// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	principaldomain "github.com/kikakkz/looming/identity/internal/principal/domain"
	principalport "github.com/kikakkz/looming/identity/internal/principal/port"
	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/internal/provision/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
	quotaport "github.com/kikakkz/looming/identity/internal/quota/port"
)

var quotaTestNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// --- test doubles (AD-25: unit layer, no network/disk/clock) ---

type fakeQuotaRepo struct {
	mu      sync.Mutex
	byID    map[string]*quotadomain.Quota
	upserts int
}

func newFakeQuotaRepo() *fakeQuotaRepo {
	return &fakeQuotaRepo{byID: map[string]*quotadomain.Quota{}}
}

func (f *fakeQuotaRepo) Upsert(_ context.Context, q *quotadomain.Quota) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	cp := *q
	f.byID[q.PrincipalID] = &cp
	return nil
}

func (f *fakeQuotaRepo) ByPrincipal(_ context.Context, principalID string) (*quotadomain.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.byID[principalID]
	if !ok {
		return nil, quotadomain.ErrNoQuota
	}
	cp := *q
	return &cp, nil
}

// fakePrincipalRepo implements only the existence probe the quota
// service needs.
type fakePrincipalRepo struct {
	byID map[string]*principaldomain.Principal
}

func (f *fakePrincipalRepo) ByID(_ context.Context, id string) (*principaldomain.Principal, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, principaldomain.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePrincipalRepo) Create(context.Context, *principaldomain.Principal) error {
	return errors.New("not implemented")
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

type fakeMapRepo struct {
	mu       sync.Mutex
	byID     map[string]*provisiondomain.IdentityMap
	owner    map[string]string // "keyID|engine" -> owning principalID
	lists    int
	deletes  int
	listErr  error
	deleteOK bool // when false, Delete fails (unused here; revoke lives in key-app)
}

func newFakeMapRepo() *fakeMapRepo {
	return &fakeMapRepo{
		byID:     map[string]*provisiondomain.IdentityMap{},
		owner:    map[string]string{},
		deleteOK: true,
	}
}

func (f *fakeMapRepo) ListByKey(_ context.Context, keyID string) ([]*provisiondomain.IdentityMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*provisiondomain.IdentityMap{}
	for _, m := range f.byID {
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
	f.deletes++
	if !f.deleteOK {
		return errors.New("map delete failed")
	}
	delete(f.byID, keyID+"|"+engine)
	return nil
}

func (f *fakeMapRepo) ListByPrincipal(_ context.Context, principalID string, limit, offset int) ([]*provisiondomain.IdentityMap, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	all := []*provisiondomain.IdentityMap{}
	for id, m := range f.byID {
		if f.owner[id] == principalID {
			cp := *m
			all = append(all, &cp)
		}
	}
	if offset >= len(all) {
		return []*provisiondomain.IdentityMap{}, int64(len(all)), nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], int64(len(all)), nil
}

type fakeProvisioner struct {
	mu         sync.Mutex
	setBudget  map[string]quotadomain.Quota
	budgetErrs map[string]error
	setCalls   int
}

func newFakeProvisioner() *fakeProvisioner {
	return &fakeProvisioner{setBudget: map[string]quotadomain.Quota{}, budgetErrs: map[string]error{}}
}

func (f *fakeProvisioner) Create(context.Context, string, *quotadomain.Quota) (string, string, error) {
	return "", "", errors.New("not implemented in quota tests")
}

func (f *fakeProvisioner) SetBudget(_ context.Context, ref string, quota quotadomain.Quota) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if err, ok := f.budgetErrs[ref]; ok {
		return err
	}
	f.setBudget[ref] = quota
	return nil
}

func (f *fakeProvisioner) Delete(context.Context, string) error {
	return errors.New("not implemented in quota tests")
}

// --- tests ---

func newTestService(repo *fakeQuotaRepo, principals *fakePrincipalRepo, maps *fakeMapRepo, prov *fakeProvisioner) *Service {
	clock := func() time.Time { return quotaTestNow }
	if prov == nil {
		return NewService(repo, principals, maps, nil, clock)
	}
	return NewService(repo, principals, maps, prov, clock)
}

func activePrincipalMap(id string) *principaldomain.Principal {
	return &principaldomain.Principal{
		ID: id, Username: "user-" + id, Kind: principaldomain.KindHuman,
		Status: principaldomain.StatusActive, Roles: []string{principaldomain.RoleMember},
		Version: 1, CreatedAt: quotaTestNow, UpdatedAt: quotaTestNow,
	}
}

func TestSetPersistsQuotaWithoutProvisioner(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	maps := newFakeMapRepo()
	svc := newTestService(repo, principals, maps, nil)

	q, failures, err := svc.Set(context.Background(), "p-1", 500, "usd", 7, "admin-1")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if q.Amount != 500 || q.Unit != quotadomain.UnitUSD || q.WindowDays != 7 || q.UpdatedBy != "admin-1" {
		t.Fatalf("quota mismatch: %+v", q)
	}
	if len(failures) != 0 {
		t.Fatalf("no provisioner means no propagation failures, got %v", failures)
	}
	if maps.lists != 0 {
		t.Fatalf("no provisioner means no map sweep, got %d lists", maps.lists)
	}
	stored, err := repo.ByPrincipal(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("ByPrincipal: %v", err)
	}
	if stored.Amount != 500 {
		t.Fatalf("persisted quota mismatch: %+v", stored)
	}
}

func TestSetValidatesBeforeTouchingStorage(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	svc := newTestService(repo, principals, newFakeMapRepo(), nil)

	for _, tc := range []struct {
		name    string
		amount  int64
		unit    string
		window  int
		wantErr error
	}{
		{"bad unit", 5, "requests", 7, quotadomain.ErrInvalidUnit},
		{"bad window", 5, "usd", 13, quotadomain.ErrInvalidWindow},
		{"negative amount", -5, "usd", 7, quotadomain.ErrInvalidAmount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := svc.Set(context.Background(), "p-1", tc.amount, tc.unit, tc.window, "admin-1"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
	if repo.upserts != 0 {
		t.Fatalf("invalid input must not persist, got %d upserts", repo.upserts)
	}
}

func TestSetRejectsUnknownPrincipal(t *testing.T) {
	svc := newTestService(newFakeQuotaRepo(), &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{}}, newFakeMapRepo(), nil)
	if _, _, err := svc.Set(context.Background(), "ghost", 5, "usd", 7, "admin-1"); !errors.Is(err, ErrPrincipalNotFound) {
		t.Fatalf("want ErrPrincipalNotFound, got %v", err)
	}
}

func TestGetSurfacesNoQuota(t *testing.T) {
	svc := newTestService(newFakeQuotaRepo(), &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{}}, newFakeMapRepo(), nil)
	if _, err := svc.Get(context.Background(), "p-1"); !errors.Is(err, quotadomain.ErrNoQuota) {
		t.Fatalf("want ErrNoQuota, got %v", err)
	}
}

func TestSetPropagatesBudgetBestEffort(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	maps := newFakeMapRepo()
	m1, _ := provisiondomain.NewIdentityMap("k-1", "litellm", "ref-1", []byte("enc"), quotaTestNow)
	m2, _ := provisiondomain.NewIdentityMap("k-2", "litellm", "ref-2", []byte("enc"), quotaTestNow)
	maps.byID["k-1|litellm"] = m1
	maps.byID["k-2|litellm"] = m2
	maps.owner["k-1|litellm"] = "p-1"
	maps.owner["k-2|litellm"] = "p-1"

	prov := newFakeProvisioner()
	prov.budgetErrs["ref-2"] = errors.New("engine 500")

	svc := newTestService(repo, principals, maps, prov)
	q, failures, err := svc.Set(context.Background(), "p-1", 700, "usd", 30, "admin-1")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if prov.setCalls != 2 {
		t.Fatalf("both active entries must be projected, got %d calls", prov.setCalls)
	}
	if got := prov.setBudget["ref-1"]; got.Amount != 700 || got.WindowDays != 30 {
		t.Fatalf("projected quota mismatch: %+v", got)
	}
	// The authority changed: 200 with the failure listed, never an error.
	if len(failures) != 1 || failures[0].CredentialRef != "ref-2" || failures[0].Message == "" {
		t.Fatalf("failures must name the failing entry: %+v", failures)
	}
	if q.Amount != 700 {
		t.Fatalf("quota must still be returned: %+v", q)
	}
}

func TestSetPropagatesFullOutageWith200Semantics(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	maps := newFakeMapRepo()
	m1, _ := provisiondomain.NewIdentityMap("k-1", "litellm", "ref-1", []byte("enc"), quotaTestNow)
	maps.byID["k-1|litellm"] = m1
	maps.owner["k-1|litellm"] = "p-1"
	prov := newFakeProvisioner()
	prov.budgetErrs["ref-1"] = errors.New("engine down")

	svc := newTestService(repo, principals, maps, prov)
	_, failures, err := svc.Set(context.Background(), "p-1", 700, "usd", 30, "admin-1")
	if err != nil {
		t.Fatalf("a full projection outage must stay a 200 at the handler, got err %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("every failed entry must be listed, got %+v", failures)
	}
	if repo.upserts != 1 {
		t.Fatalf("the authority must change even when the projection is down, got %d upserts", repo.upserts)
	}
}

func TestSetSurfacesMapSweepFailureWith200Semantics(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	maps := newFakeMapRepo()
	maps.listErr = errors.New("db down")
	svc := newTestService(repo, principals, maps, newFakeProvisioner())

	// The sweep itself failed but the authority already changed: the
	// client sees the new quota with a projection-state-unknown marker,
	// never a 500 that would hide the persisted block/unblock.
	q, failures, err := svc.Set(context.Background(), "p-1", 700, "usd", 30, "admin-1")
	if err != nil {
		t.Fatalf("a sweep failure must not fail the set, got %v", err)
	}
	if q.Amount != 700 {
		t.Fatalf("the persisted quota must be returned: %+v", q)
	}
	if repo.upserts != 1 {
		t.Fatalf("the authority change must land before the sweep, got %d upserts", repo.upserts)
	}
	if len(failures) != 1 || failures[0].Message == "" || failures[0].KeyID != "" {
		t.Fatalf("one marker failure expected: %+v", failures)
	}
}

func TestGetReturnsStoredQuota(t *testing.T) {
	repo := newFakeQuotaRepo()
	principals := &fakePrincipalRepo{byID: map[string]*principaldomain.Principal{"p-1": activePrincipalMap("p-1")}}
	svc := newTestService(repo, principals, newFakeMapRepo(), nil)
	if _, _, err := svc.Set(context.Background(), "p-1", 5, "tokens", 1, "admin-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	q, err := svc.Get(context.Background(), "p-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if q.Unit != quotadomain.UnitTokens || q.Amount != 5 {
		t.Fatalf("quota mismatch: %+v", q)
	}
}

var _ quotaport.Repository = (*fakeQuotaRepo)(nil)
var _ principalport.Repository = (*fakePrincipalRepo)(nil)
var _ port.MapRepository = (*fakeMapRepo)(nil)
var _ port.EngineProvisioner = (*fakeProvisioner)(nil)
