// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	provisiondomain "github.com/kikakkz/looming/identity/internal/provision/domain"
	"github.com/kikakkz/looming/identity/internal/provision/port"
)

var testNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type fakeMapRepo struct {
	byID    map[string]*provisiondomain.IdentityMap
	listErr error
}

func newFakeMapRepo() *fakeMapRepo {
	return &fakeMapRepo{byID: map[string]*provisiondomain.IdentityMap{}}
}

func (f *fakeMapRepo) ListByKey(_ context.Context, keyID string) ([]*provisiondomain.IdentityMap, error) {
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
	delete(f.byID, keyID+"|"+engine)
	return nil
}

func (f *fakeMapRepo) ListByPrincipal(_ context.Context, principalID string, limit, offset int) ([]*provisiondomain.IdentityMap, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	out := []*provisiondomain.IdentityMap{}
	for _, m := range f.byID {
		// Ownership is encoded in the engine field for the fake: the
		// real adapter resolves it through the loom_keys join.
		if m.Engine == "engine:"+principalID {
			cp := *m
			out = append(out, &cp)
		}
	}
	if offset >= len(out) {
		return []*provisiondomain.IdentityMap{}, int64(len(out)), nil
	}
	end := len(out)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return out[offset:end], int64(len(out)), nil
}

func TestInspectPagesPrincipalEntries(t *testing.T) {
	m1, _ := provisiondomain.NewIdentityMap("k-1", "engine:p-1", "ref-1", []byte("enc-1"), testNow)
	m2, _ := provisiondomain.NewIdentityMap("k-2", "engine:p-1", "ref-2", []byte("enc-2"), testNow)
	repo := newFakeMapRepo()
	repo.byID["k-1|engine:p-1"] = m1
	repo.byID["k-2|engine:p-1"] = m2
	m3, _ := provisiondomain.NewIdentityMap("k-3", "engine:p-2", "ref-3", []byte("enc-3"), testNow)
	repo.byID["k-3|engine:p-2"] = m3

	svc := NewService(repo)
	items, total, err := svc.Inspect(context.Background(), "p-1", 0, 0)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("want p-1's two entries, got %d (total %d)", len(items), total)
	}
	for _, m := range items {
		if m.CredentialRef == "" || m.Engine == "" || m.KeyID == "" {
			t.Fatalf("entries must carry the inspect columns: %+v", m)
		}
	}
	// Another principal is invisible.
	items, total, err = svc.Inspect(context.Background(), "p-2", 0, 0)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("p-2 inspect: %v (%d/%d)", err, len(items), total)
	}
}

func TestInspectHonoursPagination(t *testing.T) {
	repo := newFakeMapRepo()
	for i := 0; i < 5; i++ {
		m, _ := provisiondomain.NewIdentityMap(string(rune('a'+i)), "engine:p-1", "ref", []byte("enc"), testNow)
		repo.byID[m.KeyID+"|engine:p-1"] = m
	}
	svc := NewService(repo)
	items, total, err := svc.Inspect(context.Background(), "p-1", 2, 2)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if total != 5 || len(items) != 2 {
		t.Fatalf("page mismatch: %d items of %d", len(items), total)
	}
}

func TestInspectViewNeverCarriesSecrets(t *testing.T) {
	m, _ := provisiondomain.NewIdentityMap("k-1", "engine:p-1", "ref-1", []byte("top-secret-sealed-blob"), testNow)
	repo := newFakeMapRepo()
	repo.byID["k-1|engine:p-1"] = m
	svc := NewService(repo)
	items, _, err := svc.Inspect(context.Background(), "p-1", 0, 0)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	view := mapView(items[0])
	want := map[string]any{
		"key_id":         "k-1",
		"engine":         "engine:p-1",
		"credential_ref": "ref-1",
		"status":         "active",
		"created_at":     "2026-10-06T12:00:00Z",
		"updated_at":     "2026-10-06T12:00:00Z",
	}
	if diff := cmp.Diff(want, view); diff != "" {
		t.Fatalf("view must be reference-only (-want +got):\n%s", diff)
	}
}

var _ port.MapRepository = (*fakeMapRepo)(nil)
