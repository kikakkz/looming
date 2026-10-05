// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates the operator's
// /etc/looming/topology.yaml — the versioned desired-state file
// `looming-ctl apply` converges from (topology-l1 §3 admin bootstrap,
// AD-36 decision 1: single hand-editable config file, GitLab-omnibus
// shape). Validation carries file and line context so apply failures
// read as config errors, not stack traces.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/topology/internal/render"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// Version is the only schema version phase-1 apply accepts.
const Version = 1

// Config-level sentinels. Each Error wrapping them carries the file
// (and where cheap, the line) the failure came from.
var (
	ErrUnreadableFile       = errors.New("config: cannot read file")
	ErrInvalidVersion       = errors.New("config: version must be 1")
	ErrInvalidYAML          = errors.New("config: invalid yaml")
	ErrInvalidAccess        = errors.New("config: invalid access section")
	ErrInvalidState         = errors.New("config: invalid state section")
	ErrNoHosts              = errors.New("config: at least one host is required")
	ErrInvalidHost          = errors.New("config: invalid host entry")
	ErrDuplicateHost        = errors.New("config: duplicate host id or address")
	ErrUnknownPlacementHost = errors.New("config: placement references an unknown host")
	ErrUnknownComponent     = errors.New("config: unknown component (phase-1 allowlist)")
	ErrInvalidPlacement     = errors.New("config: invalid placement")
)

// Error is one config failure with file context: what failed, in which
// file, on which line when the failing entry could be located in the
// YAML tree (hosts and placements carry their entry's line; section
// errors carry the section key's line).
type Error struct {
	File string
	Line int // 0 when not tied to a specific node
	Err  error
	Msg  string
}

// Error formats "config: <file>[:<line>]: <msg>".
func (e *Error) Error() string {
	where := e.File
	if e.Line > 0 {
		where = fmt.Sprintf("%s:%d", where, e.Line)
	}
	return fmt.Sprintf("config: %s: %s", where, e.Msg)
}

// Unwrap exposes the sentinel for errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// Config is the validated desired-state file: the access section, the
// state plane (bundle Postgres on the first host), the declared hosts,
// and the component placements.
type Config struct {
	Version    int
	Access     Access
	State      *State
	Hosts      []Host
	Placements []Placement

	source string // file path, for error context
}

// Access is the guide-page visibility toggle plus the phase-1 ingress
// shape (topology-l1 §2). Endpoint is the operator's value — an IP,
// a hostname, or an http(s) URL — mapped onto the domain's ip|http
// kinds at validate time.
type Access struct {
	Mode      string
	Transport string
	Endpoint  string
}

// State is the state plane the first host converges at boot: the
// bundle-owned Postgres (AD-36 decision 5).
type State struct {
	Postgres StatePostgres
}

// StatePostgres is the bundle Postgres's container contract: image,
// the operator-prepared env_file carrying POSTGRES_PASSWORD (apply
// never generates or prints credentials — T1 decision 5), the host
// data directory, and the published port.
type StatePostgres struct {
	Image   string
	EnvFile string
	DataDir string
	Port    int
}

// Host is one declared machine. ID is the operator-chosen identity
// every reference (placements, artifacts, output) uses; Address is a
// plain phase-1 IP or hostname; SSHUser selects the executor channel
// (empty means local docker — the single-host degenerate case).
type Host struct {
	ID      string
	Address string
	SSHUser string
	Labels  []string
}

// Placement declares one component on one host: the named ports it
// claims, free-form config entries that render into the component's
// documented env prefix, and an optional operator-prepared env file —
// the phase-1 secret channel (values stay out of the rendered inline
// environment and out of the artifact's persisted content).
type Placement struct {
	Component string
	Host      string
	Ports     map[string]int
	Config    map[string]string
	EnvFile   string
}

// raw mirrors the YAML shape for strict decoding: unknown keys are
// rejected (a typo'd field is a config error, not a silent default).
type rawConfig struct {
	Version    int            `yaml:"version"`
	Access     rawAccess      `yaml:"access"`
	State      *rawState      `yaml:"state"`
	Hosts      []rawHost      `yaml:"hosts"`
	Placements []rawPlacement `yaml:"placements"`
}

type rawAccess struct {
	Mode      string `yaml:"mode"`
	Transport string `yaml:"transport"`
	Endpoint  string `yaml:"endpoint"`
}

type rawState struct {
	Postgres rawStatePostgres `yaml:"postgres"`
}

