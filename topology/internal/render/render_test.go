// SPDX-License-Identifier: Apache-2.0

package render_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/render"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

var update = flag.Bool("update", false, "rewrite golden files")

// golden renders the input and compares the first non-empty artifact
// against testdata/<name>.golden (rewriting it under -update).
func golden(t *testing.T, name string, in render.Input) []render.Artifact {
	t.Helper()
	artifacts, err := render.Render(in)
	require.NoError(t, err)
	var nonEmpty []render.Artifact
	for _, a := range artifacts {
		if !a.Empty {
			nonEmpty = append(nonEmpty, a)
		}
	}
	require.Len(t, nonEmpty, 1, "golden inputs render exactly one non-empty host")
	artifacts = nonEmpty

	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(artifacts[0].Compose), 0o600))
	}
	//nolint:gosec // testdata path constructed from the test's own name.
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing? run with -update after eyeballing")
	assert.Equal(t, string(want), artifacts[0].Compose,
		"compose drifted from golden; run with -update only after reviewing the diff")
	return artifacts
}

func twoHosts() []render.Host {
	return []render.Host{
		{ID: "gw-1", Address: "10.0.0.11", SSHUser: "root"},
		{ID: "app-1", Address: "10.0.0.12", SSHUser: "root"},
	}
}

func TestGoldenGatewayFront(t *testing.T) {
	arts := golden(t, "gateway-front", render.Input{
		Hosts: twoHosts(),
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentGatewayFront,
				HostID:    "gw-1",
				Ports:     map[string]int{"http": 8080},
				Config:    map[string]string{"upstream": "http://10.0.0.13:4000", "identity_url": "http://10.0.0.12:8081", "identity_insecure": "1"},
			},
			{
				// The gateway-front template derives GATEWAY_TOPOLOGY_URL
				// from the topologyd placement (T3) — co-located here so
				// the golden still renders exactly one non-empty host.
				Component: domain.ComponentTopologyd,
				HostID:    "gw-1",
				Ports:     map[string]int{"http": 8181},
				Config:    map[string]string{"database_url": "postgres://postgres:pw@10.0.0.11:5432/topology"},
			},
		},
	})
	assert.Equal(t, "gw-1", arts[0].HostID)
	assert.Equal(t, render.Hash(arts[0].Compose), arts[0].Hash)
	assert.Contains(t, arts[0].Compose, "GATEWAY_TOPOLOGY_URL: http://10.0.0.11:8181")
}

// TestGatewayFrontWithoutTopologydOmitsGuideURL: no topologyd
// placement → the guide env stays absent and the gateway's route 404s
// as "not configured" — the operator never sees a dangling URL.
func TestGatewayFrontWithoutTopologydOmitsGuideURL(t *testing.T) {
	artifacts, err := render.Render(render.Input{
		Hosts: twoHosts(),
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentGatewayFront,
				HostID:    "gw-1",
				Ports:     map[string]int{"http": 8080},
				Config:    map[string]string{"upstream": "http://10.0.0.13:4000"},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, artifacts, 2)
	assert.NotContains(t, artifacts[0].Compose, "GATEWAY_TOPOLOGY_URL")
}

func TestGoldenIdentityd(t *testing.T) {
	golden(t, "identityd", render.Input{
		Hosts: twoHosts(),
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentIdentityd,
				HostID:    "app-1",
				Ports:     map[string]int{"http": 8081},
				Config:    map[string]string{"database_url": "postgres://postgres:pw@10.0.0.11:5432/identity"},
			},
		},
	})
}

// TestGoldenTopologyd pins the T2 join service's compose shape:
// the topology image with the service binary as entrypoint and the
// TOPOLOGY_* env contract.
func TestGoldenTopologyd(t *testing.T) {
	arts := golden(t, "topologyd", render.Input{
		Hosts: twoHosts(),
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentTopologyd,
				HostID:    "app-1",
				Ports:     map[string]int{"http": 8081},
				Config:    map[string]string{"database_url": "postgres://postgres:pw@10.0.0.11:5432/topology"},
			},
		},
	})
	assert.Contains(t, arts[0].Compose, "entrypoint:")
	assert.Contains(t, arts[0].Compose, "topologyd")
	assert.Contains(t, arts[0].Compose, "TOPOLOGY_LISTEN: :8081")
}

