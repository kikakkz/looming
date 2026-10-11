// SPDX-License-Identifier: Apache-2.0

package factscmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

// fakeHostsClient scripts the observed-facts read.
type fakeHostsClient struct {
	hosts []joinadapter.ObservedHost
	err   error
}

func (f *fakeHostsClient) Hosts(context.Context, string) ([]joinadapter.ObservedHost, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.hosts, nil
}

const pullFixture = `version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.11"}
hosts:
  - id: node1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 16384, disk_gb: 200, arch: x86_64}
      network: {zone: cloud, egress: false}
  - id: node2
    address: 10.0.0.12
placements:
  - {component: gateway-front, host: node1, ports: {http: 8080}, env_file: /etc/looming/gateway.env}
  - {component: topologyd, host: node1, ports: {http: 8081}, env_file: /etc/looming/topologyd.env}
`

func loadFixture(t *testing.T, content string) (*config.Config, string) {
	t.Helper()
	path := writeTempTopology(t, []byte(content))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg, path
}

// TestPullMergesObservedFactsIntoTheFile is the slice-1.3 journey:
// observed facts replace declared hardware/egress on the matching
// host, a server-minted host is matched by address and gains a block
// from scratch, the zone survives, and the summary names every
// decision.
func TestPullMergesObservedFactsIntoTheFile(t *testing.T) {
	cfg, path := loadFixture(t, pullFixture)
	client := &fakeHostsClient{hosts: []joinadapter.ObservedHost{
		observedHost("node1", "10.0.0.11", &hostdomain.Capabilities{
			Hardware:    hostdomain.HardwareCapabilities{CPUCores: 8, MemoryMB: 32768, DiskGB: 457, Arch: "x86_64"},
			Network:     hostdomain.NetworkCapabilities{Egress: boolPtr(true)},
			CollectedAt: mergeNow,
		}),
		observedHost("host-deadbeef", "10.0.0.12", &hostdomain.Capabilities{
			Hardware:    hostdomain.HardwareCapabilities{CPUCores: 2, MemoryMB: 4096, Arch: "arm64"},
			Network:     hostdomain.NetworkCapabilities{Egress: boolPtr(false)},
			CollectedAt: mergeNow,
		}),
		observedHost("host-cafe", "10.0.0.99", &hostdomain.Capabilities{
			Hardware:    hostdomain.HardwareCapabilities{CPUCores: 1},
			CollectedAt: mergeNow,
		}),
	}}

	var out bytes.Buffer
	require.NoError(t, runPull(context.Background(), &out, cfg, path, client, "tok"))

	assert.Contains(t, out.String(), "node1: capabilities updated (cpu_cores 4→8, memory_mb 16384→32768, disk_gb 200→457, egress false→true)")
	assert.Contains(t, out.String(), "node2: capabilities updated (cpu_cores unset→2, memory_mb unset→4096, arch unset→arm64, egress unset→false)")
	assert.Contains(t, out.String(), "server host host-cafe (10.0.0.99): no matching host in the file; skipped")
	assert.Contains(t, out.String(), "wrote "+path+" — 2 host(s) updated, 0 unchanged")

	// The written file loads through the real validator and carries
	// the merged facts with the zone intact.
	reloaded, err := config.Load(path)
	require.NoError(t, err)
	byID := map[string]config.Host{}
	for _, h := range reloaded.Hosts {
		byID[h.ID] = h
	}
	require.NotNil(t, byID["node1"].Capabilities)
	assert.Equal(t, 8, byID["node1"].Capabilities.Hardware.CPUCores)
	assert.Equal(t, config.ZoneCloud, byID["node1"].Capabilities.Network.Zone)
	require.NotNil(t, byID["node1"].Capabilities.Network.Egress)
	assert.True(t, *byID["node1"].Capabilities.Network.Egress)
	require.NotNil(t, byID["node2"].Capabilities)
	assert.Equal(t, 2, byID["node2"].Capabilities.Hardware.CPUCores)
	assert.Empty(t, byID["node2"].Capabilities.Network.Zone)

	// The file keeps its mode through the atomic rewrite.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestPullWithoutChangesTouchesNothing: identical observations exit
// zero without rewriting the file (no mtime churn, no diff noise).
func TestPullWithoutChangesTouchesNothing(t *testing.T) {
	cfg, path := loadFixture(t, pullFixture)
	client := &fakeHostsClient{hosts: []joinadapter.ObservedHost{
		observedHost("node1", "10.0.0.11", &hostdomain.Capabilities{
			Hardware:    hostdomain.HardwareCapabilities{CPUCores: 4, MemoryMB: 16384, DiskGB: 200, Arch: "x86_64"},
			Network:     hostdomain.NetworkCapabilities{Egress: boolPtr(false)},
			CollectedAt: mergeNow,
		}),
	}}

	var out bytes.Buffer
	require.NoError(t, runPull(context.Background(), &out, cfg, path, client, "tok"))
	assert.Contains(t, out.String(), "node1: unchanged")
	assert.Contains(t, out.String(), "nothing to update")
	assert.NotContains(t, out.String(), "wrote ")

	// node2 has no declared block and no observed facts: the honest
	// report, and still no write.
	client = &fakeHostsClient{hosts: []joinadapter.ObservedHost{
		observedHost("node1", "10.0.0.11", nil),
		observedHost("node2", "10.0.0.12", nil),
	}}
	out.Reset()
	require.NoError(t, runPull(context.Background(), &out, cfg, path, client, "tok"))
	assert.Contains(t, out.String(), "node1: no observed facts on the server; unchanged")
	assert.Contains(t, out.String(), "node2: no observed facts on the server; unchanged")
	assert.Contains(t, out.String(), "nothing to update")
}

// TestPullSurfacesReadErrors: a failing topologyd read fails the
// command before anything is written.
func TestPullSurfacesReadErrors(t *testing.T) {
	cfg, path := loadFixture(t, pullFixture)
	// #nosec G304 -- the path is this test's own temp file.
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	var out bytes.Buffer
	err = runPull(context.Background(), &out, cfg, path, &fakeHostsClient{err: errors.New("boom")}, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read hosts from topologyd")

	// #nosec G304 -- the path is this test's own temp file.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a failed pull must never touch the file")
}

// TestDeriveTopologydURL resolves the server from the file's own
// topologyd placement; every gap degrades to an explicit --server
// ask instead of a guessed URL.
func TestDeriveTopologydURL(t *testing.T) {
	cfg, _ := loadFixture(t, pullFixture)
	assert.Equal(t, "http://10.0.0.11:8081", deriveTopologydURL(cfg))

	cfg, _ = loadFixture(t, `version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.11"}
hosts:
  - {id: node1, address: 10.0.0.11}
placements:
  - {component: gateway-front, host: node1, ports: {http: 8080}, env_file: /etc/looming/gateway.env}
`)
	assert.Equal(t, "", deriveTopologydURL(cfg), "no topologyd placement, no derivation")

	cfg, _ = loadFixture(t, `version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.11"}
hosts:
  - {id: node1, address: 10.0.0.11}
placements:
  - {component: topologyd, host: node1, ports: {http: 8081}, env_file: /etc/looming/topologyd.env}
`)
	assert.Equal(t, "http://10.0.0.11:8081", deriveTopologydURL(cfg), "a file with only topologyd derives fine")
}

// TestPullCommandGuards pin the operator-facing input contract: the
// token is mandatory (flag or env) and an underivable server asks for
// --server explicitly.
func TestPullCommandGuards(t *testing.T) {
	path := writeTempTopology(t, []byte(pullFixture))

	run := func(t *testing.T, args ...string) error {
		t.Helper()
		var out bytes.Buffer
		cmd := New(&out)
		cmd.SetArgs(args)
		return cmd.ExecuteContext(t.Context())
	}

	t.Setenv(serviceTokenEnv, "")
	err := run(t, "pull", "--file", path, "--server", "http://10.0.0.11:8081")
	require.Error(t, err)
	assert.Contains(t, err.Error(), serviceTokenEnv)

	// The server derivation succeeds against the fixture file; with
	// the token in the env the only remaining failure is the network
	// call itself — which proves both guards passed. The fixture
	// points at the discarded port so the dial refuses instantly
	// (AD-25: no slow network in the unit layer).
	t.Setenv(serviceTokenEnv, "tok")
	loopback := `version: 1
access: {mode: public, transport: direct, endpoint: "127.0.0.1"}
hosts:
  - {id: node1, address: 127.0.0.1}
placements:
  - {component: topologyd, host: node1, ports: {http: 1}, env_file: /etc/looming/topologyd.env}
`
	loopbackPath := writeTempTopology(t, []byte(loopback))
	err = run(t, "pull", "--file", loopbackPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read hosts from topologyd")
}
