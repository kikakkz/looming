// SPDX-License-Identifier: Apache-2.0

package render_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/render"
	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// TestImageRef pins the manifest→pin resolution: the release registry
// prefix (the same ghcr path .github/workflows/release.yml publishes
// under) plus the release name and the pinned semver tag.
func TestImageRef(t *testing.T) {
	assert.Equal(t, "ghcr.io/kikakkz/looming/gateway:v0.1.0", render.ImageRef("gateway", "v0.1.0"))
	assert.Equal(t, "ghcr.io/kikakkz/looming/topology:v2.0.0", render.ImageRef("topology", "v2.0.0"))
}

// TestReleaseNames pins the manifest's components vocabulary: the
// bundle manifest keys release images by release name (gateway,
// identity, topology — the names release CI publishes under), and
// every render contract maps onto exactly one of them.
func TestReleaseNames(t *testing.T) {
	assert.Equal(t, []string{"gateway", "identity", "topology"}, render.ReleaseNames())
	for _, component := range render.Allowlist() {
		c, ok := render.Lookup(component)
		require.True(t, ok)
		assert.Contains(t, render.ReleaseNames(), c.ReleaseName,
			"every placed component must be manifest-pinable (release-l1 §3.5 coverage check)")
	}
}

// pinnedInput is the canonical pin-test topology: one host, one
// gateway-front placement, source-build contract otherwise.
func pinnedInput() render.Input {
	return render.Input{
		Hosts: []render.Host{{ID: "local", Address: "127.0.0.1"}},
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentGatewayFront,
				HostID:    "local",
				Ports:     map[string]int{"http": 8080},
				Config:    map[string]string{"upstream": "http://10.0.0.13:4000"},
			},
		},
	}
}

// TestPinnedPlacementRendersImageWithoutBuild pins release-l1 §3.5's
// render contract: a pinned component renders `image: <ref>` and NO
// build block — apply pulls and converges, it never builds a pinned
// image.
func TestPinnedPlacementRendersImageWithoutBuild(t *testing.T) {
	ref := render.ImageRef("gateway", "v0.1.0")
	arts, err := render.Render(render.Input{
		Hosts:      pinnedInput().Hosts,
		Placements: pinnedInput().Placements,
		Images:     map[string]string{domain.ComponentGatewayFront: ref},
	})
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Contains(t, arts[0].Compose, "image: "+ref)
	assert.NotContains(t, arts[0].Compose, "build:",
		"a pinned placement must not carry a build block")
	assert.NotContains(t, arts[0].Compose, "GOPROXY",
		"build args belong to the build block, gone with the pin")
	// Everything else about the service survives the pin: ports, env,
	// restart policy.
	assert.Contains(t, arts[0].Compose, "restart: unless-stopped")
	assert.Contains(t, arts[0].Compose, "GATEWAY_LISTEN: :8080")
}

// TestPinDiffersFromUnpinnedOnlyInBuildImage is the byte-identity
// proof for the unpinned path (release-l1 §5 R2): the same topology
// rendered with and without a pin differs ONLY in the build block ↔
// image line swap — every other byte is identical.
func TestPinDiffersFromUnpinnedOnlyInBuildImage(t *testing.T) {
	unpinned, err := render.Render(pinnedInput())
	require.NoError(t, err)
	require.Len(t, unpinned, 1)

	ref := render.ImageRef("gateway", "v0.1.0")
	pinned, err := render.Render(render.Input{
		Hosts:      pinnedInput().Hosts,
		Placements: pinnedInput().Placements,
		Images:     map[string]string{domain.ComponentGatewayFront: ref},
	})
	require.NoError(t, err)
	require.Len(t, pinned, 1)

	assert.NotContains(t, unpinned[0].Compose, "image:")
	assert.NotContains(t, pinned[0].Compose, "build:")

	// Strip the swapped lines from both documents: what remains must
	// be byte-equal, and the stripped sets must be exactly the build
	// block (5 lines) versus the image line (1) — the pin's entire
	// textual footprint.
	buildLines := map[string]bool{
		"        build:":                             true,
		"            args:":                          true,
		"                - GOPROXY":                  true,
		"            context: .":                     true,
		"            dockerfile: gateway/Dockerfile": true,
	}
	strip := func(doc string, drop map[string]bool) []string {
		var out []string
		for _, line := range strings.Split(doc, "\n") {
			if !drop[line] {
				out = append(out, line)
			}
		}
		return out
	}
	assert.Equal(t, strip(unpinned[0].Compose, buildLines),
		strip(pinned[0].Compose, map[string]bool{"        image: " + ref: true}),
		"the pin changes exactly the build block → image line; all other bytes match")
	assert.Equal(t, strings.Count(unpinned[0].Compose, "\n"), strings.Count(pinned[0].Compose, "\n")+4,
		"five build lines are replaced by one image line")
}

// TestPartialPinLeavesUnpinnedComponentsOnBuild: the pin map is
// per-component — a placement absent from it keeps today's build path
// (the PR/e2e and source-development surface, release-l1 §3.5).
func TestPartialPinLeavesUnpinnedComponentsOnBuild(t *testing.T) {
	in := render.Input{
		Hosts: []render.Host{{ID: "local", Address: "127.0.0.1"}},
		Placements: []domain.ComponentPlacement{
			{Component: domain.ComponentGatewayFront, HostID: "local", Ports: map[string]int{"http": 8080}},
			{Component: domain.ComponentTopologyd, HostID: "local", Ports: map[string]int{"http": 8181}},
		},
		Images: map[string]string{domain.ComponentGatewayFront: render.ImageRef("gateway", "v0.1.0")},
	}
	arts, err := render.Render(in)
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Contains(t, arts[0].Compose, "image: ghcr.io/kikakkz/looming/gateway:v0.1.0")
	assert.NotContains(t, arts[0].Compose, "        build:\n            args:\n                - GOPROXY\n            context: .\n            dockerfile: gateway/Dockerfile",
		"the pinned gateway-front lost its build block")
	assert.Contains(t, arts[0].Compose, "context: .",
		"topologyd is unpinned: its cross-module build context stays")
	assert.Contains(t, arts[0].Compose, "dockerfile: topology/Dockerfile")
}

// TestUnknownPinIsIgnored: the renderer only consults pins for
// components actually placed — an entry for an unplaced component
// changes nothing (apply validates coverage upstream, render stays a
// pure function of placed services).
func TestUnknownPinIsIgnored(t *testing.T) {
	in := pinnedInput()
	in.Images = map[string]string{
		domain.ComponentIdentityd: render.ImageRef("identity", "v0.1.0"),
	}
	arts, err := render.Render(in)
	require.NoError(t, err)
	assert.NotContains(t, arts[0].Compose, "image:")
	assert.Contains(t, arts[0].Compose, "build:")
}
