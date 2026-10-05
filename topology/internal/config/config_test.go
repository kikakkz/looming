// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/config"
)

// validYAML is the brief's example topology: two hosts, state plane,
// gateway-front + identityd placements.
const validYAML = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "/etc/looming/postgres.env", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts:
  - id: gw-1
    address: 10.0.0.11
    ssh_user: root
    labels: [gateway]
  - id: app-1
    address: 10.0.0.12
    ssh_user: root
    labels: [identity]
placements:
  - {component: gateway-front, host: gw-1, ports: {http: 8080}, config: {upstream: "http://10.0.0.13:4000"}}
  - {component: identityd, host: app-1, ports: {http: 8081}, config: {database_url: "postgres://postgres:pw@10.0.0.11:5432/identity"}}
`

// writeCfg loads YAML from a temp file, so error messages carry a
// realistic path.
func writeCfg(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func load(t *testing.T, content string) (*config.Config, error) {
	t.Helper()
	return config.Load(writeCfg(t, content))
}

func TestLoadValidRoundTripsEverySection(t *testing.T) {
	cfg, err := load(t, validYAML)
	require.NoError(t, err)

	assert.Equal(t, 1, cfg.Version)
	assert.Equal(t, config.Access{Mode: "public", Transport: "direct", Endpoint: "10.0.0.10"}, cfg.Access)
	require.NotNil(t, cfg.State)
	assert.Equal(t, config.StatePostgres{
		Image:   "postgres:16-alpine",
		EnvFile: "/etc/looming/postgres.env",
		DataDir: "/var/lib/looming/postgres",
		Port:    5432,
	}, cfg.State.Postgres)

	require.Len(t, cfg.Hosts, 2)
	assert.Equal(t, config.Host{ID: "gw-1", Address: "10.0.0.11", SSHUser: "root", Labels: []string{"gateway"}}, cfg.Hosts[0])

	require.Len(t, cfg.Placements, 2)
	assert.Equal(t, "gateway-front", cfg.Placements[0].Component)
	assert.Equal(t, "gw-1", cfg.Placements[0].Host)
	assert.Equal(t, map[string]int{"http": 8080}, cfg.Placements[0].Ports)
	assert.Equal(t, map[string]string{"upstream": "http://10.0.0.13:4000"}, cfg.Placements[0].Config)

	// Access maps onto the domain triple; a plain IP is EndpointIP.
	access, err := cfg.DomainAccess()
	require.NoError(t, err)
	assert.Equal(t, "public", string(access.Mode))
	assert.Equal(t, "direct", string(access.Transport))
	assert.Equal(t, "ip", string(access.Endpoint))
}

func TestEndpointKindMapping(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{"10.0.0.10", "ip"},
		{"gw.local", "ip"},
		{"http://10.0.0.10:8080", "http"},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			cfg, err := load(t, `
version: 1
access: {mode: private, transport: direct, endpoint: "`+tc.endpoint+`"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`)
			require.NoError(t, err)
			access, err := cfg.DomainAccess()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(access.Endpoint))
		})
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr error
		// fragment the message must contain
		contains string
		// line the error must name (>0 asserts a line hint exists)
		line int
	}{
		{
			name: "missing version",
			yaml: `
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidVersion, contains: "version", line: 0,
		},
		{
			name:     "unsupported version",
			yaml:     versioned(2),
			wantErr:  config.ErrInvalidVersion,
			contains: "version must be 1",
		},
		{
			name: "bad access mode",
			yaml: `
version: 1
access: {mode: half-public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidAccess, contains: "half-public",
		},
		{
			name: "phase-2 transport names its issue",
			yaml: `
version: 1
access: {mode: public, transport: overlay, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidAccess, contains: "#112",
		},
		{
			name: "garbage endpoint",
			yaml: `
version: 1
access: {mode: public, transport: direct, endpoint: "not an endpoint:::"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidAccess, contains: "endpoint",
		},
		{
			name:     "no hosts",
			yaml:     withHosts("hosts: []"),
			wantErr:  config.ErrNoHosts,
			contains: "at least one host",
		},
		{
			name: "duplicate host id",
			yaml: withHosts(`
hosts:
  - {id: dup, address: 10.0.0.1}
  - {id: dup, address: 10.0.0.2}
`),
			wantErr: config.ErrDuplicateHost, contains: `host id "dup"`, line: 7,
		},
		{
			name: "duplicate host address",
			yaml: withHosts(`
hosts:
  - {id: a, address: 10.0.0.1}
  - {id: b, address: 10.0.0.1}
`),
			wantErr: config.ErrDuplicateHost, contains: `host address "10.0.0.1"`, line: 7,
		},
		{
			name: "host without address",
			yaml: withHosts(`
hosts:
  - {id: a}
`),
			wantErr: config.ErrInvalidHost, contains: "no address", line: 6,
		},
		{
			name:     "unknown placement host",
			yaml:     placementsHost("ghost"),
			wantErr:  config.ErrUnknownPlacementHost,
			contains: `unknown host "ghost"`, line: 6,
		},
		{
			name:     "unknown component hits the allowlist",
			yaml:     placementsComponent("vault"),
			wantErr:  config.ErrUnknownComponent,
			contains: "gateway-front",
		},
		{
			name: "duplicate placement pair",
			yaml: `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements:
  - {component: identityd, host: only, ports: {http: 8081}}
  - {component: identityd, host: only, ports: {http: 8082}}
`,
			wantErr: config.ErrInvalidPlacement, contains: "twice on host", line: 7,
		},
		{
			name:     "gateway-front needs its http port",
			yaml:     placementsPorts("{grpc: 8080}"),
			wantErr:  config.ErrInvalidPlacement,
			contains: `needs its "http" port`,
		},
		{
			name:     "port out of range",
			yaml:     placementsPorts("{http: 70000}"),
			wantErr:  config.ErrInvalidPlacement,
			contains: "outside 1..65535",
		},
		{
			name:     "bad port name",
			yaml:     placementsPorts("{http: 8080, 'bad name': 1}"),
			wantErr:  config.ErrInvalidPlacement,
			contains: "port name",
		},
		{
			name: "bad config key",
			yaml: `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}, config: {"bad key": "x"}}
