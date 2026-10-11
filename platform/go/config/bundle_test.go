// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
)

const bundleSection = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}}
bundle: {version: v0.1.0, manifest: /etc/looming/bundle-manifest.yaml}
`

func TestBundleSectionLoads(t *testing.T) {
	cfg, err := load(t, bundleSection)
	require.NoError(t, err)
	require.NotNil(t, cfg.Bundle)
	assert.Equal(t, "v0.1.0", cfg.Bundle.Version)
	assert.Equal(t, "/etc/looming/bundle-manifest.yaml", cfg.Bundle.Manifest)
}

func TestBundleSectionIsOptional(t *testing.T) {
	cfg, err := load(t, validYAML)
	require.NoError(t, err)
	assert.Nil(t, cfg.Bundle)
}

func TestBundleSectionValidation(t *testing.T) {
	cases := []struct {
		name    string
		section string
		wantMsg string
	}{
		{"empty version", `bundle: {manifest: /etc/looming/bundle-manifest.yaml}`, "bundle.version is required"},
		{"bad version shape", `bundle: {version: "0.1.0", manifest: /etc/looming/bundle-manifest.yaml}`, "bundle.version"},
		{"empty manifest", `bundle: {version: v0.1.0}`, "bundle.manifest is required"},
		{"relative manifest", `bundle: {version: v0.1.0, manifest: bundle/manifest.yaml}`, "must be an absolute path"},
		{"ok", `bundle: {version: v0.1.0, manifest: /etc/looming/bundle-manifest.yaml}`, ""},
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
				require.NotNil(t, cfg.Bundle)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, config.ErrInvalidBundle)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

// TestBundleSectionRejectsUnknownKeys: the strict decoder treats a
// typo'd key under bundle: as a config error, not a silent default —
// the same contract every other section carries.
func TestBundleSectionRejectsUnknownKeys(t *testing.T) {
	_, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
bundle: {version: v0.1.0, manifest: /etc/looming/bundle-manifest.yaml, manifset: typo}
`)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrInvalidYAML)
}