func TestGoldenStateHostPostgres(t *testing.T) {
	arts := golden(t, "state-postgres", render.Input{
		StateHostID: "gw-1",
		State: &render.StatePostgres{
			Image:   "postgres:16-alpine",
			EnvFile: "/etc/looming/postgres.env",
			DataDir: "/var/lib/looming/postgres",
			Port:    5432,
		},
		Hosts: twoHosts(),
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentGatewayFront,
				HostID:    "gw-1",
				Ports:     map[string]int{"http": 8080},
				Config:    map[string]string{"upstream": "http://10.0.0.13:4000"},
			},
		},
	})
	assert.Contains(t, arts[0].Compose, "bundle-postgres:")
}

func TestStateComposeIsStandalonePostgres(t *testing.T) {
	compose, err := render.StateCompose(render.StatePostgres{
		Image:   "postgres:16-alpine",
		EnvFile: "/etc/looming/postgres.env",
		DataDir: "/var/lib/looming/postgres",
		Port:    5432,
	})
	require.NoError(t, err)
	golden(t, "state-postgres-only", render.Input{
		StateHostID: "gw-1",
		State: &render.StatePostgres{
			Image:   "postgres:16-alpine",
			EnvFile: "/etc/looming/postgres.env",
			DataDir: "/var/lib/looming/postgres",
			Port:    5432,
		},
		Hosts: []render.Host{{ID: "gw-1", Address: "10.0.0.11"}},
	})
	assert.Equal(t, golden(t, "state-postgres-only", render.Input{
		StateHostID: "gw-1",
		State: &render.StatePostgres{
			Image:   "postgres:16-alpine",
			EnvFile: "/etc/looming/postgres.env",
			DataDir: "/var/lib/looming/postgres",
			Port:    5432,
		},
		Hosts: []render.Host{{ID: "gw-1", Address: "10.0.0.11"}},
	})[0].Compose, compose, "StateCompose must match the state host's embedded service")
}

func TestRenderIsDeterministicAcrossInputOrder(t *testing.T) {
	build := func() []render.Artifact {
		arts, err := render.Render(render.Input{
			Hosts: []render.Host{
				{ID: "app-1", Address: "10.0.0.12"},
				{ID: "gw-1", Address: "10.0.0.11"},
			},
			Placements: []domain.ComponentPlacement{
				{Component: domain.ComponentIdentityd, HostID: "app-1", Ports: map[string]int{"http": 8081}},
				{Component: domain.ComponentGatewayFront, HostID: "gw-1", Ports: map[string]int{"http": 8080}},
			},
		})
		require.NoError(t, err)
		return arts
	}
	first, second := build(), build()
	require.Len(t, first, 2)
	assert.Equal(t, first, second, "same input must render identical artifacts (hash stability)")
	assert.Equal(t, "app-1", first[0].HostID, "hosts render in sorted id order")
	assert.Equal(t, "gw-1", first[1].HostID)
}

func TestRenderEmitsEmptyArtifactForIdleHosts(t *testing.T) {
	arts, err := render.Render(render.Input{
		Hosts: []render.Host{
			{ID: "gw-1", Address: "10.0.0.11"},
			{ID: "idle-1", Address: "10.0.0.99"},
		},
		Placements: []domain.ComponentPlacement{
			{Component: domain.ComponentGatewayFront, HostID: "gw-1", Ports: map[string]int{"http": 8080}},
		},
	})
	require.NoError(t, err)
	require.Len(t, arts, 2, "every declared host gets an artifact; idle hosts render empty")
	assert.Equal(t, "gw-1", arts[0].HostID)
	assert.False(t, arts[0].Empty)
	assert.Equal(t, "idle-1", arts[1].HostID)
	assert.True(t, arts[1].Empty)
	assert.Contains(t, arts[1].Compose, "services: {}")
}

func TestRenderEmitsPlacementEnvFile(t *testing.T) {
	arts, err := render.Render(render.Input{
		Hosts: []render.Host{{ID: "app-1", Address: "10.0.0.12"}},
		Placements: []domain.ComponentPlacement{
			{Component: domain.ComponentIdentityd, HostID: "app-1", Ports: map[string]int{"http": 8081}},
		},
		EnvFiles: map[string]string{"app-1\x00" + domain.ComponentIdentityd: "/etc/looming/identityd.env"},
	})
	require.NoError(t, err)
	require.Len(t, arts, 1)
	assert.Contains(t, arts[0].Compose, "env_file:")
	assert.Contains(t, arts[0].Compose, "/etc/looming/identityd.env")
}

