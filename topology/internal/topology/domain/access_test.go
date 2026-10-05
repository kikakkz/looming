// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

func TestNewAccessPhase1(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		transport string
		endpoint  string
	}{
		{"public direct ip", "public", "direct", "ip"},
		{"public direct http", "public", "direct", "http"},
		{"private direct ip", "private", "direct", "ip"},
		{"private direct http", "private", "direct", "http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := domain.NewAccess(tc.mode, tc.transport, tc.endpoint)
			assert.NoError(t, err)
			assert.Equal(t, domain.AccessMode(tc.mode), a.Mode)
			assert.Equal(t, domain.Transport(tc.transport), a.Transport)
			assert.Equal(t, domain.Endpoint(tc.endpoint), a.Endpoint)
			assert.NoError(t, a.Validate())
		})
	}
}

func TestNewAccessRejectsBadMode(t *testing.T) {
	for _, mode := range []string{"", "open", "PUBLIC", "internal"} {
		_, err := domain.NewAccess(mode, "direct", "ip")
		assert.ErrorIs(t, err, domain.ErrInvalidAccessMode, "mode %q", mode)
	}
}

func TestNewAccessRejectsPhase2TransportsByIssue(t *testing.T) {
	cases := []struct {
		transport string
		issue     string // the owning phase-2 issue must be named in the error
	}{
		{"vip", "#109"},     // gateway HA
		{"dns", "#111"},     // org DNS
		{"acme", "#110"},    // automated TLS
		{"overlay", "#112"}, // cross-NAT
	}
	for _, tc := range cases {
		t.Run(tc.transport, func(t *testing.T) {
			_, err := domain.NewAccess("public", tc.transport, "ip")
			assert.ErrorIs(t, err, domain.ErrTransportNotPhase1)
			assert.Contains(t, err.Error(), tc.issue)
		})
	}
}

func TestNewAccessRejectsBadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "https", "tcp", "IP"} {
		_, err := domain.NewAccess("public", "direct", endpoint)
		assert.ErrorIs(t, err, domain.ErrInvalidEndpoint, "endpoint %q", endpoint)
	}
}

func TestAccessValidateCatchesHandBuiltValues(t *testing.T) {
	// Hand-built Access values (constructed without NewAccess) fail
	// validation the same way — Declare re-checks at the boundary.
	assert.ErrorIs(t, domain.Access{Mode: "open", Transport: "direct", Endpoint: "ip"}.Validate(), domain.ErrInvalidAccessMode)
	assert.ErrorIs(t, domain.Access{Mode: "public", Transport: "vip", Endpoint: "ip"}.Validate(), domain.ErrTransportNotPhase1)
	assert.ErrorIs(t, domain.Access{Mode: "public", Transport: "direct", Endpoint: "https"}.Validate(), domain.ErrInvalidEndpoint)
}
