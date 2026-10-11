// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	app "github.com/kikakkz/looming/platform/go/hostapp"
	domain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// fakeRegistry is the host Registry double: call log plus scripted
// errors for the pass-through contract tests.
type fakeRegistry struct {
	registered []*domain.Host
	updated    []*domain.Host
	byID       map[string]*domain.Host
	byAddress  map[string]*domain.Host

	registerErr error
	byIDErr     error
	byAddrErr   error
	updateErr   error
	listErr     error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		byID:      map[string]*domain.Host{},
		byAddress: map[string]*domain.Host{},
	}
}

func (f *fakeRegistry) Register(_ context.Context, h *domain.Host) (*domain.Host, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	f.registered = append(f.registered, h)
	f.byID[h.ID] = h
	f.byAddress[h.Address] = h
	return h, nil
}

func (f *fakeRegistry) ByID(_ context.Context, id string) (*domain.Host, error) {
	if f.byIDErr != nil {
		return nil, f.byIDErr
	}
	h, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return h, nil
}

func (f *fakeRegistry) ByAddress(_ context.Context, address string) (*domain.Host, error) {
	if f.byAddrErr != nil {
		return nil, f.byAddrErr
	}
	h, ok := f.byAddress[address]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return h, nil
}

func (f *fakeRegistry) Update(_ context.Context, h *domain.Host) (*domain.Host, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = append(f.updated, h)
	f.byID[h.ID] = h
	f.byAddress[h.Address] = h
	return h, nil
}

func (f *fakeRegistry) List(_ context.Context) ([]domain.Host, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.Host, 0, len(f.registered))
	for _, h := range f.registered {
		out = append(out, *h)
	}
	return out, nil
}

func TestServiceDelegatesToRegistry(t *testing.T) {
	svc, reg := newService()

	h, err := svc.Register(ctx, hostAt("10.0.0.1"))
	assert.NoError(t, err)
	assert.Len(t, reg.registered, 1)

	got, err := svc.ByID(ctx, h.ID)
	assert.NoError(t, err)
	assert.Equal(t, h, got)

	got, err = svc.ByAddress(ctx, "10.0.0.1")
	assert.NoError(t, err)
	assert.Equal(t, h, got)

	moved := *h
	moved.Address = "10.0.0.2"
	updated, err := svc.Update(ctx, &moved)
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.2", updated.Address)
	assert.Len(t, reg.updated, 1)
}

func TestServicePropagatesDomainErrors(t *testing.T) {
	svc, _ := newService()
	h := hostAt("10.0.0.1")

	_, err := svc.Register(ctx, h)
	assert.NoError(t, err)

	// Registry-sentinel errors propagate unchanged: the service is a
	// pass-through and must not rewrite the domain contract.
	other := hostAt("10.0.0.2")
	reg := newFakeRegistry()
	reg.registerErr = domain.ErrAddressTaken
	svc = app.NewService(reg)
	_, err = svc.Register(ctx, other)
	assert.ErrorIs(t, err, domain.ErrAddressTaken)

	_, err = svc.ByID(ctx, "33333333-3333-3333-3333-333333333333")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	_, err = svc.ByAddress(ctx, "10.9.9.9")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestServicePropagatesRegistryFailures(t *testing.T) {
	boom := errors.New("db unavailable")
	reg := newFakeRegistry()

	reg.registerErr = boom
	svc := app.NewService(reg)
	_, err := svc.Register(ctx, hostAt("10.0.0.1"))
	assert.ErrorIs(t, err, boom)

	reg = newFakeRegistry()
	reg.byIDErr = boom
	svc = app.NewService(reg)
	_, err = svc.ByID(ctx, "id")
	assert.ErrorIs(t, err, boom)

	reg = newFakeRegistry()
	reg.byAddrErr = boom
	svc = app.NewService(reg)
	_, err = svc.ByAddress(ctx, "10.0.0.1")
	assert.ErrorIs(t, err, boom)

	reg = newFakeRegistry()
	reg.updateErr = boom
	svc = app.NewService(reg)
	_, err = svc.Update(ctx, hostAt("10.0.0.1"))
	assert.ErrorIs(t, err, boom)
}

var ctx = context.Background()

func newService() (*app.Service, *fakeRegistry) {
	reg := newFakeRegistry()
	return app.NewService(reg), reg
}

func hostAt(address string) *domain.Host {
	h, err := domain.NewHost("11111111-1111-1111-1111-111111111111", address, []string{"engine"}, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		panic(err)
	}
	return h
}