type rawStatePostgres struct {
	Image   string `yaml:"image"`
	EnvFile string `yaml:"env_file"`
	DataDir string `yaml:"data_dir"`
	Port    int    `yaml:"port"`
}

type rawHost struct {
	ID      string   `yaml:"id"`
	Address string   `yaml:"address"`
	SSHUser string   `yaml:"ssh_user"`
	Labels  []string `yaml:"labels"`
}

type rawPlacement struct {
	Component string            `yaml:"component"`
	Host      string            `yaml:"host"`
	Ports     map[string]int    `yaml:"ports"`
	Config    map[string]string `yaml:"config"`
	EnvFile   string            `yaml:"env_file"`
}

// portNamePattern and hostIDPattern constrain the vocabulary other
// planes consume: port names become env/docker references, host ids
// become artifact keys and CLI output.
var (
	portNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	hostIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

// Load reads, strictly decodes, and validates the config file at path.
// Any failure — unreadable file, YAML syntax, unknown key, or an
// invariant violation — is one config error with file context.
func Load(path string) (*Config, error) {
	//nolint:gosec // the path is the operator-supplied CLI argument; reading exactly what was named is the command's job.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{File: path, Err: ErrUnreadableFile, Msg: err.Error()}
	}

	// Line hints come from a node tree; the typed value comes from a
	// strict second pass over the same bytes (KnownFields rejects
	// unknown keys, which node.Decode does not).
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, &Error{File: path, Err: ErrInvalidYAML, Msg: err.Error()}
	}
	if len(node.Content) == 0 {
		return nil, &Error{File: path, Err: ErrInvalidYAML, Msg: "empty file"}
	}
	var raw rawConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, &Error{File: path, Err: ErrInvalidYAML, Msg: err.Error()}
	}

	cfg := &Config{
		Version: raw.Version,
		Access:  Access{Mode: raw.Access.Mode, Transport: raw.Access.Transport, Endpoint: raw.Access.Endpoint},
		source:  path,
	}
	if raw.State != nil {
		cfg.State = &State{Postgres: StatePostgres{
			Image:   raw.State.Postgres.Image,
			EnvFile: raw.State.Postgres.EnvFile,
			DataDir: raw.State.Postgres.DataDir,
			Port:    raw.State.Postgres.Port,
		}}
	}
	for _, h := range raw.Hosts {
		cfg.Hosts = append(cfg.Hosts, Host(h))
	}
	for _, p := range raw.Placements {
		cfg.Placements = append(cfg.Placements, Placement(p))
	}

	if err := cfg.validate(&node); err != nil {
		return nil, err
	}
	return cfg, nil
}

// sectionLine returns the line of key's value node in the document
// mapping (0 when absent) — section-level errors point at the section.
func sectionLine(doc *yaml.Node, key string) int {
	if doc == nil {
		return 0
	}
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return 0
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return 0
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i].Line
		}
	}
	return 0
}

// entryLines returns the line of each entry in key's sequence
// (hosts, placements) indexed by position — entry-level errors point
// at the offending entry.
func entryLines(doc *yaml.Node, key string) []int {
	if doc == nil {
		return nil
	}
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		seq := root.Content[i+1]
		if seq.Kind != yaml.SequenceNode {
			return nil
		}
		lines := make([]int, len(seq.Content))
		for j, item := range seq.Content {
			lines[j] = item.Line
		}
		return lines
	}
	return nil
}

// fail builds the config Error for this file.
func (c *Config) fail(line int, err error, format string, args ...any) *Error {
	return &Error{File: c.source, Line: line, Err: err, Msg: fmt.Sprintf(format, args...)}
}

// endpointKind maps the operator's endpoint value onto the domain's
// phase-1 kinds: an IP or hostname is EndpointIP, an http(s) URL is
// EndpointHTTP (topology-l1 §3 renders http://<static-ip>:<port>).
func endpointKind(value string) (domain.Endpoint, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("endpoint is required")
	}
	if net.ParseIP(trimmed) != nil {
		return domain.EndpointIP, nil
	}
	if u, err := url.Parse(trimmed); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return domain.EndpointHTTP, nil
	}
	if hostIDPattern.MatchString(trimmed) && !strings.Contains(trimmed, "_") {
		return domain.EndpointIP, nil
	}
	return "", fmt.Errorf("endpoint %q must be an IP, a hostname, or an http(s) URL", value)
}

// DomainAccess validates the access triple through the domain
// vocabulary (reserved phase-2 transports fail with their owning
// issue named) and returns the aggregate value.
func (c *Config) DomainAccess() (domain.Access, error) {
	kind, err := endpointKind(c.Access.Endpoint)
	if err != nil {
		return domain.Access{}, err
	}
	return domain.NewAccess(c.Access.Mode, c.Access.Transport, string(kind))
}

