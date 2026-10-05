// SPDX-License-Identifier: Apache-2.0

// Package apply is the T1 converge pipeline (topology-l1 §3 admin
// bootstrap journey): load and validate the operator's topology.yaml,
// bring up the state plane on first boot, persist the desired state
// through the T0 Declare flow, render per-host compose files, and
// converge each host's docker project — restarting only what changed
// (render-diff: an unchanged content hash means no docker call at
// all). Every step is a testable unit behind a seam in Deps.
package apply

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the store connections

	"github.com/kikakkz/looming/topology/internal/config"
	"github.com/kikakkz/looming/topology/internal/exec"
	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	hostport "github.com/kikakkz/looming/topology/internal/host/port"
	"github.com/kikakkz/looming/topology/internal/render"
	topologyadapter "github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/internal/topology/app"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
	topologyport "github.com/kikakkz/looming/topology/internal/topology/port"
	"github.com/kikakkz/looming/topology/migrations"
)

// DatabaseName is the bundle Postgres database the topology component
// owns — database-per-component per AD-36 decision 5, created by the
// state plane at first boot.
const DatabaseName = "topology"

// Stores bundles the persistence seams one apply run needs. Opened
// against the topology database after the state plane is ensured.
type Stores struct {
	Topology  topologyport.Store
	Registry  hostport.Registry
	Artifacts topologyport.ArtifactStore
}

// Deps is the pipeline's seam surface — everything that touches the
// outside world, so unit tests run the full matrix without docker,
// postgres, the filesystem, or the wall clock. StdDeps wires the
// production implementations.
type Deps struct {
	// Runner executes docker CLI invocations (state plane + executor).
	Runner exec.Runner
	// Clock and Sleep back the pg_isready wait without wall-clock
	// assumptions in tests.
	Clock func() time.Time
	Sleep func(time.Duration)
	// ReadFile reads the operator-prepared postgres env_file.
	ReadFile func(string) ([]byte, error)
	// OpenStores opens the topology database and constructs the
	// persistence adapters against it.
	OpenStores func(ctx context.Context, databaseURL string) (Stores, error)
	// ProvisionDB creates the topology database when missing (over the
	// maintenance connection) and applies the embedded migrations.
	ProvisionDB func(ctx context.Context, adminURL, databaseURL string) error
}

// StdDeps returns Deps with every seam wired to its production
// implementation, parametrized only by the command runner.
func StdDeps(runner exec.Runner) Deps {
	return Deps{
		Runner:      runner,
		Clock:       time.Now,
		Sleep:       time.Sleep,
		ReadFile:    os.ReadFile,
		OpenStores:  openPostgresStores,
		ProvisionDB: provisionTopologyDB,
	}
}

// openPostgresStores connects to the topology database and constructs
// the postgres adapters — the composition root's store wiring.
func openPostgresStores(ctx context.Context, databaseURL string) (Stores, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return Stores{}, fmt.Errorf("apply: open topology store: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return Stores{}, fmt.Errorf("apply: ping topology store: %w", err)
	}
	return Stores{
		Topology:  topologyadapter.NewStore(db),
		Registry:  hostadapter.NewRegistry(db),
		Artifacts: topologyadapter.NewArtifactStore(db),
	}, nil
}

// provisionTopologyDB creates the topology database over the
// maintenance connection when it does not exist yet, then applies the
// embedded migrations. Both steps are idempotent, so every apply may
// call it: second and later applies find the database and the schema
// migrated and change nothing.
func provisionTopologyDB(ctx context.Context, adminURL, databaseURL string) error {
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		return fmt.Errorf("apply: open maintenance connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, DatabaseName).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", DatabaseName)); err != nil {
			return fmt.Errorf("apply: create %s database: %w", DatabaseName, err)
		}
	case err != nil:
		return fmt.Errorf("apply: probe %s database: %w", DatabaseName, err)
	}

	if err := migrations.Up(databaseURL); err != nil {
		return fmt.Errorf("apply: migrate %s database: %w", DatabaseName, err)
	}
	return nil
}

// Input is one apply run: the config file, whether to stop after the
// render, and the operator-provided store URL (TOPOLOGY_DATABASE_URL).
// The URL is required only when the config carries no state section;
// otherwise the state plane derives it and the env value, when set,
// wins as an explicit override.
type Input struct {
	ConfigPath  string
	DryRun      bool
	DatabaseURL string
}

// HostResult is one host's converge outcome. A failed host does not
// roll back or block the others — every host is attempted and the
// failures are reported (partial-failure convergence).
type HostResult struct {
	HostID  string
	Changed bool
	Skipped bool
	Err     error
}

// Result is one apply run's outcome: the persisted revision (0 in
// dry-run, where nothing is persisted), the per-host converge results,
// and — in dry-run — the rendered compose files themselves.
type Result struct {
	Revision  int64
	DryRun    bool
	Hosts     []HostResult
	Artifacts []render.Artifact
}

// Failed reports whether any host's converge errored. The pipeline
// itself returns nil for per-host failures; the caller decides the
// exit code through Failed.
func (r *Result) Failed() bool {
	for _, h := range r.Hosts {
		if h.Err != nil {
			return true
		}
	}
	return false
}

