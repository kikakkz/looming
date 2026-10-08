// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapter "github.com/kikakkz/looming/platform/go/guideadapter"
	domain "github.com/kikakkz/looming/platform/go/guidedomain"
	"github.com/kikakkz/looming/platform/go/tests/pgtest"
)

var guideCtx = context.Background()

// TestGuideStoreRoundTrip saves a rendered guide and reads it back:
// the snapshot jsonb must survive the round trip byte-for-byte in
// shape, and a second save replaces the row (re-render is a converge).
func TestGuideStoreRoundTrip(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)

	_, err := store.Current(guideCtx)
	assert.ErrorIs(t, err, domain.ErrNoGuide)

	renderedAt := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	want := domain.Guide{
		ID:          domain.SingletonID,
		RenderedRev: 3,
		RenderedAt:  renderedAt,
		Snapshot: domain.Snapshot{
			ClusterName:    "prod cluster",
			AccessPublic:   true,
			CLIDownloadURL: "https://releases.example.com/looming",
			IdentityURL:    "http://10.0.0.12:8081",
			GatewayURL:     "http://10.0.0.11:8080",
			Steps:          []string{"one", "two"},
			RegisterHint:   "ask an admin",
		},
	}
	require.NoError(t, store.Save(guideCtx, want))

	got, err := store.Current(guideCtx)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("guide round trip mismatch (-want +got):\n%s", diff)
	}

	// A newer revision replaces the singleton row — no history.
	want.RenderedRev = 4
	want.Snapshot.ClusterName = "renamed cluster"
	want.RenderedAt = renderedAt.Add(time.Minute)
	require.NoError(t, store.Save(guideCtx, want))

	got, err = store.Current(guideCtx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.RenderedRev)
	assert.Equal(t, "renamed cluster", got.Snapshot.ClusterName)
}

// TestGuideStoreFailurePaths pins the adapter's error envelope: a
// corrupt row, and a missing table (both reads and writes).
func TestGuideStoreFailurePaths(t *testing.T) {
	db := pgtest.NewDB(t)
	store := adapter.NewStore(db)

	// A jsonb-valid but structurally wrong snapshot fails the decode.
	_, err := db.ExecContext(guideCtx,
		`INSERT INTO guide (id, snapshot, rendered_rev, rendered_at) VALUES ($1, '[]', 1, now())`,
		domain.SingletonID)
	require.NoError(t, err)
	_, err = store.Current(guideCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode snapshot")

	// A missing table surfaces as a plain wrapped error, for reads and
	// writes alike.
	_, err = db.ExecContext(guideCtx, `DROP TABLE guide`)
	require.NoError(t, err)
	_, err = store.Current(guideCtx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrNoGuide)
	require.Error(t, store.Save(guideCtx, domain.Render(domain.Facts{Revision: 1, ClusterName: "c"}, time.Now())))
}