// validate checks the config-level invariants the domain cannot see:
// version, hosts (present, well-formed, unique), placements
// (reference declared hosts, phase-1 components, well-formed ports and
// config keys), and the state section's container contract.
func (c *Config) validate(doc *yaml.Node) error {
	if c.Version != Version {
		return c.fail(sectionLine(doc, "version"), ErrInvalidVersion,
			"version must be %d (got %d)", Version, c.Version)
	}

	if err := c.validateAccess(doc); err != nil {
		return err
	}
	if err := c.validateHosts(doc); err != nil {
		return err
	}
	if err := c.validatePlacements(doc); err != nil {
		return err
	}
	if err := c.validateState(doc); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateAccess(doc *yaml.Node) error {
	if _, err := c.DomainAccess(); err != nil {
		return c.fail(sectionLine(doc, "access"), ErrInvalidAccess, "access: %v", err)
	}
	return nil
}

func (c *Config) validateHosts(doc *yaml.Node) error {
	if len(c.Hosts) == 0 {
		return c.fail(sectionLine(doc, "hosts"), ErrNoHosts, "at least one host is required")
	}
	lines := entryLines(doc, "hosts")
	ids := map[string]int{}
	addresses := map[string]int{}
	for i, h := range c.Hosts {
		line := 0
		if i < len(lines) {
			line = lines[i]
		}
		if !hostIDPattern.MatchString(h.ID) {
			return c.fail(line, ErrInvalidHost, "host id %q is empty or carries unsafe characters", h.ID)
		}
		if strings.TrimSpace(h.Address) == "" {
			return c.fail(line, ErrInvalidHost, "host %q has no address", h.ID)
		}
		if prev, dup := ids[h.ID]; dup {
			return c.fail(line, ErrDuplicateHost, "host id %q repeats entry at line %d", h.ID, lines[prev])
		}
		if prev, dup := addresses[h.Address]; dup {
			return c.fail(line, ErrDuplicateHost, "host address %q repeats entry at line %d", h.Address, lines[prev])
		}
		ids[h.ID] = i
		addresses[h.Address] = i
	}
	return nil
}

func (c *Config) validatePlacements(doc *yaml.Node) error {
	lines := entryLines(doc, "placements")
	declared := make(map[string]bool, len(c.Hosts))
	for _, h := range c.Hosts {
		declared[h.ID] = true
	}
	seen := map[string]int{}
	hostPorts := map[string]map[int]int{} // host -> port -> placement index (-1 = state plane)
	if c.State != nil && len(c.Hosts) > 0 {
		hostPorts[c.Hosts[0].ID] = map[int]int{c.State.Postgres.Port: -1}
	}
	for i, p := range c.Placements {
		line := 0
		if i < len(lines) {
			line = lines[i]
		}
		if !declared[p.Host] {
			return c.fail(line, ErrUnknownPlacementHost,
				"placement of %q references unknown host %q", p.Component, p.Host)
		}
		contract, ok := render.Lookup(p.Component)
		if !ok {
			return c.fail(line, ErrUnknownComponent,
				"unknown component %q (phase-1 allowlist: %s)", p.Component, strings.Join(render.Allowlist(), ", "))
		}
		pair := p.Component + "\x00" + p.Host
		if prev, dup := seen[pair]; dup {
			return c.fail(line, ErrInvalidPlacement, "%q is placed twice on host %q (entry at line %d)",
				p.Component, p.Host, lines[prev])
		}
		seen[pair] = i
		if msg := placementPortChecks(i, p, contract, hostPorts, lines); msg != "" {
			return c.fail(line, ErrInvalidPlacement, "%s", msg)
		}
		if msg := placementConfigChecks(p, contract); msg != "" {
			return c.fail(line, ErrInvalidPlacement, "%s", msg)
		}
	}
	return nil
}

// placementPortChecks validates one placement's port set against the
// per-host published-port registry: names, ranges, and uniqueness
// against both other placements and the state plane's port. It returns
// "" when valid, else the operator-facing message.
func placementPortChecks(index int, p Placement, contract render.Contract, hostPorts map[string]map[int]int, lines []int) string {
	if contract.ListenPort != "" {
		if _, ok := p.Ports[contract.ListenPort]; !ok {
			return fmt.Sprintf("%q needs its %q port (%s wiring)", p.Component, contract.ListenPort, contract.ListenEnv)
		}
	}
	for name, port := range p.Ports {
		if !portNamePattern.MatchString(name) {
			return fmt.Sprintf("port name %q on %q is not [a-z0-9_]", name, p.Component)
		}
		if port < 1 || port > 65535 {
			return fmt.Sprintf("port %q on %q is %d, outside 1..65535", name, p.Component, port)
		}
		if hostPorts[p.Host] == nil {
			hostPorts[p.Host] = map[int]int{}
		}
		if prev, taken := hostPorts[p.Host][port]; taken {
			owner := "state.postgres"
			if prev >= 0 {
				owner = fmt.Sprintf("placement at line %d", lines[prev])
			}
			return fmt.Sprintf("port %d on host %q is already published by %s", port, p.Host, owner)
		}
		hostPorts[p.Host][port] = index
	}
	return ""
}

// placementConfigChecks validates the env wiring details: the optional
// env_file path shape and config keys' env-name uniqueness after
// folding. It returns "" when valid, else the operator-facing message.
func placementConfigChecks(p Placement, contract render.Contract) string {
	if p.EnvFile != "" && !filepath.IsAbs(p.EnvFile) {
		return fmt.Sprintf("env_file %q on %q must be an absolute path", p.EnvFile, p.Component)
	}
	envNames := map[string]string{contract.ListenEnv: "(listen port)"}
	for key := range p.Config {
		if !renderConfigKey(key) {
			return fmt.Sprintf("config key %q on %q cannot become an env name", key, p.Component)
		}
		name := render.EnvName(contract.EnvPrefix, key)
		if prev, dup := envNames[name]; dup {
			return fmt.Sprintf("config key %q on %q collides with %s after env-name folding", key, p.Component, prev)
		}
		envNames[name] = "config key " + strconv.Quote(key)
	}
	return ""
}

// renderConfigKey accepts exactly the keys render's env pass-through
// accepts — config fails here with line context instead of at render.
func renderConfigKey(key string) bool {
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return len(key) > 0
}

func (c *Config) validateState(doc *yaml.Node) error {
	if c.State == nil {
		return nil
	}
	line := sectionLine(doc, "state")
	sp := c.State.Postgres
	switch {
	case sp.Image == "":
		return c.fail(line, ErrInvalidState, "state.postgres.image is required")
	case sp.EnvFile == "":
		return c.fail(line, ErrInvalidState, "state.postgres.env_file is required (POSTGRES_PASSWORD lives there; apply never generates credentials)")
	case !filepath.IsAbs(sp.EnvFile):
		return c.fail(line, ErrInvalidState, "state.postgres.env_file %q must be an absolute path", sp.EnvFile)
	case sp.DataDir == "":
		return c.fail(line, ErrInvalidState, "state.postgres.data_dir is required")
	case !filepath.IsAbs(sp.DataDir):
		return c.fail(line, ErrInvalidState, "state.postgres.data_dir %q must be an absolute path", sp.DataDir)
	case sp.Port < 1 || sp.Port > 65535:
		return c.fail(line, ErrInvalidState, "state.postgres.port is %d, outside 1..65535", sp.Port)
	}
	return nil
}

// RenderHosts maps the declared hosts onto the renderer's view.
func (c *Config) RenderHosts() []render.Host {
	hosts := make([]render.Host, len(c.Hosts))
	for i, h := range c.Hosts {
		hosts[i] = render.Host{ID: h.ID, Address: h.Address, SSHUser: h.SSHUser}
	}
	return hosts
}

// RenderEnvFiles maps placement env files onto the renderer's key
// space ("host\x00component"): the phase-1 secret channel.
func (c *Config) RenderEnvFiles() map[string]string {
	out := map[string]string{}
	for _, p := range c.Placements {
		if p.EnvFile != "" {
			out[p.Host+"\x00"+p.Component] = p.EnvFile
		}
	}
	return out
}

// RenderState maps the state section onto the renderer's view, nil
// when the config carries no state plane.
func (c *Config) RenderState() *render.StatePostgres {
	if c.State == nil {
		return nil
	}
	sp := c.State.Postgres
	return &render.StatePostgres{Image: sp.Image, EnvFile: sp.EnvFile, DataDir: sp.DataDir, Port: sp.Port}
}

// StateHostID names the host the state plane renders on: phase-1 apply
// runs on the first host, so that is where the state section converges.
// Returns "" when the config carries no state plane.
func (c *Config) StateHostID() string {
	if c.State == nil || len(c.Hosts) == 0 {
		return ""
	}
	return c.Hosts[0].ID
}
