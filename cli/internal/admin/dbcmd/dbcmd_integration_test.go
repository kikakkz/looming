// SPDX-License-Identifier: Apache-2.0

//go:build integration

package dbcmd

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hostadapter "github.com/kikakkz/looming/platform/go/hostadapter"
	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	"github.com/kikakkz/looming/platform/go/tests/pgtest"
	topologyadapter "github.com/kikakkz/looming/platform/go/topologyadapter"
	topologyapp "github.com/kikakkz/looming/platform/go/topologyapp"
	topologydomain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// TestTokenCommandsRoundTripAgainstPostgres runs create → list →
// revoke → list over the real schema: the admin-side database-direct
// surface (integration layer — the unit layer covers validation only).
func TestTokenCommandsRoundTripAgainstPostgres(t *testing.T) {
	_, dsn := pgtest.NewDBWithDSN(t)
	t.Setenv(databaseURLEnv, dsn)

	create, err := runCmd(t, "create", "--role", "engine", "--ttl", "1h")
	require.NoError(t, err)
	token := regexp.MustCompile(`token:\s+(\S+)`).FindStringSubmatch(create)
	require.Len(t, token, 2, "the raw token must print once: %q", create)
	assert.Contains(t, create, "role:       engine")
	assert.Contains(t, create, "expires_at:")

	list, err := runCmd(t, "list")
	require.NoError(t, err)
	assert.Contains(t, list, "PREFIX")
	assert.Contains(t, list, "engine")
	assert.Contains(t, list, "unused")

	// The list prefix is the revoke vocabulary.
	prefix := regexp.MustCompile(`(?m)^([0-9a-f]{12})\s`).FindStringSubmatch(list)
	require.Len(t, prefix, 2, "list must show a hash prefix: %q", list)

	revoked, err := runCmd(t, "revoke", prefix[1])
	require.NoError(t, err)
	assert.Contains(t, revoked, "revoked join token")

	// The consumed token cannot join: mint again through the service
	// boundary is covered elsewhere; here the list flips to used.
	after, err := runCmd(t, "list")
	require.NoError(t, err)
	require.Contains(t, after, "used")
	assert.False(t, strings.Contains(after, "unused"), "the only token is consumed")

	// Revoking again fails with the used conflict.
	_, err = runCmd(t, "revoke", prefix[1])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already used")

	// An unknown prefix fails clearly.
	_, err = runCmd(t, "revoke", "ffffffffffff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no join token matches")
}

// TestGuideShowRendersOnDemandAgainstPostgres: `guide show` reads the
// persisted guide; with none (or a moved revision) it renders from the
// current topology plus the config facts — here over the real schema.
func TestGuideShowRendersOnDemandAgainstPostgres(t *testing.T) {
	db, dsn := pgtest.NewDBWithDSN(t)
	t.Setenv(databaseURLEnv, dsn)
	path := writeConfig(t, testConfig)

	// No topology declared yet: the render names the gap.
	_, err := runCmd(t, "guide", "show", "--config", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no topology declared")

	// Declare a topology with a gateway-front placement and a host row
	// to resolve: the guide renders the placement URL.
	hostID := "11111111-1111-1111-1111-111111111111"
	_, err = hostadapter.NewRegistry(db).Register(t.Context(), &hostdomain.Host{
		ID: hostID, Address: "10.0.0.11", JoinedAt: time.Now(),
	})
	require.NoError(t, err)
	svc := topologyapp.NewService(topologyadapter.NewStore(db), time.Now)
	_, err = svc.Declare(t.Context(), topologyapp.DeclareInput{
		Hosts: []string{hostID},
		Placements: []topologydomain.ComponentPlacement{
			{Component: topologydomain.ComponentGatewayFront, HostID: hostID, Ports: map[string]int{"http": 8080}},
		},
		Access: topologydomain.Access{Mode: topologydomain.ModePublic, Transport: topologydomain.TransportDirect, Endpoint: topologydomain.EndpointIP},
	})
	require.NoError(t, err)

	first, err := runCmd(t, "guide", "show", "--config", path)
	require.NoError(t, err)
	assert.Contains(t, first, "guide rendered at revision 1")
	assert.Contains(t, first, `"cluster_name": "looming cluster"`)
	assert.Contains(t, first, `"gateway_url": "http://10.0.0.11:8080"`)
	assert.Contains(t, first, `"access_public": true`)

	// Second read is unchanged — no re-render.
	second, err := runCmd(t, "guide", "show", "--config", path)
	require.NoError(t, err)
	assert.Contains(t, second, "unchanged (already current) at revision 1")
}
