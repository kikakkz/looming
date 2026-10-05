// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kikakkz/looming/topology/internal/host/domain"
)

func TestNewHost(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	labels := []string{"engine"}

	h, err := domain.NewHost("11111111-1111-1111-1111-111111111111", "10.0.0.1", labels, now)
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.1", h.Address)
	assert.Equal(t, []string{"engine"}, h.RoleLabels)
	assert.Equal(t, now, h.JoinedAt)
	assert.Nil(t, h.CredentialHash, "the re-join credential slot stays empty until T2's join mints it")

	labels[0] = "mutated"
	assert.Equal(t, []string{"engine"}, h.RoleLabels, "NewHost must copy the role-label slice")
}

func TestNewHostRequiresIDAndAddress(t *testing.T) {
	now := time.Now()
	_, err := domain.NewHost("", "10.0.0.1", nil, now)
	assert.ErrorIs(t, err, domain.ErrInvalidHostID)
	_, err = domain.NewHost("11111111-1111-1111-1111-111111111111", "", nil, now)
	assert.ErrorIs(t, err, domain.ErrInvalidAddress)
	_, err = domain.NewHost("11111111-1111-1111-1111-111111111111", "   ", nil, now)
	assert.ErrorIs(t, err, domain.ErrInvalidAddress)
}

func TestNewHostRejectsMalformedIDs(t *testing.T) {
	now := time.Now()
	bad := []string{
		"not-a-uuid",
		"111111111111111111111111111111111111",  // no hyphens
		"11111111-1111-1111-1111-11111111111",   // short
		"11111111-1111-1111-1111-1111111111111", // long
		"11111111_1111_1111_1111_111111111111",  // wrong separators
		"gggggggg-gggg-gggg-gggg-gggggggggggg",  // non-hex
		"11111111-1111-1111-1111-11111111111z",  // non-hex tail
	}
	for _, id := range bad {
		_, err := domain.NewHost(id, "10.0.0.1", nil, now)
		assert.ErrorIs(t, err, domain.ErrInvalidHostID, "id %q", id)
	}
}

func TestNewHostAcceptsCanonicalUUIDShapes(t *testing.T) {
	now := time.Now()
	for _, id := range []string{
		"11111111-1111-1111-1111-111111111111",
		"AABBCCDD-EEFF-1122-3344-556677889900", // uppercase hex is a valid UUID
	} {
		_, err := domain.NewHost(id, "10.0.0.1", nil, now)
		assert.NoError(t, err, "id %q", id)
	}
}