func TestRenderRejectsEnvNameCollision(t *testing.T) {
	_, err := render.Render(render.Input{
		Hosts: []render.Host{{ID: "app-1", Address: "10.0.0.12"}},
		Placements: []domain.ComponentPlacement{
			{
				Component: domain.ComponentIdentityd,
				HostID:    "app-1",
				Ports:     map[string]int{"http": 8081},
				Config:    map[string]string{"db_url": "a", "db.url": "b"},
			},
		},
	})
	assert.ErrorIs(t, err, render.ErrInvalidConfigKey)
	assert.Contains(t, err.Error(), "collides")
}

func TestRenderRejectsUnknownComponent(t *testing.T) {
	_, err := render.Render(render.Input{
		Hosts: []render.Host{{ID: "gw-1", Address: "10.0.0.11"}},
		Placements: []domain.ComponentPlacement{
			{Component: "vault", HostID: "gw-1", Ports: map[string]int{"http": 8200}},
		},
	})
	assert.ErrorIs(t, err, render.ErrUnknownComponent)
	assert.Contains(t, err.Error(), "gateway-front")
}

func TestRenderRejectsBadInput(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*render.Input)
		wantError error
	}{
		{
			"port out of range",
			func(in *render.Input) { in.Placements[0].Ports["http"] = 70000 },
			render.ErrInvalidPort,
		},
		{
			"missing listen port",
			func(in *render.Input) { delete(in.Placements[0].Ports, "http") },
			render.ErrMissingListen,
		},
		{
			"invalid config key",
			func(in *render.Input) { in.Placements[0].Config["bad key!"] = "x" },
			render.ErrInvalidConfigKey,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := render.Input{
				Hosts: []render.Host{{ID: "gw-1", Address: "10.0.0.11"}},
				Placements: []domain.ComponentPlacement{
					{Component: domain.ComponentGatewayFront, HostID: "gw-1", Ports: map[string]int{"http": 8080}, Config: map[string]string{}},
				},
			}
			tc.mutate(&in)
			_, err := render.Render(in)
			assert.ErrorIs(t, err, tc.wantError)
		})
	}
}

func TestStateComposeValidatesPort(t *testing.T) {
	_, err := render.StateCompose(render.StatePostgres{Image: "postgres:16-alpine", EnvFile: "/e", DataDir: "/d", Port: 0})
	assert.ErrorIs(t, err, render.ErrInvalidPort)
	_, err = render.StateCompose(render.StatePostgres{Port: 5432})
	assert.Error(t, err)
}

func TestHashIsSha256Prefixed(t *testing.T) {
	h := render.Hash("abc")
	assert.Equal(t, "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", h)
}

