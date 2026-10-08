// SPDX-License-Identifier: Apache-2.0

//go:build integration

package adapter_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/tests/pgtest"
	adapter "github.com/kikakkz/looming/platform/go/topologyadapter"
	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

func TestArtifactCurrentNoArtifact(t *testing.T) {
	store := adapter.NewArtifactStore(pgtest.NewDB(t))
	_, err := store.Current(ctx, "gw-1", domain.ArtifactKindCompose)
	assert.ErrorIs(t, err, domain.ErrNoArtifact)
}

func TestArtifactSaveCurrentRoundTrip(t *testing.T) {
	store := adapter.NewArtifactStore(pgtest.NewDB(t))
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	want, err := domain.NewRenderArtifact("gw-1", domain.ArtifactKindCompose,
		"sha256:aaa", "services: {}\n", now)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, want))

	got, err := store.Current(ctx, "gw-1", domain.ArtifactKindCompose)
	require.NoError(t, err)
	// timestamptz scans back in the session location: compare the
	// instant, not the location (the T0 store-test precedent).
	assert.Equal(t, want.HostID, got.HostID)
	assert.Equal(t, want.Kind, got.Kind)
	assert.Equal(t, want.ContentHash, got.ContentHash)
	assert.Equal(t, want.Content, got.Content)
	assert.True(t, got.RenderedAt.Equal(want.RenderedAt))
}

func TestArtifactSaveUpsertsPerHostAndKind(t *testing.T) {
	store := adapter.NewArtifactStore(pgtest.NewDB(t))
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	first, err := domain.NewRenderArtifact("gw-1", domain.ArtifactKindCompose, "sha256:1", "v1", at)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, first))

	// Re-render the same host: the row is replaced, not duplicated.
	second, err := domain.NewRenderArtifact("gw-1", domain.ArtifactKindCompose, "sha256:2", "v2", at.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, second))

	got, err := store.Current(ctx, "gw-1", domain.ArtifactKindCompose)
	require.NoError(t, err)
	assertSameArtifact(t, second, got)

	// A different host (or kind) is an independent row.
	other, err := domain.NewRenderArtifact("app-1", domain.ArtifactKindCompose, "sha256:3", "v3", at)
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, other))

	got, err = store.Current(ctx, "app-1", domain.ArtifactKindCompose)
	require.NoError(t, err)
	assertSameArtifact(t, other, got)
}

// assertSameArtifact compares artifacts by value, with the rendered-at
// instant compared time.Equal (postgres returns session-location times).
func assertSameArtifact(t *testing.T, want, got domain.RenderArtifact) {
	t.Helper()
	assert.Equal(t, want.HostID, got.HostID)
	assert.Equal(t, want.Kind, got.Kind)
	assert.Equal(t, want.ContentHash, got.ContentHash)
	assert.Equal(t, want.Content, got.Content)
	assert.True(t, got.RenderedAt.Equal(want.RenderedAt), "rendered-at instant mismatch: %v vs %v", got.RenderedAt, want.RenderedAt)
}
