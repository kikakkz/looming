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
