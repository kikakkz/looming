// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnownPermissionsDeterministicOrder(t *testing.T) {
	got := KnownPermissions()
	require.Len(t, got, 6)
	seen := map[Permission]bool{}
	for _, p := range got {
		assert.False(t, seen[p], "duplicate %q", p)
		seen[p] = true
	}
	assert.Equal(t, PermGatewayUse, got[0])
}

func TestEffectivePermissions(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  []Permission
	}{
		{
			name:  "member bundle",
			roles: []string{RoleMember},
			want:  []Permission{PermGatewayUse, PermKeyManageOwn, PermModelUseAll, PermQuotaView},
		},
		{
			name:  "admin is the full set",
			roles: []string{RoleAdmin},
			want:  KnownPermissions(),
		},
		{
			name:  "union deduplicates across roles",
			roles: []string{RoleMember, RoleAdmin, RoleMember},
			want:  KnownPermissions(),
		},
		{
			name:  "unknown roles ignored",
			roles: []string{"custom-future-role", RoleMember},
			want:  []Permission{PermGatewayUse, PermKeyManageOwn, PermModelUseAll, PermQuotaView},
		},
		{
			name:  "empty roles yield empty set",
			roles: nil,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EffectivePermissions(tc.roles))
		})
	}
}

func TestSetRolesValidation(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	p := &Principal{ID: "p1", Status: StatusActive, Roles: []string{RoleMember}, Version: 3}

	t.Run("happy path dedupes and stamps", func(t *testing.T) {
		err := p.SetRoles([]string{RoleMember, RoleAdmin, RoleMember}, now)
		require.NoError(t, err)
		assert.Equal(t, []string{RoleMember, RoleAdmin}, p.Roles)
		assert.Equal(t, now, p.UpdatedAt)
	})

	t.Run("empty set rejected", func(t *testing.T) {
		err := p.SetRoles(nil, now)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownRole))
	})

	t.Run("unknown role rejected", func(t *testing.T) {
		err := p.SetRoles([]string{RoleMember, "team-lead"}, now)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownRole))
	})
}

func TestValidRole(t *testing.T) {
	assert.True(t, ValidRole(RoleAdmin))
	assert.True(t, ValidRole(RoleMember))
	assert.False(t, ValidRole("admin "))
	assert.False(t, ValidRole(""))
}
