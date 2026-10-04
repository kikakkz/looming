// SPDX-License-Identifier: Apache-2.0
package defaultengine

import (
	"context"
	"testing"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

func TestProvisionKeyIsIdempotent(t *testing.T) {
	e := New()
	ref1, err := e.ProvisionKey(context.Background(), "ker", engineplane.QuotaSpec{Amount: 100, Window: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := e.ProvisionKey(context.Background(), "ker", engineplane.QuotaSpec{Amount: 200, Window: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Fatalf("repeated provision must return the same credential: %q vs %q", ref1, ref2)
	}
	rep, err := e.Usage(context.Background(), ref1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limit != 200 {
		t.Fatalf("budget must reflect the latest provision, got %d", rep.Limit)
	}
}

func TestRevokeForcesReprovision(t *testing.T) {
	e := New()
	ref, _ := e.ProvisionKey(context.Background(), "ker", engineplane.QuotaSpec{Amount: 1})
	if err := e.RevokeKey(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Usage(context.Background(), ref); err == nil {
		t.Fatal("revoked credential must not report usage")
	}
}
