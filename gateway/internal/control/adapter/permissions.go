// SPDX-License-Identifier: Apache-2.0

// PermissionAllowlist serves the front layer's model-permission
// question from the control plane's ModelAllowlistCache — the
// slice-D replacement for the retired static GATEWAY_ALLOWLISTS stub.
// The cache is the identity feed's effective-permission projection
// expanded against the gateway's model catalog (AD-32: the catalog is
// engine-side configuration; identity never evaluates model:use:*).
package adapter

import (
	"context"

	"github.com/kikakkz/looming/gateway/internal/control/app"
)

// ModelAllowlistSource is the cache surface this adapter needs; the
// concrete *app.ModelAllowlistCache satisfies it structurally.
type ModelAllowlistSource interface {
	Get() app.Snapshot
}

// PermissionAllowlist answers Models(subject) from the projection
// cache. A subject the projection does not know yields an empty list
// — the front layer's ModelAllowed denies on empty (fail closed), so
// an unprojected subject is indistinguishable from a denied one.
type PermissionAllowlist struct {
	cache ModelAllowlistSource
}

// NewPermissionAllowlist wires the adapter over the projection cache.
func NewPermissionAllowlist(cache ModelAllowlistSource) PermissionAllowlist {
	return PermissionAllowlist{cache: cache}
}

// Models returns the subject's catalog-intersected model list.
func (p PermissionAllowlist) Models(_ context.Context, subject string) ([]string, error) {
	models, ok := p.cache.Get().V[subject]
	if !ok {
		return nil, nil
	}
	return append([]string(nil), models...), nil
}
