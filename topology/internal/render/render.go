// SPDX-License-Identifier: Apache-2.0

// Package render is the topology-l1 §6 Renderer port's phase-1 docker
// backend: it turns the declared topology snapshot into per-host
// compose files. One compose project ("looming") per placement host,
// one service per placed component plus the bundle's Postgres on the
// state host. Output is deterministic (sorted keys and lists) so the
// content hash that drives render-diff convergence is stable.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// Project is the compose project name every host converges under — one
// project per host, same name everywhere, so `looming-ctl` wraps docker
// commands with a single -p flag (topology-l1 §2 supervision shape).
const Project = "looming"

// ComponentBundlePostgres is the state-plane service on the state
// host: the bundle-owned Postgres (AD-36 decision 5). It is rendered
// from the config's state section, never from a placement.
const ComponentBundlePostgres = "bundle-postgres"

// postgresDataTarget is the in-container path the bundle Postgres
// data_dir bind-mounts onto.
const postgresDataTarget = "/var/lib/postgresql/data"

// Render errors. These surface as config errors at the apply boundary:
// they mean the declared topology asks for something phase-1 render
// cannot express.
var (
	ErrUnknownComponent = errors.New("render: unknown component (phase-1 allowlist)")
	ErrInvalidPort      = errors.New("render: port out of range 1..65535")
	ErrInvalidConfigKey = errors.New("render: invalid config key")
	ErrMissingListen    = errors.New("render: placement lacks the component's listen port")
)

// Contract is one phase-1 component's documented env contract: the env
// prefix its container variables carry, its bundle-root-relative build
// context, and which named placement port feeds its listen env. Values
// are the binaries' real env vocabulary (gateway's GATEWAY_*,
// identityd's IDENTITY_*).
type Contract struct {
	EnvPrefix  string
	BuildDir   string
	ListenPort string
	ListenEnv  string
}

// contracts is the phase-1 allowlist: placements naming any other
// component are rejected at validate time. The map is the single
// source for both the check and the render.
var contracts = map[string]Contract{
	domain.ComponentGatewayFront: {EnvPrefix: "GATEWAY", BuildDir: "gateway", ListenPort: "http", ListenEnv: "GATEWAY_LISTEN"},
	domain.ComponentIdentityd:    {EnvPrefix: "IDENTITY", BuildDir: "identity", ListenPort: "http", ListenEnv: "IDENTITY_LISTEN"},
}

// Lookup returns the phase-1 contract for a component, or false when
// the component is outside the allowlist.
func Lookup(component string) (Contract, bool) {
	c, ok := contracts[component]
	return c, ok
}

// Allowlist returns the phase-1 component names, sorted — the
// vocabulary config validation and error messages use.
func Allowlist() []string {
	names := make([]string, 0, len(contracts))
	for name := range contracts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Host is a declared host as the operator named it (the YAML id — also
// the render_artifacts key space).
type Host struct {
	ID      string
	Address string
	SSHUser string
}

// StatePostgres mirrors the config's state.postgres section: the
// bundle-owned Postgres the state plane ensures on the state host.
type StatePostgres struct {
	Image   string
	EnvFile string
	DataDir string
	Port    int
}

// Input is one render pass: the declared hosts and the topology's
// placement set (HostIDs are operator-facing YAML host ids, not uuids).
// State/StateHostID are nil/"" when the config carries no state
// section — then no bundle-postgres service is rendered.
type Input struct {
	StateHostID string
	State       *StatePostgres
	Hosts       []Host
	Placements  []domain.ComponentPlacement
}

// Artifact is one host's rendered compose file plus the content hash
// render-diff convergence compares.
type Artifact struct {
	HostID  string
	Compose string
	Hash    string
}

// configKeyPattern constrains placement config keys to what safely
// becomes an env name after uppercasing and -/. → _.
var configKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// envName maps a config key to its env name under the component's
// prefix: lowercased keys uppercased, separators normalized to _.
func envName(prefix, key string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_")
	return prefix + "_" + strings.ToUpper(replacer.Replace(key))
}

// postgresService builds the bundle-postgres service map from the
// state config.
func postgresService(sp StatePostgres) map[string]any {
	return map[string]any{
		"image":     sp.Image,
		"env_file":  []string{sp.EnvFile},
		"ports":     []string{fmt.Sprintf("%d:%d", sp.Port, sp.Port)},
		"volumes":   []string{fmt.Sprintf("%s:%s", sp.DataDir, postgresDataTarget)},
		"restart":   "unless-stopped",
	}
}

// placementService builds one placed component's service map from its
// contract. Errors name the placement so apply can report them with
// host context.
func placementService(p domain.ComponentPlacement, c Contract) (map[string]any, error) {
	svc := map[string]any{
		"build":   map[string]any{"context": c.BuildDir},
		"restart": "unless-stopped",
	}

	ports := make([]string, 0, len(p.Ports))
	for name, port := range p.Ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: %s.%s = %d", ErrInvalidPort, p.Component, name, port)
		}
		ports = append(ports, fmt.Sprintf("%d:%d", port, port))
	}
	sort.Strings(ports)
	if len(ports) > 0 {
		svc["ports"] = ports
	}

	env := map[string]string{}
	if c.ListenPort != "" {
		listen, ok := p.Ports[c.ListenPort]
		if !ok {
			return nil, fmt.Errorf("%w: %q needs port %q for %s", ErrMissingListen, p.Component, c.ListenPort, c.ListenEnv)
		}
		env[c.ListenEnv] = fmt.Sprintf(":%d", listen)
	}
	keys := make([]string, 0, len(p.Config))
	for key := range p.Config {
		if !configKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: %q on %q", ErrInvalidConfigKey, key, p.Component)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env[envName(c.EnvPrefix, key)] = p.Config[key]
	}
	if len(env) > 0 {
		svc["environment"] = env
	}

	return svc, nil
}

