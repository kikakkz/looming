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
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// Project is the compose project name every host converges under — one
// project per host, same name everywhere, so `looming` wraps docker
// commands with a single -p flag (topology-l1 §2 supervision shape).
const Project = "looming"

// ComponentBundlePostgres is the state-plane service on the state
// host: the bundle-owned Postgres (AD-36 decision 5). It is rendered
// from the config's state section, never from a placement.
const ComponentBundlePostgres = "bundle-postgres"

// postgresDataTarget is the in-container path the bundle Postgres
// data_dir bind-mounts onto.
const postgresDataTarget = "/var/lib/postgresql/data"

// HostGatewayAlias is the container-side alias for the docker host:
// the loopback single-host deployment derives container-audience URLs
// (the gateway's guide link) against it, and placements make it
// resolvable through the host-gateway mapping entry. Config validation
// and the renderer share these constants so the derivation rule and its
// wiring requirement cannot drift apart.
const HostGatewayAlias = "host.docker.internal"

// HostGatewayMapping is the extra_hosts entry that resolves
// HostGatewayAlias to the docker host from inside a container.
const HostGatewayMapping = HostGatewayAlias + ":host-gateway"

// IsLoopbackAddress reports whether addr names this very machine's
// loopback (the phase-1 single-host degenerate case): the single-host
// deployment is where containers need HostGatewayAlias to reach
// host-published ports. Anything else — hostnames, remote IPs — is a
// real network address containers (and other hosts) can already use.
func IsLoopbackAddress(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

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
// context (and dockerfile when the context is not the component's own
// directory), the directories whose contents feed the convergence
// digest, which named placement port feeds its listen env, and an
// optional in-image entrypoint. Values are the binaries' real env
// vocabulary (gateway's GATEWAY_*, identityd's IDENTITY_*,
// topologyd's TOPOLOGY_*).
type Contract struct {
	EnvPrefix string
	// BuildDir is the compose build context, bundle-root-relative.
	BuildDir string
	// Dockerfile is the compose build dockerfile, bundle-root-relative
	// (set when BuildDir is a wider context than the component's own
	// directory — topologyd's cross-module build).
	Dockerfile string
	// DigestDirs scopes the convergence-digest walk: the directories
	// whose file contents the image build actually reads. Empty
	// defaults to [BuildDir].
	DigestDirs []string
	ListenPort string
	ListenEnv  string
	Entrypoint string
}

// contracts is the phase-1 allowlist: placements naming any other
// component are rejected at validate time. The map is the single
// source for both the check and the render.
var contracts = map[string]Contract{
	domain.ComponentGatewayFront: {EnvPrefix: "GATEWAY", BuildDir: "gateway", ListenPort: "http", ListenEnv: "GATEWAY_LISTEN"},
	domain.ComponentIdentityd:    {EnvPrefix: "IDENTITY", BuildDir: "identity", ListenPort: "http", ListenEnv: "IDENTITY_LISTEN"},
	// topologyd serves the join/rejoin API. CLI-1 (#108) moved its
	// module's dependencies into platform/go, so its image builds from
	// the bundle root (context "." + explicit dockerfile) and the
	// convergence digest covers both directories the build reads.
	// TOPOLOGY_DATABASE_URL rides the placement's config keys or
	// env_file like any other secret material.
	domain.ComponentTopologyd: {
		EnvPrefix: "TOPOLOGY", BuildDir: ".", Dockerfile: "topology/Dockerfile",
		DigestDirs: []string{"platform/go", "topology"},
		ListenPort: "http", ListenEnv: "TOPOLOGY_LISTEN", Entrypoint: "/usr/local/bin/topologyd",
	},
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
// EnvFiles carries operator-prepared per-placement env files — the
// phase-1 secret channel, keyed "host\x00component"; secret values
// ride in those files, never in the rendered inline environment.
type Input struct {
	StateHostID string
	State       *StatePostgres
	Hosts       []Host
	Placements  []domain.ComponentPlacement
	EnvFiles    map[string]string
	// BundleRoot anchors build-context digesting: the convergence
	// hash mixes every phase-1 component's build directory in, so a
	// source change with an unchanged compose text (build: context
	// pins only the directory) still trips render-diff and reconverges.
	// Empty disables digesting (unit tests stay pure functions of
	// the input).
	BundleRoot string
}

// Artifact is one host's rendered compose file plus the content hash
// render-diff convergence compares. Empty marks a declared host with
// nothing to run (no placements, not the state host): the pipeline
// converges it by removing the host's compose project entirely, so a
// host that loses its last placement does not keep orphaned
// containers.
type Artifact struct {
	HostID  string
	Compose string
	Hash    string
	Empty   bool
}

// configKeyPattern constrains placement config keys to what safely
// becomes an env name after uppercasing and -/. → _.
var configKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// EnvName maps a config key to its env name under the component's
// prefix: separators normalized to _ and uppercased, so "db-url",
// "db.url", and "db_url" collide by construction — callers validate
// the collision away (render errors at render time, config with line
// context).
func EnvName(prefix, key string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_")
	return prefix + "_" + strings.ToUpper(replacer.Replace(key))
}

// postgresService builds the bundle-postgres service map from the
// state config. The host publishes sp.Port; the container side is
// Postgres's fixed 5432.
func postgresService(sp StatePostgres) map[string]any {
	return map[string]any{
		"image":    sp.Image,
		"env_file": []string{sp.EnvFile},
		"ports":    []string{fmt.Sprintf("%d:5432", sp.Port)},
		"volumes":  []string{fmt.Sprintf("%s:%s", sp.DataDir, postgresDataTarget)},
		"restart":  "unless-stopped",
	}
}

// buildMap is the compose `build` block: the contract's context plus
// GOPROXY passthrough, and the explicit dockerfile when the context is
// a wider directory than the component's own (topologyd's cross-module
// build; empty means the context's default Dockerfile).
func buildMap(c Contract) map[string]any {
	m := map[string]any{"context": c.BuildDir, "args": []string{"GOPROXY"}}
	if c.Dockerfile != "" {
		m["dockerfile"] = c.Dockerfile
	}
	return m
}

// placementService builds one placed component's service map from its
// contract. Errors name the placement so apply can report them with
// host context. envFile, when non-empty, is the operator-prepared
// env_file the service loads — the phase-1 secret channel (values stay
// out of the rendered artifact's inline environment). guideURL, when
// non-empty, is the topologyd base URL injected as the gateway's
// GATEWAY_TOPOLOGY_URL (T3): the renderer's derivation of the internal
// link, never an operator config key.
func placementService(p domain.ComponentPlacement, c Contract, envFile, guideURL string) (map[string]any, error) {
	svc := map[string]any{
		// GOPROXY rides the build-args passthrough so a mirrored or
		// offline build environment overrides the module proxy without
		// editing the Dockerfile: compose takes a name-only arg from
		// the invoking environment (the CLI shells out with the
		// operator's env, and CI uses the Go default). First real
		// container build this suite runs — pinned here so the hazard
		// stays visible (bundle e2e, #107).
		"build":   buildMap(c),
		"restart": "unless-stopped",
	}
	if c.Entrypoint != "" {
		svc["entrypoint"] = []string{c.Entrypoint}
	}
	if envFile != "" {
		svc["env_file"] = []string{envFile}
	}
	if len(p.ExtraHosts) > 0 {
		// Sorted copy: the compose hash is the render-diff anchor, so
		// the artifact must be a pure function of the declared set.
		svc["extra_hosts"] = sortedStrings(p.ExtraHosts)
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

	env, err := placementEnv(p, c, guideURL)
	if err != nil {
		return nil, err
	}
	if len(env) > 0 {
		svc["environment"] = env
	}

	return svc, nil
}

// placementEnv builds one placement's inline environment: the listen
// var, the renderer-derived guide URL for the gateway front, and the
// operator config keys folded to env names (duplicates rejected).
func placementEnv(p domain.ComponentPlacement, c Contract, guideURL string) (map[string]string, error) {
	env := map[string]string{}
	if c.ListenPort != "" {
		listen, ok := p.Ports[c.ListenPort]
		if !ok {
			return nil, fmt.Errorf("%w: %q needs port %q for %s", ErrMissingListen, p.Component, c.ListenPort, c.ListenEnv)
		}
		env[c.ListenEnv] = fmt.Sprintf(":%d", listen)
	}
	if guideURL != "" && p.Component == domain.ComponentGatewayFront {
		env["GATEWAY_TOPOLOGY_URL"] = guideURL
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
		name := EnvName(c.EnvPrefix, key)
		if _, dup := env[name]; dup {
			return nil, fmt.Errorf("%w: %q on %q maps to %s, and collides with an earlier entry", ErrInvalidConfigKey, key, p.Component, name)
		}
		env[name] = p.Config[key]
	}
	return env, nil
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

// topologydURL derives the gateway's guide source from the declared
// topologyd placement: host address + http port, the phase-1
// http://<address>:<port> shape (T3 — the gateway-front template gains
// GATEWAY_TOPOLOGY_URL from this, never from operator config). Every
// gap (no placement, no http port, unknown host) degrades to "": the
// gateway then serves its 404 "not configured" stub.
//
// Loopback special case: when topologyd sits on this very host
// (address 127.0.0.1), the URL is consumed from INSIDE the gateway
// container, where loopback is the container itself — the derivation
// therefore targets the docker host via HostGatewayAlias instead (the
// published port is the host's). Config validation requires the
// gateway-front placement to carry the matching host-gateway
// extra_hosts entry whenever this rule fires, so the alias resolves.
func topologydURL(in Input) string {
	contract, ok := Lookup(domain.ComponentTopologyd)
	if !ok || contract.ListenPort == "" {
		return ""
	}
	for _, p := range in.Placements {
		if p.Component != domain.ComponentTopologyd {
			continue
		}
		port, ok := p.Ports[contract.ListenPort]
		if !ok {
			return ""
		}
		for _, h := range in.Hosts {
			if h.ID == p.HostID {
				host := h.Address
				if IsLoopbackAddress(host) {
					host = HostGatewayAlias
				}
				return fmt.Sprintf("http://%s:%d", host, port)
			}
		}
		return ""
	}
	return ""
}

// sortedStrings returns a sorted copy of in — the deterministic output
// the content hash needs.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
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

// buildContextDigest fingerprints every phase-1 component's digest
// scope (its build directory by default; the contract's DigestDirs
// when the image reads a wider set — topologyd's cross-module build
// covers platform/go too): sorted (path, file-sha256) pairs
// combined into one digest. Dockerfiles reference the directories,
// not their contents, so a source-only change leaves the rendered
// compose text untouched — without this digest the apply pipeline
// would mark the host unchanged and the stale image would keep
// running (CodeRabbit review on PR #140). Deterministic: directory
// walk order is sorted; symlinks and dotfiles are skipped.
func buildContextDigest(root string) (string, error) {
	h := sha256.New()
	for _, component := range Allowlist() {
		c, ok := Lookup(component)
		if !ok || c.BuildDir == "" {
			continue
		}
		dirs := c.DigestDirs
		if len(dirs) == 0 {
			dirs = []string{c.BuildDir}
		}
		for _, dir := range dirs {
			entries, err := digestDir(root, filepath.Join(root, dir))
			if err != nil {
				return "", fmt.Errorf("render: digest build context %q: %w", dir, err)
			}
			for _, e := range entries {
				if _, err := fmt.Fprintf(h, "%s:%x\n", e.path, e.sum); err != nil {
					return "", fmt.Errorf("render: digest write: %w", err)
				}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type digestEntry struct {
	path string
	sum  [32]byte
}

// digestDir walks one build directory into sorted (path, sha256)
// entries. Paths come from walking the operator's own bundle root,
// not request input. Dotfiles are skipped EXCEPT .dockerignore: its
// contents decide which files Docker actually sends to the build, so
// a .dockerignore edit must move the convergence digest (CodeRabbit
// review on PR #140). Non-regular entries (symlinks, sockets) are
// skipped — ReadFile would follow a symlink out of the context and
// Docker ships symlinks as links, not content.
func digestDir(root, dir string) ([]digestEntry, error) {
	var entries []digestEntry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if name != ".dockerignore" && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "coverage")) {
			return nil
		}
		// #nosec G304 -- path is inside the operator-supplied bundle root.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, digestEntry{path: rel, sum: sha256.Sum256(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
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

	// Source-only changes must reconverge even when the rendered
	// compose text is byte-identical: mix the build contexts into
	// every artifact's convergence hash. An empty root disables
	// digesting (unit tests render as pure functions).
	convergenceHash := Hash
	if in.BundleRoot != "" {
		digest, err := buildContextDigest(in.BundleRoot)
		if err != nil {
			return nil, err
		}
		convergenceHash = func(content string) string {
			return Hash(content + "\x00build-context:" + digest)
		}
	}

	guideURL := topologydURL(in)

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
			svc, err := placementService(p, c, in.EnvFiles[p.HostID+"\x00"+p.Component], guideURL)
			if err != nil {
				return nil, err
			}
			services[p.Component] = svc
		}
		empty := len(services) == 0
		if empty {
			// A declared-but-idle host still gets an artifact: the
			// compose document is the converge unit, and its hash is
			// what render-diff compares before tearing the project
			// down.
			services = map[string]any{}
		}
		compose, err := composeDocument(services)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{HostID: hostID, Compose: compose, Hash: convergenceHash(compose), Empty: empty})
	}
	return artifacts, nil
}
