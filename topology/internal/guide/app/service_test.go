// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/guide/app"
	"github.com/kikakkz/looming/topology/internal/guide/domain"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	topologydomain "github.com/kikakkz/looming/topology/internal/topology/domain"
)

var fixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeGuides is the guide store seam scripted by the tests.
type fakeGuides struct {
	guide   domain.Guide
	has     bool
	saved   []domain.Guide
	saveErr error
}

func (f *fakeGuides) Current(_ context.Context) (domain.Guide, error) {
	if !f.has {
		return domain.Guide{}, domain.ErrNoGuide
	}
	return f.guide, nil
}

func (f *fakeGuides) Save(_ context.Context, g domain.Guide) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, g)
	f.guide, f.has = g, true
	return nil
}

// fakeTopology serves the persisted Topology snapshot.
type fakeTopology struct {
	current topologydomain.Topology
	err     error
}

func (f *fakeTopology) Current(_ context.Context) (topologydomain.Topology, error) {
	if f.err != nil {
		return topologydomain.Topology{}, f.err
	}
	return f.current, nil
}

func (f *fakeTopology) Save(_ context.Context, _ topologydomain.Topology) error {
	return errors.New("not used")
}

// fakeRegistry resolves placement host ids to addresses.
type fakeRegistry struct {
	hosts map[string]*hostdomain.Host
}

func (f *fakeRegistry) Register(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	return h, nil
}

func (f *fakeRegistry) ByID(_ context.Context, id string) (*hostdomain.Host, error) {
	h, ok := f.hosts[id]
	if !ok {
		return nil, hostdomain.ErrNotFound
	}
	return h, nil
}

func (f *fakeRegistry) ByAddress(_ context.Context, _ string) (*hostdomain.Host, error) {
	return nil, hostdomain.ErrNotFound
}

func (f *fakeRegistry) Update(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	return h, nil
}

// placedTopology is a revision-3 public topology with the phase-1 pair
// placed: identityd on app-1, gateway-front on gw-1.
func placedTopology() topologydomain.Topology {
	return topologydomain.Topology{
		ID:       topologydomain.SingletonID,
		Revision: 3,
		Access: topologydomain.Access{
			Mode:      topologydomain.ModePublic,
			Transport: topologydomain.TransportDirect,
			Endpoint:  topologydomain.EndpointIP,
		},
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentIdentityd, HostID: "app-1", Ports: map[string]int{"http": 8081}},
			{Component: topologydomain.ComponentGatewayFront, HostID: "gw-1", Ports: map[string]int{"http": 8080}},
		},
	}
}

func newWorld() (*fakeGuides, *fakeTopology, *fakeRegistry, *app.Service) {
	guides := &fakeGuides{}
	topo := &fakeTopology{current: placedTopology()}
	registry := &fakeRegistry{hosts: map[string]*hostdomain.Host{
		"gw-1":  {ID: "gw-1", Address: "10.0.0.11"},
		"app-1": {ID: "app-1", Address: "10.0.0.12"},
	}}
	svc := app.NewService(guides, topo, registry, func() time.Time { return fixedNow })
	return guides, topo, registry, svc
}

func facts() app.Facts {
	return app.Facts{ClusterName: "prod cluster", CLIDownloadURL: "https://releases.example.com/looming"}
}

func TestEnsureRenderedRendersAndPersistsWhenAbsent(t *testing.T) {
	guides, _, _, svc := newWorld()
	g, rendered, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)
	assert.True(t, rendered)

	assert.Equal(t, int64(3), g.RenderedRev)
	assert.Equal(t, "http://10.0.0.12:8081", g.Snapshot.IdentityURL)
	assert.Equal(t, "http://10.0.0.11:8080", g.Snapshot.GatewayURL)
	assert.Equal(t, "prod cluster", g.Snapshot.ClusterName)
	assert.True(t, g.Snapshot.AccessPublic)

	require.Len(t, guides.saved, 1)
	assert.Equal(t, g, guides.saved[0])
}

func TestEnsureRenderedSkipsWhenRevisionUnchanged(t *testing.T) {
	guides, _, _, svc := newWorld()
	first, _, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)

	second, rendered, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)
	assert.False(t, rendered, "same revision must not re-render (idempotency)")
	assert.Equal(t, first, second)
	assert.Len(t, guides.saved, 1, "no second write")
}

func TestEnsureRenderedRerendersOnNewRevision(t *testing.T) {
	guides, topo, _, svc := newWorld()
	_, _, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)

	topo.current.Revision = 4
	g, rendered, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)
	assert.True(t, rendered)
	assert.Equal(t, int64(4), g.RenderedRev)
	assert.Len(t, guides.saved, 2)
}

func TestEnsureRenderedReflectsPrivateAccess(t *testing.T) {
	_, topo, _, svc := newWorld()
	topo.current.Access.Mode = topologydomain.ModePrivate

	g, rendered, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)
	assert.True(t, rendered)
	assert.False(t, g.Snapshot.AccessPublic)
}

func TestEnsureRenderedDegradesMissingPlacements(t *testing.T) {
	_, topo, _, svc := newWorld()
	// Drop the identityd placement: the guide renders with an empty
	// identity URL instead of failing.
	topo.current.Placements = []topologydomain.ComponentPlacement{
		{Component: topologydomain.ComponentGatewayFront, HostID: "gw-1", Ports: map[string]int{"http": 8080}},
	}

	g, _, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)
	assert.Empty(t, g.Snapshot.IdentityURL)
	assert.Equal(t, "http://10.0.0.11:8080", g.Snapshot.GatewayURL)
}

func TestEnsureRenderedPropagatesStoreFailures(t *testing.T) {
	t.Run("topology read", func(t *testing.T) {
		_, topo, _, svc := newWorld()
		topo.err = errors.New("db down")
		_, _, err := svc.EnsureRendered(context.Background(), facts())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db down")
	})
	t.Run("guide save", func(t *testing.T) {
		guides, _, _, svc := newWorld()
		guides.saveErr = errors.New("write failed")
		_, _, err := svc.EnsureRendered(context.Background(), facts())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write failed")
	})
}

func TestCurrentWrapsStore(t *testing.T) {
	guides, _, _, svc := newWorld()
	_, err := svc.Current(context.Background())
	assert.ErrorIs(t, err, domain.ErrNoGuide)

	guides.guide = domain.Render(domain.Facts{Revision: 9, ClusterName: "c"}, fixedNow)
	guides.has = true
	g, err := svc.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(9), g.RenderedRev)
}

// TestEnsureRenderedRerendersOnConfigFactChange: the revision alone is
// not the freshness signal — config-level facts (cluster name, CLI
// URL) do not move the Topology revision, and a stale fact must
// re-render anyway (review finding on #122).
func TestEnsureRenderedRerendersOnConfigFactChange(t *testing.T) {
	guides, _, _, svc := newWorld()
	_, _, err := svc.EnsureRendered(context.Background(), facts())
	require.NoError(t, err)

	renamed := facts()
	renamed.ClusterName = "renamed cluster"
	g, rendered, err := svc.EnsureRendered(context.Background(), renamed)
	require.NoError(t, err)
	assert.True(t, rendered, "same revision but a changed config fact: re-render")
	assert.Equal(t, "renamed cluster", g.Snapshot.ClusterName)
	assert.Len(t, guides.saved, 2)
}
