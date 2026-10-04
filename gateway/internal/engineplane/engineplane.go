// SPDX-License-Identifier: Apache-2.0
// Package engineplane is the engine slot contract (AD-32): every
// pluggable engine exposes a Forward face (northbound OpenAI-compatible
// endpoint, plain proxying) and an Admin face (provisioning operations).
// dp and cp depend only on this package — never on engine internals
// (docs/architecture/gateway-l1.md §5, AD-33 matrix).
package engineplane

import (
	"context"
	"errors"
	"net/http"
)

// ErrNotImplemented marks engine faces a slice has not built yet.
var ErrNotImplemented = errors.New("engineplane: not implemented in this slice")

// QuotaSpec is the normalized budget description passed to an engine.
// Adapters translate it to engine-native semantics; per-engine variance
// is documented per adapter (AD-32).
type QuotaSpec struct {
	Amount int64  // budget units (adapter-defined: tokens, cents, …)
	Window string // e.g. "daily", "monthly", "total"
	Scope  string // e.g. "user", "key"
}

// CredentialRef identifies a provisioned engine-side credential.
type CredentialRef string

// Forwarder is the engine's data face: payload-preserving forwarding.
// The model id travels inside the request untouched (AD-32: the front
// layer does not route).
type Forwarder interface {
	Forward(ctx context.Context, w http.ResponseWriter, r *http.Request) error
}

// EngineAdmin is the engine's control face: provisioning operations.
// Engines without admin capability are forward-only (gateway-l1 §7).
type EngineAdmin interface {
	// ProvisionKey is idempotent per (subject, engine): repeating the
	// same subject returns the existing credential.
	ProvisionKey(ctx context.Context, subject string, budget QuotaSpec) (CredentialRef, error)
	SetBudget(ctx context.Context, ref CredentialRef, budget QuotaSpec) error
	RevokeKey(ctx context.Context, ref CredentialRef) error
	Usage(ctx context.Context, ref CredentialRef) (UsageReport, error)
}

// UsageReport is the reconciliation anchor (AD-32 #5): engine-side
// usage as the engine counts it. Discrepancy against MeterRecords is an
// implementation-bug signal, never a display correction.
type UsageReport struct {
	Used  int64
	Limit int64
}
