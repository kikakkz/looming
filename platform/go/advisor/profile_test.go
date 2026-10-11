// SPDX-License-Identifier: Apache-2.0

package advisor_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/advisor"
)

// TestParseShippedProfiles: the embedded document parses and covers
// the whole phase-1 allowlist — the fail-closed cross-check the loader
// guarantees.
func TestParseShippedProfiles(t *testing.T) {
	profiles, err := advisor.Parse(advisor.DefaultProfilesYAML())
	require.NoError(t, err)
	require.Len(t, profiles, 3)
	for _, name := range []string{"gateway-front", "identityd", "topologyd"} {
		require.Contains(t, profiles, name)
	}
}

func TestParseKindDefaultsMergeUnderProfileOverrides(t *testing.T) {
	profiles, err := advisor.Parse([]byte(`
kinds:
  edge:
    hard:
      min_cpu_cores: 1
      min_memory_mb: 512
      needs_egress: true
      arch: [x86_64, arm64]
      ports: [http]
    soft:
      preferred_zone: cloud
      spread: component
profiles:
  gateway-front:
    kind: edge
    hard:
      min_cpu_cores: 2
      min_memory_mb: 1024
  identityd:
    kind: edge
    hard:
      min_memory_mb: 2048
      needs_egress: false
    soft:
      preferred_zone: lan
`))
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	gw := profiles["gateway-front"]
	// Profile overrides win.
	assert.Equal(t, 2, gw.Hard.MinCPUCores)
	assert.Equal(t, 1024, gw.Hard.MinMemoryMB)
	// Kind defaults fill the gaps.
	assert.True(t, gw.Hard.NeedsEgress)
	assert.Equal(t, []string{"x86_64", "arm64"}, gw.Hard.Arch)
	assert.Equal(t, []string{"http"}, gw.Hard.Ports)
	assert.Equal(t, "cloud", gw.Soft.PreferredZone)
	assert.Equal(t, "component", gw.Soft.Spread)

	// Zero-valued override still overrides; list inheritance is
	// replace-when-present, not union.
	loner := profiles["identityd"]
	assert.Equal(t, 1, loner.Hard.MinCPUCores, "unset scalars inherit the kind default")
	assert.False(t, loner.Hard.NeedsEgress, "explicit false overrides the kind's true")
	assert.Equal(t, "lan", loner.Soft.PreferredZone)
	assert.Equal(t, []string{"http"}, loner.Hard.Ports, "absent list inherits the kind's")
}

func TestParseProfileWithoutKindStandsAlone(t *testing.T) {
	profiles, err := advisor.Parse([]byte(`
profiles:
  topologyd:
    hard:
      min_cpu_cores: 1
      arch: [arm64]
    soft:
      spread: component
`))
	require.NoError(t, err)
	p := profiles["topologyd"]
	assert.Equal(t, "", p.Kind)
	assert.Equal(t, 1, p.Hard.MinCPUCores)
	assert.Equal(t, []string{"arm64"}, p.Hard.Arch)
	assert.False(t, p.Hard.NeedsEgress)
	assert.Equal(t, "", p.Soft.PreferredZone)
	assert.Equal(t, "component", p.Soft.Spread)
}

func TestParseSchemaErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr error
	}{
		{
			name:    "empty document",
			yaml:    "  \n",
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name:    "broken yaml",
			yaml:    "profiles: [",
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "unknown key fails closed",
			yaml: `
profiles:
  topologyd:
    hards: {}
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "negative floor",
			yaml: `
profiles:
  topologyd:
    hard:
      min_memory_mb: -1
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "arch outside the vocabulary",
			yaml: `
profiles:
  topologyd:
    hard:
      arch: [riscv]
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "port name outside the vocabulary",
			yaml: `
profiles:
  topologyd:
    hard:
      ports: ["HTTP"]
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "zone outside the vocabulary",
			yaml: `
profiles:
  topologyd:
    soft:
      preferred_zone: wan
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "spread outside the vocabulary",
			yaml: `
profiles:
  topologyd:
    soft:
      spread: rack
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
		{
			name: "dangling kind reference",
			yaml: `
profiles:
  topologyd:
    kind: ghost
`,
			wantErr: advisor.ErrUnknownKind,
		},
		{
			name: "name outside the render allowlist",
			yaml: `
profiles:
  my-database:
    hard:
      min_cpu_cores: 4
`,
			wantErr: advisor.ErrUnknownProfileName,
		},
		{
			name: "kind block with a dead negative value",
			yaml: `
kinds:
  service:
    hard:
      min_disk_gb: -5
profiles:
  topologyd:
    kind: service
`,
			wantErr: advisor.ErrInvalidProfiles,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := advisor.Parse([]byte(tc.yaml))
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestParseAllowlistErrorNamesOffenders: every out-of-allowlist name
// is reported, sorted, so the review-gated data error reads as a
// review checklist.
func TestParseAllowlistErrorNamesOffenders(t *testing.T) {
	_, err := advisor.Parse([]byte(`
profiles:
  zeta:
    hard: {}
  alpha:
    hard: {}
`))
	require.ErrorIs(t, err, advisor.ErrUnknownProfileName)
	assert.Contains(t, err.Error(), "alpha, zeta")
	assert.Contains(t, err.Error(), "gateway-front")
}

// TestParseShippedProfileValues pins the shipped floors — a profile
// change is a knowledge change and must be visible in review.
func TestParseShippedProfileValues(t *testing.T) {
	profiles, err := advisor.Parse(advisor.DefaultProfilesYAML())
	require.NoError(t, err)

	gw := profiles["gateway-front"]
	assert.Equal(t, "edge", gw.Kind)
	assert.Equal(t, 2, gw.Hard.MinCPUCores)
	assert.Equal(t, 1024, gw.Hard.MinMemoryMB)
	assert.True(t, gw.Hard.NeedsEgress)
	assert.Equal(t, []string{"http"}, gw.Hard.Ports)
	assert.Equal(t, "cloud", gw.Soft.PreferredZone)

	id := profiles["identityd"]
	assert.True(t, id.Hard.NeedsEgress)
	assert.Equal(t, "lan", id.Soft.PreferredZone)
	assert.Equal(t, []string{"x86_64", "arm64"}, id.Hard.Arch)

	tp := profiles["topologyd"]
	assert.False(t, tp.Hard.NeedsEgress)
	assert.Equal(t, 1, tp.Hard.MinCPUCores)
	assert.Equal(t, "component", tp.Soft.Spread)
}
