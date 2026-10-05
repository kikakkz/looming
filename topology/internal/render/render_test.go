// SPDX-License-Identifier: Apache-2.0

package render_test

import (
	"flag"
	"os"
	"path/filepath"
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
		},
	})
	assert.Equal(t, "gw-1", arts[0].HostID)
	assert.Equal(t, render.Hash(arts[0].Compose), arts[0].Hash)
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