`,
			wantErr: config.ErrInvalidPlacement, contains: "config key", line: 6,
		},
		{
			name: "state postgres bad port",
			yaml: `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "/etc/looming/postgres.env", data_dir: "/var/lib/looming/postgres", port: 0}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidState, contains: "port",
		},
		{
			name: "state postgres missing env_file",
			yaml: `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`,
			wantErr: config.ErrInvalidState, contains: "env_file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.yaml)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)
			var cfgErr *config.Error
			require.True(t, errors.As(err, &cfgErr), "error must carry file context: %v", err)
			assert.Contains(t, cfgErr.File, "topology.yaml")
			assert.Contains(t, err.Error(), tc.contains)
			if tc.line > 0 {
				assert.Equal(t, tc.line, cfgErr.Line, "line hint must point at the offending entry")
			}
		})
	}
}

// versioned builds a config with the given version number.
func versioned(v int) string {
	return `
version: ` + itoa(v) + `
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`
}

// withHosts splices a hosts section into a minimal config.
func withHosts(hosts string) string {
	return `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
` + hosts + `
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`
}

func placementsHost(host string) string {
	return `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements:
  - {component: gateway-front, host: ` + host + `, ports: {http: 8080}}
`
}

func placementsComponent(component string) string {
	return `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements:
  - {component: ` + component + `, host: only, ports: {http: 8080}}
`
}

func placementsPorts(ports string) string {
	return `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements:
  - {component: gateway-front, host: only, ports: ` + ports + `}
`
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}

func TestLoadUnreadableFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.ErrorIs(t, err, config.ErrUnreadableFile)
}

func TestLoadInvalidYAML(t *testing.T) {
	_, err := load(t, "version: [1,\n")
	assert.ErrorIs(t, err, config.ErrInvalidYAML)
}

func TestLoadEmptyFile(t *testing.T) {
	_, err := load(t, "")
	assert.ErrorIs(t, err, config.ErrInvalidYAML)
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	_, err := load(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hostz: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`)
	assert.ErrorIs(t, err, config.ErrInvalidYAML)
	assert.Contains(t, err.Error(), "hostz")
}

func TestRenderViews(t *testing.T) {
	cfg, err := load(t, validYAML)
	require.NoError(t, err)

	hosts := cfg.RenderHosts()
	require.Len(t, hosts, 2)
	assert.Equal(t, "gw-1", hosts[0].ID)
	assert.Equal(t, "root", hosts[0].SSHUser)

	state := cfg.RenderState()
	require.NotNil(t, state)
	assert.Equal(t, 5432, state.Port)
	assert.Equal(t, "gw-1", cfg.StateHostID())

	// No state section: no state render, no state host.
	plain := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.1}]
placements: [{component: gateway-front, host: only, ports: {http: 8080}}]
`
	cfg, err = load(t, plain)
	require.NoError(t, err)
	assert.Nil(t, cfg.RenderState())
	assert.Equal(t, "", cfg.StateHostID())
}
