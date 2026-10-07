// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"

	"github.com/kikakkz/looming/gateway/internal/control/app"
)

// snapshotSource stubs the cache read side so the adapter's mapping
// contract is tested without a running writer loop.
type snapshotSource struct {
	snap app.Snapshot
}

func (s snapshotSource) Get() app.Snapshot { return s.snap }

func TestPermissionAllowlistUnknownSubjectEmpty(t *testing.T) {
	a := NewPermissionAllowlist(snapshotSource{snap: app.Snapshot{V: map[string][]string{"ker": {"gpt-5"}}}})
	models, err := a.Models(context.Background(), "ghost")
	if err != nil || len(models) != 0 {
		t.Fatalf("unknown subject must be empty and error-free: %v %v", models, err)
	}
}

func TestPermissionAllowlistServesCatalogIntersectedList(t *testing.T) {
	a := NewPermissionAllowlist(snapshotSource{snap: app.Snapshot{V: map[string][]string{"ker": {"gpt-5", "claude-sonnet"}}}})
	models, err := a.Models(context.Background(), "ker")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0] != "gpt-5" {
		t.Fatalf("projection mismatch: %v", models)
	}
	// The adapter hands out copies — mutating the result must not
	// poison the shared snapshot.
	models[0] = "mutated"
	again, _ := a.Models(context.Background(), "ker")
	if again[0] != "gpt-5" {
		t.Fatalf("snapshot mutated through the returned slice: %v", again)
	}
}