// composeDocument marshals the deterministic top-level compose shape:
// fixed project name plus the host's services.
func composeDocument(services map[string]any) (string, error) {
	doc := map[string]any{
		"name":     Project,
		"services": services,
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render: encode compose: %w", err)
	}
	return string(out), nil
}

// StateCompose renders the standalone compose document the state-plane
// ensure converges: the bundle-postgres service alone. Keeping it
// separate from the placement render lets first boot bring Postgres up
// before any placement exists.
func StateCompose(sp StatePostgres) (string, error) {
	if sp.Port < 1 || sp.Port > 65535 {
		return "", fmt.Errorf("%w: state.postgres.port = %d", ErrInvalidPort, sp.Port)
	}
	if sp.Image == "" || sp.EnvFile == "" || sp.DataDir == "" {
		return "", errors.New("render: state.postgres requires image, env_file, and data_dir")
	}
	return composeDocument(map[string]any{ComponentBundlePostgres: postgresService(sp)})
}

// Hash returns the content hash render-diff convergence stores and
// compares: "sha256:" plus the hex digest of the compose content.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Render produces one artifact per host that carries a placement or is
// the state host. Hosts are visited in id order and each host's
// placements in component order, so the output (and its hash) is a
// pure function of the input.
func Render(in Input) ([]Artifact, error) {
	byHost := map[string][]domain.ComponentPlacement{}
	for _, p := range in.Placements {
		byHost[p.HostID] = append(byHost[p.HostID], p)
	}

	hostIDs := make([]string, 0, len(in.Hosts))
	seen := map[string]bool{}
	for _, h := range in.Hosts {
		if !seen[h.ID] {
			seen[h.ID] = true
			hostIDs = append(hostIDs, h.ID)
		}
	}
	sort.Strings(hostIDs)

	artifacts := make([]Artifact, 0, len(hostIDs))
	for _, hostID := range hostIDs {
		services := map[string]any{}
		if in.State != nil && hostID == in.StateHostID {
			services[ComponentBundlePostgres] = postgresService(*in.State)
		}
		placements := byHost[hostID]
		sort.Slice(placements, func(i, j int) bool {
			if placements[i].Component != placements[j].Component {
				return placements[i].Component < placements[j].Component
			}
			return placements[i].HostID < placements[j].HostID
		})
		for _, p := range placements {
			c, ok := Lookup(p.Component)
			if !ok {
				return nil, fmt.Errorf("%w: %q (phase-1: %s)", ErrUnknownComponent, p.Component, strings.Join(Allowlist(), ", "))
			}
			svc, err := placementService(p, c)
			if err != nil {
				return nil, err
			}
			services[p.Component] = svc
		}
		if len(services) == 0 {
			continue
		}
		compose, err := composeDocument(services)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{HostID: hostID, Compose: compose, Hash: Hash(compose)})
	}
	return artifacts, nil
}