// TestPlacementExtraHostsPassthrough pins the compose-level host-alias
// channel placements gained for the bundle e2e (#107): entries render
// into the service's extra_hosts, sorted (the content hash is the
// render-diff anchor), and never into the inline environment.
func TestPlacementExtraHostsPassthrough(t *testing.T) {
	artifacts, err := render.Render(render.Input{
		Hosts: []render.Host{{ID: "local", Address: "127.0.0.1"}},
		Placements: []domain.ComponentPlacement{
			{
				Component:  domain.ComponentGatewayFront,
				HostID:     "local",
				Ports:      map[string]int{"http": 8080},
				ExtraHosts: []string{"fake.upstream:10.0.0.99", "host.docker.internal:host-gateway"},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	assert.Contains(t, artifacts[0].Compose, "- host.docker.internal:host-gateway")
	assert.Contains(t, artifacts[0].Compose, "- fake.upstream:10.0.0.99")
	// Sorted: the fake upstream mapping sorts before the alias.
	aliasAt := strings.Index(artifacts[0].Compose, "host.docker.internal:host-gateway")
	fakeAt := strings.Index(artifacts[0].Compose, "fake.upstream:10.0.0.99")
	assert.Greater(t, aliasAt, fakeAt, "extra_hosts must render sorted")
	assert.NotContains(t, artifacts[0].Compose, "EXTRA_HOSTS", "extra_hosts never becomes an env name")

	// Same set, different declared order: identical artifact (pure
	// function of the declared set).
	again, err := render.Render(render.Input{
		Hosts: []render.Host{{ID: "local", Address: "127.0.0.1"}},
		Placements: []domain.ComponentPlacement{
			{
				Component:  domain.ComponentGatewayFront,
				HostID:     "local",
				Ports:      map[string]int{"http": 8080},
				ExtraHosts: []string{"host.docker.internal:host-gateway", "fake.upstream:10.0.0.99"},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, artifacts[0].Compose, again[0].Compose)
}

// TestLoopbackTopologydGuideURL pins the loopback single-host
// derivation: the gateway container cannot reach the host's loopback,
// so the derived GATEWAY_TOPOLOGY_URL targets host.docker.internal
// (config validation requires the matching host-gateway alias on the
// gateway-front placement). Remote addresses keep the plain derivation.
func TestLoopbackTopologydGuideURL(t *testing.T) {
	placement := func() domain.ComponentPlacement {
		return domain.ComponentPlacement{
			Component: domain.ComponentTopologyd,
			HostID:    "local",
			Ports:     map[string]int{"http": 8181},
		}
	}
	gateway := func() domain.ComponentPlacement {
		return domain.ComponentPlacement{
			Component: domain.ComponentGatewayFront,
			HostID:    "local",
			Ports:     map[string]int{"http": 8080},
		}
	}

	renderOne := func(addr string) string {
		artifacts, err := render.Render(render.Input{
			Hosts:      []render.Host{{ID: "local", Address: addr}},
			Placements: []domain.ComponentPlacement{gateway(), placement()},
		})
		require.NoError(t, err)
		require.Len(t, artifacts, 1)
		return artifacts[0].Compose
	}

	assert.Contains(t, renderOne("127.0.0.1"),
		"GATEWAY_TOPOLOGY_URL: http://host.docker.internal:8181")
	assert.Contains(t, renderOne("10.0.0.11"),
		"GATEWAY_TOPOLOGY_URL: http://10.0.0.11:8181")
}

// TestIsLoopbackAddress pins the address classification both the
// renderer's derivation and config validation rely on.
func TestIsLoopbackAddress(t *testing.T) {
	assert.True(t, render.IsLoopbackAddress("127.0.0.1"))
	assert.True(t, render.IsLoopbackAddress("::1"))
	assert.False(t, render.IsLoopbackAddress("10.0.0.11"))
	assert.False(t, render.IsLoopbackAddress("host.docker.internal"))
	assert.False(t, render.IsLoopbackAddress(""))
}

func TestBundleRootMixesBuildContextIntoHash(t *testing.T) {
	in := render.Input{
		StateHostID: "h1",
		State:       &render.StatePostgres{Image: "postgres:16-alpine", EnvFile: "/e.env", DataDir: "/d", Port: 5432},
		Hosts:       []render.Host{{ID: "h1", Address: "10.0.0.1"}},
		Placements: []domain.ComponentPlacement{
			{Component: domain.ComponentGatewayFront, HostID: "h1", Ports: map[string]int{"http": 8080}, Config: map[string]string{"upstream": "http://10.0.0.2:4000"}},
		},
	}
	plain, err := render.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	root := t.TempDir()
	mk := func(dir, name, content string) {
		t.Helper()
		if mkErr := os.MkdirAll(filepath.Join(root, dir), 0o750); mkErr != nil {
			t.Fatal(mkErr)
		}
		if mkErr := os.WriteFile(filepath.Join(root, dir, name), []byte(content), 0o600); mkErr != nil {
			t.Fatal(mkErr)
		}
	}
	// Minimal stand-in build contexts for every phase-1 component.
	for _, d := range []string{"gateway", "identity", "topology"} {
		mk(d, "go.mod", "module x\n")
	}

	rooted, err := render.Render(render.Input{StateHostID: in.StateHostID, State: in.State, Hosts: in.Hosts, Placements: in.Placements, BundleRoot: root})
	if err != nil {
		t.Fatalf("Render with root: %v", err)
	}
	if rooted[0].Hash == plain[0].Hash {
		t.Fatalf("BundleRoot must change the convergence hash")
	}

	// A source-only change (compose text untouched) still moves the hash.
	mk("gateway", "main.go", "package main // changed\n")
	changed, err := render.Render(render.Input{StateHostID: in.StateHostID, State: in.State, Hosts: in.Hosts, Placements: in.Placements, BundleRoot: root})
	if err != nil {
		t.Fatalf("Render after source change: %v", err)
	}
	if changed[0].Hash == rooted[0].Hash {
		t.Fatalf("a build-context source change must change the convergence hash")
	}
}
