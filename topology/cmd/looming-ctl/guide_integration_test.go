// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	topologyadapter "github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/internal/topology/app"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
	"github.com/kikakkz/looming/topology/tests/pgtest"
)

// TestGuideShowRendersOnDemandAgainstPostgres: `guide show` reads the
// persisted guide; with none (or a moved revision) it renders from the
// current topology plus the config facts — here over the real schema.
func TestGuideShowRendersOnDemandAgainstPostgres(t *testing.T) {
	db, dsn := pgtest.NewDBWithDSN(t)
	t.Setenv(databaseURLEnv, dsn)
	path := writeCtlConfig(t, ctlTestConfig)

	// No topology declared yet: the render names the gap.
	_, err := runWith(t, "guide", "show", "--config", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no topology declared")

	// Declare a topology with a gateway-front placement and a host row
	// to resolve: the guide renders the placement URL.
	hostID := "11111111-1111-1111-1111-111111111111"
	_, err = hostadapter.NewRegistry(db).Register(t.Context(), &hostdomain.Host{
		ID: hostID, Address: "10.0.0.11", JoinedAt: time.Now(),
	})
	require.NoError(t, err)
	svc := app.NewService(topologyadapter.NewStore(db), time.Now)
	_, err = svc.Declare(t.Context(), app.DeclareInput{
		Hosts: []string{hostID},
		Placements: []domain.ComponentPlacement{
			{Component: domain.ComponentGatewayFront, HostID: hostID, Ports: map[string]int{"http": 8080}},
		},
		Access: domain.Access{Mode: domain.ModePublic, Transport: domain.TransportDirect, Endpoint: domain.EndpointIP},
	})
	require.NoError(t, err)

	first, err := runWith(t, "guide", "show", "--config", path)
	require.NoError(t, err)
	assert.Contains(t, first, "guide rendered at revision 1")
	assert.Contains(t, first, `"cluster_name": "looming cluster"`)
	assert.Contains(t, first, `"gateway_url": "http://10.0.0.11:8080"`)
	assert.Contains(t, first, `"access_public": true`)

	// Second read is unchanged — no re-render.
	second, err := runWith(t, "guide", "show", "--config", path)
	require.NoError(t, err)
	assert.Contains(t, second, "unchanged (already current) at revision 1")
}
