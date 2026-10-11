// SPDX-License-Identifier: Apache-2.0

package apply_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/apply"
	"github.com/kikakkz/looming/platform/go/bundle"
)

// bundleManifest covers both placements of twoHostConfig (release
// names, per release-l1 §3.4).
const bundleManifest = `
components:
  gateway: v0.1.0
  identity: v0.1.0
  topology: v0.3.0
`

// statelessTwoHostConfig is twoHostConfig without the state section:
// the bundle tests drive the converge through an explicit database
// URL, so no state plane comes up and the docker script stays the two
// host ensures alone.
var statelessTwoHostConfig = strings.Replace(twoHostConfig, `
state:
  postgres: {image: "postgres:16-alpine", env_file: "/etc/looming/postgres.env", data_dir: "/var/lib/looming/postgres", port: 5432}`, "", 1)

// bundleConfig is statelessTwoHostConfig plus the installer-owned
// bundle section (release-l1 §3.5); %s is the manifest path.
var bundleConfig = statelessTwoHostConfig + `
bundle: {version: v0.1.0, manifest: %s}
`

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle-manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func bundleConfigPath(t *testing.T, manifest string) string {
	t.Helper()
	return writeConfig(t, fmt.Sprintf(bundleConfig, manifest))
}

// TestApplyWithBundleRendersImagePins pins release-l1 §5 R2: apply
// with a manifest renders `image:` refs and never builds — the
// converge step's compose documents carry the pinned refs and no
// build block at all.
func TestApplyWithBundleRendersImagePins(t *testing.T) {
	manifest := writeManifest(t, bundleManifest)
	path := bundleConfigPath(t, manifest)

	w := newWorld(t, []scriptedCall{
		{}, // ensure app-1
		{}, // ensure gw-1
	}, nil)
	res, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  path,
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Failed())

	ups := w.composeUpCalls()
	require.Len(t, ups, 2)
	for _, up := range ups {
		assert.NotContains(t, up.stdin, "build:",
			"a pinned converge never builds:\n%s", up.stdin)
	}
	assert.Contains(t, ups[0].stdin, "image: ghcr.io/kikakkz/looming/identity:v0.1.0")
	assert.Contains(t, ups[1].stdin, "image: ghcr.io/kikakkz/looming/gateway:v0.1.0")

	// The bundle version is the operator's selection; it passes
	// through to the report, never cross-validated against the
	// manifest's content (release-l1 does not require it).
	require.NotNil(t, res.Bundle)
	assert.Equal(t, "v0.1.0", res.Bundle.Version)
	assert.Equal(t, manifest, res.Bundle.Manifest)
	assert.Equal(t, "ghcr.io/kikakkz/looming/topology:v0.3.0", res.Bundle.Pins["topology"])
}

// TestApplyBundleManifestMissingComponent: the manifest must cover
// every placed component — apply fails closed, names the missing
// component, and touches nothing.
func TestApplyBundleManifestMissingComponent(t *testing.T) {
	// identityd is placed but unpinned here.
	manifest := writeManifest(t, `
components:
  gateway: v0.1.0
  topology: v0.1.0
`)
	path := bundleConfigPath(t, manifest)

	w := newWorld(t, nil, nil)
	_, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  path,
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identityd",
		"the failure names the un-pinned placement")
	assert.Contains(t, err.Error(), manifest)
	assert.Empty(t, w.runner.calls, "fail closed before any docker call")
	assert.Empty(t, w.opens, "fail closed before the stores open")
}

// TestApplyBundleManifestFileMissing: a declared bundle whose
// manifest file does not exist fails closed at the validation stage —
// before the stores open or any host converge.
func TestApplyBundleManifestFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	path := bundleConfigPath(t, missing)

	w := newWorld(t, nil, nil)
	_, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  path,
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrUnreadable)
	assert.Contains(t, err.Error(), missing)
	assert.Empty(t, w.runner.calls)
	assert.Empty(t, w.opens)
}

// TestApplyBundleManifestInvalidFailsClosed: an unparsable manifest
// (unknown component) is a validation failure, not a warning.
func TestApplyBundleManifestInvalidFailsClosed(t *testing.T) {
	manifest := writeManifest(t, "components: {vault: v0.1.0}\n")
	path := bundleConfigPath(t, manifest)

	w := newWorld(t, nil, nil)
	_, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  path,
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrUnknownComponent)
	assert.Empty(t, w.runner.calls)
}

// TestApplyBundleDryRunRendersPins: the dry-run render shows the same
// pinned compose the converge would ship — the operator inspects
// image refs before converging.
func TestApplyBundleDryRunRendersPins(t *testing.T) {
	manifest := writeManifest(t, bundleManifest)
	path := bundleConfigPath(t, manifest)

	w := newWorld(t, nil, nil)
	res, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  path,
		DryRun:      true,
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.NoError(t, err)
	require.Len(t, res.Artifacts, 2)
	for _, artifact := range res.Artifacts {
		assert.NotContains(t, artifact.Compose, "build:")
	}
	assert.Contains(t, res.Artifacts[0].Compose, "image: ghcr.io/kikakkz/looming/identity:v0.1.0")
	assert.Contains(t, res.Artifacts[1].Compose, "image: ghcr.io/kikakkz/looming/gateway:v0.1.0")
	require.NotNil(t, res.Bundle)
	assert.Equal(t, "v0.1.0", res.Bundle.Version)
	assert.Empty(t, w.runner.calls, "dry-run never touches docker")
}

// TestApplyUnpinnedConfigHasNoBundleInfo: without a bundle section
// the result carries no bundle info and renders exactly the
// source-build path (byte-identity with today's behavior is pinned
// by the render-level diff test and the e2e suite).
func TestApplyUnpinnedConfigHasNoBundleInfo(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure app-1
		scriptedCall{}, // ensure gw-1
	), map[string]string{stateEnvFile: stateEnvFileContent})
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	assert.Nil(t, res.Bundle)
	ups := w.composeUpCalls()
	require.Len(t, ups, 2)
	for _, up := range ups {
		assert.Contains(t, up.stdin, "build:",
			"no bundle section → the source-build render is unchanged")
		assert.NotContains(t, up.stdin, "image: ghcr.io")
	}
}