// Pipeline converges the deployment described by a topology.yaml
// toward reality. Construct with NewPipeline.
type Pipeline struct {
	deps Deps
}

// NewPipeline wires the pipeline over the given dependencies.
func NewPipeline(deps Deps) *Pipeline {
	return &Pipeline{deps: deps}
}

// Apply runs the converge pipeline end to end: load and validate the
// config, ensure the state plane, register hosts, declare the
// topology, render per-host compose files, and converge each host's
// docker project with render-diff. Pipeline-level failures (config,
// state plane, declare) return an error; per-host converge failures
// are recorded on the result and reported, never rolled back.
func (p *Pipeline) Apply(ctx context.Context, in Input) (*Result, error) {
	cfg, err := config.Load(in.ConfigPath)
	if err != nil {
		return nil, err
	}

	plan, err := buildPlan(cfg)
	if err != nil {
		return nil, err
	}

	if in.DryRun {
		return p.dryRun(plan)
	}

	databaseURL, err := p.ensureDatabase(ctx, cfg, in.DatabaseURL)
	if err != nil {
		return nil, err
	}

	stores, err := p.deps.OpenStores(ctx, databaseURL)
	if err != nil {
		return nil, err
	}

	if err := p.registerHosts(ctx, stores, cfg, plan); err != nil {
		return nil, err
	}

	topo, err := p.declare(ctx, stores, cfg, plan)
	if err != nil {
		return nil, err
	}

	artifacts, err := p.render(cfg, topo, plan)
	if err != nil {
		return nil, err
	}

	return p.convergeHosts(ctx, stores, cfg, topo.Revision, artifacts), nil
}

// dryRun stops after the render: no docker, no database. The rendered
// compose files land on the result so the caller can print them.
func (p *Pipeline) dryRun(plan plan) (*Result, error) {
	artifacts, err := render.Render(plan.render)
	if err != nil {
		return nil, err
	}
	return &Result{DryRun: true, Artifacts: artifacts}, nil
}

// ensureDatabase brings the state plane up when the config declares
// one and derives the topology database URL, applying the operator's
// explicit override last.
func (p *Pipeline) ensureDatabase(ctx context.Context, cfg *config.Config, override string) (string, error) {
	derived := ""
	if cfg.State != nil {
		url, err := p.ensureStatePlane(ctx, cfg.State.Postgres)
		if err != nil {
			return "", err
		}
		derived = url
	}
	if override != "" {
		return override, nil
	}
	if derived == "" {
		return "", errors.New("apply: no state section in config and TOPOLOGY_DATABASE_URL is unset: one of them must provide the topology database")
	}
	return derived, nil
}

// declare registers nothing itself — that is registerHosts' job — but
// translates the plan into the T0 use case and runs it. An unchanged
// declaration no-ops (converge-idempotency: no revision bump).
func (p *Pipeline) declare(ctx context.Context, stores Stores, cfg *config.Config, plan plan) (domain.Topology, error) {
	access, err := cfg.DomainAccess()
	if err != nil {
		return domain.Topology{}, err
	}
	svc := app.NewService(stores.Topology, p.deps.Clock)
	return svc.Declare(ctx, app.DeclareInput{
		Hosts:      plan.dbHostIDs,
		Placements: plan.domainPlacements,
		Access:     access,
	})
}

// registerHosts registers every declared host with its deterministic
// database identity (hostDBID), keyed upsert by address: re-apply
// refreshes labels, and a YAML id rename surfaces as the address-
// uniqueness error rather than silently forking the host.
func (p *Pipeline) registerHosts(ctx context.Context, stores Stores, cfg *config.Config, plan plan) error {
	for i, h := range cfg.Hosts {
		host, err := hostdomain.NewHost(plan.dbHostIDs[i], h.Address, h.Labels, p.deps.Clock())
		if err != nil {
			return fmt.Errorf("apply: host %q: %w", h.ID, err)
		}
		if _, err := stores.Registry.Register(ctx, host); err != nil {
			return fmt.Errorf("apply: register host %q: %w", h.ID, err)
		}
	}
	return nil
}

// render maps the persisted topology back onto operator-facing host
// ids and renders one compose artifact per host.
func (p *Pipeline) render(cfg *config.Config, topo domain.Topology, plan plan) ([]render.Artifact, error) {
	placements := make([]domain.ComponentPlacement, 0, len(topo.Placements))
	for _, pl := range topo.Placements {
		yamlID, ok := plan.yamlHostID[pl.HostID]
		if !ok {
			return nil, fmt.Errorf("apply: placement of %q references host %q outside the config", pl.Component, pl.HostID)
		}
		pl.HostID = yamlID
		placements = append(placements, pl)
	}
	return render.Render(render.Input{
		StateHostID: cfg.StateHostID(),
		State:       cfg.RenderState(),
		Hosts:       cfg.RenderHosts(),
		Placements:  placements,
	})
}

