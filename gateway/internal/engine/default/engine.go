// SPDX-License-Identifier: Apache-2.0
// Package default is the engine slot's default thin implementation
// (AD-29): payload-preserving forwarding plus admin operations backed
// by its own config store. Slice 0 implements the Admin face against
// an in-memory store; Forwarding lands with the forwarding slice.
package defaultengine

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
)

// Engine is the default engine: admin face functional, forward face
// pending its slice. Idempotent ProvisionKey per subject.
type Engine struct {
	mu     sync.Mutex
	keys   map[string]engineplane.CredentialRef // subject → ref
	budget map[engineplane.CredentialRef]engineplane.QuotaSpec
	next   int
}

func New() *Engine {
	return &Engine{
		keys:   map[string]engineplane.CredentialRef{},
		budget: map[engineplane.CredentialRef]engineplane.QuotaSpec{},
	}
}

var _ engineplane.EngineAdmin = (*Engine)(nil)
var _ engineplane.Forwarder = (*Engine)(nil)

// Forward is not implemented in slice 0.
func (e *Engine) Forward(_ context.Context, _ http.ResponseWriter, _ *http.Request) error {
	return engineplane.ErrNotImplemented
}

// ProvisionKey is idempotent per subject: repeats return the existing
// credential (Journey 2 contract, gateway-l1 §6).
func (e *Engine) ProvisionKey(_ context.Context, subject string, budget engineplane.QuotaSpec) (engineplane.CredentialRef, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref, ok := e.keys[subject]; ok {
		e.budget[ref] = budget
		return ref, nil
	}
	e.next++
	ref := engineplane.CredentialRef("default-engine-key-" + itoa(e.next))
	e.keys[subject] = ref
	e.budget[ref] = budget
	return ref, nil
}

func (e *Engine) SetBudget(_ context.Context, ref engineplane.CredentialRef, budget engineplane.QuotaSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.budget[ref]; !ok {
		return errors.New("defaultengine: unknown credential")
	}
	e.budget[ref] = budget
	return nil
}

func (e *Engine) RevokeKey(_ context.Context, ref engineplane.CredentialRef) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for subject, r := range e.keys {
		if r == ref {
			delete(e.keys, subject)
		}
	}
	delete(e.budget, ref)
	return nil
}

func (e *Engine) Usage(_ context.Context, ref engineplane.CredentialRef) (engineplane.UsageReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.budget[ref]
	if !ok {
		return engineplane.UsageReport{}, errors.New("defaultengine: unknown credential")
	}
	return engineplane.UsageReport{Used: 0, Limit: b.Amount}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
