// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

func TestNewRenderArtifactRejectsIncomplete(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name                        string
		hostID, kind, hash, content string
	}{
		{"empty host", "", domain.ArtifactKindCompose, "h", "c"},
		{"empty kind", "gw-1", "", "h", "c"},
		{"empty hash", "gw-1", domain.ArtifactKindCompose, "", "c"},
		{"empty content", "gw-1", domain.ArtifactKindCompose, "h", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewRenderArtifact(tc.hostID, tc.kind, tc.hash, tc.content, now)
			assert.ErrorIs(t, err, domain.ErrInvalidArtifact)
		})
	}
}

func TestNewRenderArtifactRejectsUnknownKind(t *testing.T) {
	_, err := domain.NewRenderArtifact("gw-1", "systemd-unit", "h", "c", time.Now())
	assert.ErrorIs(t, err, domain.ErrUnknownArtifactKind)
}

func TestNewRenderArtifactRoundTripsFields(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	a, err := domain.NewRenderArtifact("gw-1", domain.ArtifactKindCompose, "sha256:x", "content", now)
	require.NoError(t, err)
	assert.Equal(t, "gw-1", a.HostID)
	assert.Equal(t, domain.ArtifactKindCompose, a.Kind)
	assert.Equal(t, "sha256:x", a.ContentHash)
	assert.Equal(t, "content", a.Content)
	assert.True(t, a.RenderedAt.Equal(now))
}