// convergeHosts ships each artifact whose content hash changed (or
// that was never shipped) to its host's executor and persists the new
// artifact. Unchanged hashes skip entirely — no docker call. Every
// host is attempted; failures are recorded per host and reported, not
// propagated (partial-failure convergence).
func (p *Pipeline) convergeHosts(ctx context.Context, stores Stores, cfg *config.Config, revision int64, artifacts []render.Artifact) *Result {
	executor := exec.NewExecutor(p.deps.Runner, render.Project)
	hostByID := make(map[string]config.Host, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		hostByID[h.ID] = h
	}

	hosts := make([]HostResult, 0, len(artifacts))
	for _, artifact := range artifacts {
		hr := p.convergeOneHost(ctx, stores, executor, hostByID, artifact)
		hosts = append(hosts, hr)
	}
	return &Result{Revision: revision, Hosts: hosts}
}

// convergeOneHost renders the diff for one artifact and converges it.
func (p *Pipeline) convergeOneHost(ctx context.Context, stores Stores, executor *exec.Executor, hostByID map[string]config.Host, artifact render.Artifact) HostResult {
	hr := HostResult{HostID: artifact.HostID}

	stored, err := stores.Artifacts.Current(ctx, artifact.HostID, domain.ArtifactKindCompose)
	switch {
	case err == nil && stored.ContentHash == artifact.Hash:
		hr.Skipped = true
		return hr
	case err != nil && !errors.Is(err, domain.ErrNoArtifact):
		hr.Err = fmt.Errorf("read stored artifact: %w", err)
		return hr
	}

	cfgHost, ok := hostByID[artifact.HostID]
	if !ok {
		hr.Err = errors.New("rendered artifact for a host outside the config")
		return hr
	}
	host := exec.Host{ID: cfgHost.ID, Address: cfgHost.Address, SSHUser: cfgHost.SSHUser}
	if _, err := executor.Ensure(ctx, host, artifact.Compose); err != nil {
		hr.Err = err
		return hr
	}

	storedArtifact, err := domain.NewRenderArtifact(artifact.HostID, domain.ArtifactKindCompose, artifact.Hash, artifact.Compose, p.deps.Clock())
	if err != nil {
		hr.Err = fmt.Errorf("build artifact record: %w", err)
		return hr
	}
	if err := stores.Artifacts.Save(ctx, storedArtifact); err != nil {
		// The converge ran but the diff anchor did not persist: report
		// the error so the operator knows the next apply will re-ship.
		hr.Err = fmt.Errorf("persist artifact after successful converge: %w", err)
		return hr
	}
	hr.Changed = true
	return hr
}

// plan is the config translated into every shape the pipeline needs:
// the render input skeleton, the deterministic database host ids, and
// the domain placement set the Declare flow validates.
type plan struct {
	render render.Input
	// dbHostIDs parallels cfg.Hosts: the deterministic uuid each YAML
	// host id maps to in the hosts/placements tables.
	dbHostIDs []string
	// yamlHostID inverts dbHostIDs: persisted placement rows back to
	// operator-facing ids for render and reporting.
	yamlHostID map[string]string
	// domainPlacements is the declare input: placements keyed by
	// database host id.
	domainPlacements []domain.ComponentPlacement
}

// buildPlan translates the validated config into the pipeline's
// working shapes. Rendering uses operator ids; persistence uses the
// deterministic database ids (T0's hosts.id is uuid — migration 0002
// deliberately does not retrofit it, so apply owns the mapping).
func buildPlan(cfg *config.Config) (plan, error) {
	p := plan{
		render: render.Input{
			StateHostID: cfg.StateHostID(),
			State:       cfg.RenderState(),
			Hosts:       cfg.RenderHosts(),
		},
		yamlHostID: map[string]string{},
	}

	dbIDByYAML := make(map[string]string, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		dbID := hostDBID(h.ID)
		p.dbHostIDs = append(p.dbHostIDs, dbID)
		p.yamlHostID[dbID] = h.ID
		dbIDByYAML[h.ID] = dbID
	}

	for _, pl := range cfg.Placements {
		dbHost, ok := dbIDByYAML[pl.Host]
		if !ok {
			return plan{}, fmt.Errorf("apply: placement of %q references unknown host %q", pl.Component, pl.Host)
		}
		p.domainPlacements = append(p.domainPlacements, domain.ComponentPlacement{
			Component: pl.Component,
			HostID:    dbHost,
			Ports:     pl.Ports,
			Config:    pl.Config,
		})
		// The render skeleton groups by operator-facing host id (the
		// artifact key space); the dry-run path renders from it
		// directly.
		p.render.Placements = append(p.render.Placements, domain.ComponentPlacement{
			Component: pl.Component,
			HostID:    pl.Host,
			Ports:     pl.Ports,
			Config:    pl.Config,
		})
	}
	return p, nil
}

// hostDBID is the deterministic database identity of one YAML host id:
// a uuid5 in a fixed namespace. The same operator id always maps to
// the same row across applies (converge-idempotency), while T0's uuid
// host columns stay untouched. Joined hosts (T2) mint their own random
// uuids over this namespace's reserved deterministic range — the two
// id spaces never collide because T2 ids are not derived from YAML
// ids.
func hostDBID(yamlID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("looming-topology-host\x00"+yamlID)).String()
}
