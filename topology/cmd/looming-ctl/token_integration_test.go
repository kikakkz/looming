// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/tests/pgtest"
)

// TestTokenCommandsRoundTripAgainstPostgres runs create → list →
// revoke → list over the real schema: the ctl's admin-side surface
// (integration layer — the unit layer covers validation only).
func TestTokenCommandsRoundTripAgainstPostgres(t *testing.T) {
	_, dsn := pgtest.NewDBWithDSN(t)
	t.Setenv(databaseURLEnv, dsn)

	create, err := runWith(t, "token", "create", "--role", "engine", "--ttl", "1h")
	require.NoError(t, err)
	token := regexp.MustCompile(`token:\s+(\S+)`).FindStringSubmatch(create)
	require.Len(t, token, 2, "the raw token must print once: %q", create)
	assert.Contains(t, create, "role:       engine")
	assert.Contains(t, create, "expires_at:")

	list, err := runWith(t, "token", "list")
	require.NoError(t, err)
	assert.Contains(t, list, "PREFIX")
	assert.Contains(t, list, "engine")
	assert.Contains(t, list, "unused")

	// The list prefix is the revoke vocabulary.
	prefix := regexp.MustCompile(`(?m)^([0-9a-f]{12})\s`).FindStringSubmatch(list)
	require.Len(t, prefix, 2, "list must show a hash prefix: %q", list)

	revoked, err := runWith(t, "token", "revoke", prefix[1])
	require.NoError(t, err)
	assert.Contains(t, revoked, "revoked join token")

	// The consumed token cannot join: mint again through the service
	// boundary is covered elsewhere; here the list flips to used.
	after, err := runWith(t, "token", "list")
	require.NoError(t, err)
	require.Contains(t, after, "used")
	assert.False(t, strings.Contains(after, "unused"), "the only token is consumed")

	// Revoking again fails with the used conflict.
	_, err = runWith(t, "token", "revoke", prefix[1])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already used")

	// An unknown prefix fails clearly.
	_, err = runWith(t, "token", "revoke", "ffffffffffff")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no join token matches")
}
