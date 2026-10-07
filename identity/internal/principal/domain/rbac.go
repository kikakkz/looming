// SPDX-License-Identifier: Apache-2.0

// RBAC vocabulary and the builtin role-permission table (identity-l1
// §2): permissions are the unit, builtin roles are fixed bundles, and
// the effective set is their union. Custom roles stay a deferred
// slice; the table below is the entire phase-1 policy.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Permission is one capability grant in the component-namespaced
// vocabulary (identity-l1 §2). model:use:* is the wildcard over the
// deployment's model catalog — expansion against the catalog happens
// on the consumer side (the gateway intersects with its own catalog,
// AD-32 envelope); identity never evaluates it.
type Permission string

const (
	PermGatewayUse      Permission = "gateway:use"
	PermIdentityApprove Permission = "identity:approve"
	PermIdentityManage  Permission = "identity:manage"
	PermKeyManageOwn    Permission = "key:manage:own"
	PermModelUseAll     Permission = "model:use:*"
	PermQuotaView       Permission = "quota:view"
)

// ErrUnknownRole rejects role assignments outside the builtin set.
// Custom roles are a named deferral (identity-l1 §8) — until then the
// role vocabulary is exactly {admin, member}.
var ErrUnknownRole = errors.New("identity: unknown role")

// builtinRolePermissions is the immutable builtin table (identity-l1
// §2): admin carries every known permission; member carries the
// self-service bundle. The member set deliberately lacks
// identity:approve/identity:manage — approvals and principal
// management stay admin-only.
var builtinRolePermissions = map[string][]Permission{
	RoleAdmin: {
		PermGatewayUse,
		PermIdentityApprove,
		PermIdentityManage,
		PermKeyManageOwn,
		PermModelUseAll,
		PermQuotaView,
	},
	RoleMember: {
		PermGatewayUse,
		PermKeyManageOwn,
		PermModelUseAll,
		PermQuotaView,
	},
}

// KnownPermissions lists every permission in deterministic order —
// the feed projects effective sets sorted this way, so snapshot bytes
// stay stable across processes (revision/idempotency hygiene).
func KnownPermissions() []Permission {
	return []Permission{
		PermGatewayUse,
		PermIdentityApprove,
		PermIdentityManage,
		PermKeyManageOwn,
		PermModelUseAll,
		PermQuotaView,
	}
}

// EffectivePermissions resolves a role list to its permission union,
// sorted by KnownPermissions order. Unknown roles are ignored —
// assignment validation happens at the aggregate boundary (SetRoles);
// this resolver stays total so the feed projection cannot fail on
// historical rows.
func EffectivePermissions(roles []string) []Permission {
	granted := map[Permission]bool{}
	for _, role := range roles {
		for _, perm := range builtinRolePermissions[role] {
			granted[perm] = true
		}
	}
	var out []Permission
	for _, perm := range KnownPermissions() {
		if granted[perm] {
			out = append(out, perm)
		}
	}
	return out
}

// ValidRole reports whether role is in the builtin vocabulary.
func ValidRole(role string) bool {
	_, ok := builtinRolePermissions[role]
	return ok
}

// normalizeRoles validates, deduplicates (order-preserving first
// occurrence), and requires at least one role — a principal with no
// role has no permissions and no recovery path through the UI.
func normalizeRoles(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", ErrUnknownRole)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if !ValidRole(role) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRole, role)
		}
		if !seen[role] {
			seen[role] = true
			out = append(out, role)
		}
	}
	return out, nil
}

// SetRoles replaces the principal's role set after validation
// (identity-l1 §4: builtin roles immutable, assignment mutable;
// custom roles deferred). The last-active-admin guard lives in the
// adapter's conditional UPDATE, mirroring UpdateStatus.
func (p *Principal) SetRoles(roles []string, now time.Time) error {
	normalized, err := normalizeRoles(roles)
	if err != nil {
		return err
	}
	p.Roles = normalized
	p.UpdatedAt = now
	return nil
}
