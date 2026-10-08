// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

const bootstrapYAML = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}}
  - {component: identityd, host: only, ports: {http: 8081}, env_file: /etc/looming/identityd.env}
bootstrap: {admin_email: "admin@example.com"}
`

func TestBootstrapSectionLoads(t *testing.T) {
	cfg, err := load(t, bootstrapYAML)
	require.NoError(t, err)
	require.NotNil(t, cfg.Bootstrap)
	assert.Equal(t, "admin@example.com", cfg.Bootstrap.AdminEmail)
}

func TestBootstrapSectionIsOptional(t *testing.T) {
	cfg, err := load(t, validYAML)
	require.NoError(t, err)
	assert.Nil(t, cfg.Bootstrap)
}

func TestBootstrapValidation(t *testing.T) {
	cases := []struct {
		name    string
		section string
		wantMsg string
	}{
		{"missing email", `bootstrap: {}`, "bootstrap.admin_email is required"},
		{"empty email", `bootstrap: {admin_email: "  "}`, "bootstrap.admin_email is required"},
		{"not an address", `bootstrap: {admin_email: "admin"}`, "is not an email address"},
		{"ok with spaces", `bootstrap: {admin_email: "  admin@example.com "}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`+tc.section)
			if tc.wantMsg == "" {
				require.NoError(t, err)
				require.NotNil(t, cfg.Bootstrap)
				assert.Equal(t, "admin@example.com", cfg.Bootstrap.AdminEmail, "surrounding space is trimmed")
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, config.ErrInvalidBootstrap), "error must carry the bootstrap sentinel")
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}
