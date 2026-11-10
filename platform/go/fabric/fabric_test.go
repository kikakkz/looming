// SPDX-License-Identifier: Apache-2.0

package fabric_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/fabric"
)

func validConfig() fabric.NodeConfig {
	return fabric.NodeConfig{
		ControlURL: "https://headscale.example.com",
		AuthKey:    "tskey-auth-k0123456789",
		StateDir:   "/var/lib/looming/fabric",
		Hostname:   "gateway-1",
	}
}

func TestNodeConfigValidateAcceptsValid(t *testing.T) {
	require.NoError(t, validConfig().Validate())
}

func TestNodeConfigValidateRequiresEveryField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fabric.NodeConfig)
	}{
		{"control url", func(c *fabric.NodeConfig) { c.ControlURL = "" }},
		{"auth key", func(c *fabric.NodeConfig) { c.AuthKey = "" }},
		{"state dir", func(c *fabric.NodeConfig) { c.StateDir = "" }},
		{"hostname", func(c *fabric.NodeConfig) { c.Hostname = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, fabric.ErrMissingField)
		})
	}
}

func TestNodeConfigValidateRejectsBadHostnames(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
	}{
		{"uppercase", "Gateway-1"},
		{"leading hyphen", "-gateway"},
		{"trailing hyphen", "gateway-"},
		{"underscore", "gate_way"},
		{"dot", "gateway.example.com"},
		{"space", "gateway 1"},
		{"too long", strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Hostname = tc.hostname
			err := cfg.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, fabric.ErrInvalidHostname)
		})
	}
}
