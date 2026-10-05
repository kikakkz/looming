// SPDX-License-Identifier: Apache-2.0

package apply_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/apply"
	"github.com/kikakkz/looming/topology/internal/config"
	"github.com/kikakkz/looming/topology/internal/exec"
	guidedomain "github.com/kikakkz/looming/topology/internal/guide/domain"
	hostdomain "github.com/kikakkz/looming/topology/internal/host/domain"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

var ctx = context.Background()

var fixedNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// scriptedCall is one docker CLI response the fake runner serves.
type scriptedCall struct {
	stdout string
	err    error
}

// fakeRunner serves scripted responses in order and records calls.
type fakeRunner struct {
	script []scriptedCall
	calls  []fakeCall
}

type fakeCall struct {
	args  []string
	stdin string
	env   []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin []byte, env []string) ([]byte, error) {
	if name != "docker" {
		panic("runner must invoke docker, got " + name)
	}
	f.calls = append(f.calls, fakeCall{args: append([]string(nil), args...), stdin: string(stdin), env: append([]string(nil), env...)})
	i := len(f.calls) - 1
	if i >= len(f.script) {
		panic("fake runner out of script")
	}
	step := f.script[i]
	return []byte(step.stdout), step.err
}

func (f *fakeRunner) findCalls(want ...string) []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if len(c.args) >= len(want) && equalStrings(c.args[:len(want)], want) {
			out = append(out, c)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeStore is the topology Store port against memory.
type fakeStore struct {
	current domain.Topology
	saved   []domain.Topology
}

func (f *fakeStore) Save(_ context.Context, t domain.Topology) error {
	f.saved = append(f.saved, t)
	f.current = t
	return nil
}

func (f *fakeStore) Current(context.Context) (domain.Topology, error) {
	if f.current.Revision == 0 {
		return domain.Topology{}, domain.ErrNoTopology
	}
	return f.current, nil
}

// fakeRegistry is the host Registry port against memory.
type fakeRegistry struct {
	byAddress map[string]*hostdomain.Host
	byID      map[string]*hostdomain.Host
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{byAddress: map[string]*hostdomain.Host{}, byID: map[string]*hostdomain.Host{}}
}

func (f *fakeRegistry) Register(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	if prev, taken := f.byAddress[h.Address]; taken && prev.ID != h.ID {
		return nil, hostdomain.ErrAddressTaken
	}
	f.byAddress[h.Address] = h
	f.byID[h.ID] = h
	return h, nil
}

func (f *fakeRegistry) ByID(_ context.Context, id string) (*hostdomain.Host, error) {
	if h, ok := f.byID[id]; ok {
		return h, nil
	}
	return nil, hostdomain.ErrNotFound
}

func (f *fakeRegistry) ByAddress(_ context.Context, address string) (*hostdomain.Host, error) {
	if h, ok := f.byAddress[address]; ok {
		return h, nil
	}
	return nil, hostdomain.ErrNotFound
}

func (f *fakeRegistry) Update(_ context.Context, h *hostdomain.Host) (*hostdomain.Host, error) {
	f.byID[h.ID] = h
	f.byAddress[h.Address] = h
	return h, nil
}

// fakeArtifacts is the ArtifactStore port against memory.
type fakeArtifacts struct {
	artifacts map[string]domain.RenderArtifact
	saved     []domain.RenderArtifact
}

func newFakeArtifacts() *fakeArtifacts {
	return &fakeArtifacts{artifacts: map[string]domain.RenderArtifact{}}
}

func artifactKey(hostID, kind string) string { return hostID + "\x00" + kind }

func (f *fakeArtifacts) Current(_ context.Context, hostID, kind string) (domain.RenderArtifact, error) {
	if a, ok := f.artifacts[artifactKey(hostID, kind)]; ok {
		return a, nil
	}
	return domain.RenderArtifact{}, domain.ErrNoArtifact
}

func (f *fakeArtifacts) Save(_ context.Context, a domain.RenderArtifact) error {
	f.artifacts[artifactKey(a.HostID, a.Kind)] = a
	f.saved = append(f.saved, a)
	return nil
}

// fakeGuides is the guide Store port against memory.
type fakeGuides struct {
	guides map[string]guidedomain.Guide
	saved  []guidedomain.Guide
	err    error
}

func newFakeGuides() *fakeGuides {
	return &fakeGuides{guides: map[string]guidedomain.Guide{}}
}

func (f *fakeGuides) Current(context.Context) (guidedomain.Guide, error) {
	if f.err != nil {
		return guidedomain.Guide{}, f.err
	}
	g, ok := f.guides[guidedomain.SingletonID]
	if !ok {
		return guidedomain.Guide{}, guidedomain.ErrNoGuide
	}
	return g, nil
}

func (f *fakeGuides) Save(_ context.Context, g guidedomain.Guide) error {
	if f.err != nil {
		return f.err
	}
	f.guides[guidedomain.SingletonID] = g
	f.saved = append(f.saved, g)
	return nil
}

// env is the fake world one apply run (or a sequence of runs) shares:
// docker scripts plus every persistence seam.
type world struct {
	runner      *fakeRunner
	stores      *apply.Stores
	registry    *fakeRegistry
	artifacts   *fakeArtifacts
	guides      *fakeGuides
	topology    *fakeStore
	provisions  []provisionCall
	opens       []string
	sleeps      []time.Duration
	readFiles   map[string]string
	pipeline    *apply.Pipeline
	inviteCalls []inviteCall
	inviteFn    func(endpoint, key, email string) (int, []byte, error)
}

type provisionCall struct {
	adminURL, databaseURL string
}

type inviteCall struct {
	endpoint, key, email string
}

// inviteResponse is the happy-path identityd 201 body.
const inviteResponse = `{"token":"invite-token-1","expires_at":"2026-10-07T00:00:00Z","invite_url_path":"/v1/self/register"}`

// newWorld builds the shared fake world. Postgres env_file content is
// served from readFiles.
func newWorld(t *testing.T, script []scriptedCall, readFiles map[string]string) *world {
	t.Helper()
	w := &world{
		runner:    &fakeRunner{script: script},
		registry:  newFakeRegistry(),
		artifacts: newFakeArtifacts(),
		guides:    newFakeGuides(),
		topology:  &fakeStore{},
		readFiles: readFiles,
	}
	w.stores = &apply.Stores{Topology: w.topology, Registry: w.registry, Artifacts: w.artifacts, Guides: w.guides}
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(d time.Duration) { w.sleeps = append(w.sleeps, d) },
		ReadFile: w.readFile,
		OpenStores: func(_ context.Context, databaseURL string) (apply.Stores, error) {
			w.opens = append(w.opens, databaseURL)
			return *w.stores, nil
		},
		ProvisionDB: func(_ context.Context, adminURL, databaseURL string) error {
			w.provisions = append(w.provisions, provisionCall{adminURL: adminURL, databaseURL: databaseURL})
			return nil
		},
		InvitePoster: func(_ context.Context, endpoint, key, email string) (int, []byte, error) {
			w.inviteCalls = append(w.inviteCalls, inviteCall{endpoint: endpoint, key: key, email: email})
			if w.inviteFn != nil {
				return w.inviteFn(endpoint, key, email)
			}
			return http.StatusCreated, []byte(inviteResponse), nil
		},
	})
	return w
}

func (w *world) readFile(path string) ([]byte, error) {
	content, ok := w.readFiles[path]
	if !ok {
		return nil, errors.New("file not found: " + path)
	}
	return []byte(content), nil
}

// pgUpScript scripts the state-plane happy path: container absent, up
// ok, pg_isready fails once then succeeds.
func pgUpScript() []scriptedCall {
	return []scriptedCall{
		{stdout: ""}, // ps: not running
		{},           // compose up -d
		{err: &exec.ExitError{Name: "docker", Code: 2, Stderr: "not ready"}},
		{}, // pg_isready ok
	}
}

const stateEnvFile = "/etc/looming/postgres.env"

const stateEnvFileContent = "POSTGRES_USER=postgres\nPOSTGRES_PASSWORD=s3cret\n"

// twoHostConfig is the brief's example topology: gw-1 local (no
// ssh_user — the single-host degenerate), app-1 over ssh.
const twoHostConfig = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "` + stateEnvFile + `", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts:
  - id: gw-1
    address: 10.0.0.11
    labels: [gateway]
  - id: app-1
    address: 10.0.0.12
    ssh_user: root
    labels: [identity]
placements:
  - {component: gateway-front, host: gw-1, ports: {http: 8080}, config: {upstream: "http://10.0.0.13:4000"}}
  - {component: identityd, host: app-1, ports: {http: 8081}, config: {database_url: "postgres://postgres:s3cret@10.0.0.11:5432/identity"}}
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// composeUpCalls returns every `compose ... up -d --remove-orphans`
// call (state plane and per-host ensures alike).
func (w *world) composeUpCalls() []fakeCall {
	return w.runner.findCalls("compose", "-p", "looming", "-f", "-", "up", "-d", "--remove-orphans")
}

// downCalls returns every project-scoped `compose down` call.
func (w *world) downCalls() []fakeCall {
	return w.runner.findCalls("compose", "-p", "looming", "down")
}

func TestApplyFirstBootConvergesEveryHost(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure gw-1
		scriptedCall{}, // ensure app-1
	), map[string]string{stateEnvFile: stateEnvFileContent})

	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.DryRun)
	assert.Equal(t, int64(1), res.Revision)
	assert.False(t, res.Failed())

	// Hosts registered with deterministic database ids: same id across
	// applies, distinct between hosts.
	gw, err := w.registry.ByAddress(ctx, "10.0.0.11")
	require.NoError(t, err)
	app, err := w.registry.ByAddress(ctx, "10.0.0.12")
	require.NoError(t, err)
	assert.NotEqual(t, gw.ID, app.ID)
	gwAgain, err := w.registry.ByID(ctx, gw.ID)
	require.NoError(t, err)
	assert.Equal(t, gw, gwAgain)

	// State plane: probe, up, two readiness polls, then provision over
	// the maintenance URL; the component URL derives from the env_file.
	assert.Len(t, w.provisions, 1)
	assert.Equal(t, "postgres://postgres:s3cret@127.0.0.1:5432/postgres?sslmode=disable", w.provisions[0].adminURL)
	assert.Equal(t, "postgres://postgres:s3cret@127.0.0.1:5432/topology?sslmode=disable", w.provisions[0].databaseURL)
	assert.Equal(t, []string{"postgres://postgres:s3cret@127.0.0.1:5432/topology?sslmode=disable"}, w.opens)

	// Both hosts changed; results follow the renderer's sorted host
	// order (app-1 before gw-1). The ssh host's ensure carried
	// DOCKER_HOST; the local one carried nothing.
	require.Len(t, res.Hosts, 2)
	assert.Equal(t, apply.HostResult{HostID: "app-1", Changed: true}, res.Hosts[0])
	assert.Equal(t, apply.HostResult{HostID: "gw-1", Changed: true}, res.Hosts[1])
	ups := w.composeUpCalls()
	require.Len(t, ups, 2, "two host ensures (the state plane's up carries no --remove-orphans)")
	assert.Equal(t, []string{"DOCKER_HOST=ssh://root@10.0.0.12"}, ups[0].env)
	assert.Empty(t, ups[1].env, "gw-1 is local: no DOCKER_HOST")
	assert.Contains(t, ups[0].stdin, "identityd:")
	assert.Contains(t, ups[1].stdin, "gateway-front:")

	// Artifacts persisted as the render-diff anchor.
	require.Len(t, w.artifacts.saved, 2)
	assert.Equal(t, "app-1", w.artifacts.saved[0].HostID)
	assert.Equal(t, "compose", w.artifacts.saved[0].Kind)
	assert.True(t, strings.HasPrefix(w.artifacts.saved[0].ContentHash, "sha256:"))
}

func TestApplySecondRunSkipsEverything(t *testing.T) {
	// Script both runs up front: run 1 is the first-boot path (probe,
	// up, two readiness polls, two ensures); run 2 finds the container
	// running and every artifact unchanged.
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // run 1: ensure gw-1
		scriptedCall{}, // run 1: ensure app-1
		scriptedCall{stdout: "looming-bundle-postgres-1\n"}, // run 2: ps — container running
		scriptedCall{}, // run 2: pg_isready ok
	), map[string]string{stateEnvFile: stateEnvFileContent})

	path := writeConfig(t, twoHostConfig)
	first, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.Len(t, first.Hosts, 2)

	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	assert.False(t, second.Failed())
	assert.Equal(t, int64(1), second.Revision, "identical declare must not bump the revision")
	for _, h := range second.Hosts {
		assert.True(t, h.Skipped, "unchanged artifacts skip entirely: %s", h.HostID)
		assert.False(t, h.Changed)
	}
	ups := w.composeUpCalls()
	assert.Len(t, ups, 2, "run 1's two ensures only: run 2 must issue zero compose up calls")
	assert.Len(t, w.provisions, 2, "re-apply still provisions (guarded no-op)")
}

func TestApplyChangedPlacementConvergesOnlyThatHost(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // run 1: ensure gw-1
		scriptedCall{}, // run 1: ensure app-1
		scriptedCall{stdout: "looming-bundle-postgres-1\n"},
		scriptedCall{}, // run 2: pg_isready ok
		scriptedCall{}, // run 2: ensure app-1 only
	), map[string]string{stateEnvFile: stateEnvFileContent})

	path := writeConfig(t, twoHostConfig)
	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)

	// The operator moves identityd to a new port: app-1's artifact
	// changes, gw-1's does not.
	changed := strings.Replace(twoHostConfig, "http: 8081", "http: 8082", 1)
	path2 := writeConfig(t, changed)
	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path2})
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.Revision, "changed declare bumps the revision")
	require.Len(t, second.Hosts, 2)
	assert.True(t, second.Hosts[0].Changed, "app-1 changed")
	assert.True(t, second.Hosts[1].Skipped, "gw-1 unchanged")

	ups := w.composeUpCalls()
	require.Len(t, ups, 3, "2 first-boot ensures + exactly one re-ensure (the state plane's up carries no --remove-orphans)")
	assert.Equal(t, []string{"DOCKER_HOST=ssh://root@10.0.0.12"}, ups[2].env)
	assert.Contains(t, ups[2].stdin, "8082:8082")
}

func TestApplyPartialFailureReportsAndContinues(t *testing.T) {
	// app-1's ensure succeeds, gw-1's fails: convergence is per-host.
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure app-1 ok
		scriptedCall{err: &exec.ExitError{Name: "docker", Code: 1, Stderr: "network gw-net not found"}},
	), map[string]string{stateEnvFile: stateEnvFileContent})

	path := writeConfig(t, twoHostConfig)
	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err, "per-host failures never abort the pipeline")
	require.True(t, res.Failed())

	require.Len(t, res.Hosts, 2)
	assert.Equal(t, apply.HostResult{HostID: "app-1", Changed: true}, res.Hosts[0])
	require.Error(t, res.Hosts[1].Err)
	assert.Contains(t, res.Hosts[1].Err.Error(), "network gw-net not found",
		"the operator sees compose's stderr")
	assert.Contains(t, res.Hosts[1].Err.Error(), "gw-1")

	// app-1's artifact persisted (the diff anchor), gw-1's did not.
	_, err = w.artifacts.Current(ctx, "app-1", domain.ArtifactKindCompose)
	assert.NoError(t, err)
	_, err = w.artifacts.Current(ctx, "gw-1", domain.ArtifactKindCompose)
	assert.ErrorIs(t, err, domain.ErrNoArtifact)
}

func TestApplyDryRunTouchesNothing(t *testing.T) {
	w := newWorld(t, nil, map[string]string{stateEnvFile: stateEnvFileContent})

	res, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath: writeConfig(t, twoHostConfig),
		DryRun:     true,
	})
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.Zero(t, res.Revision)
	assert.Empty(t, res.Hosts)
	assert.Empty(t, w.runner.calls, "dry-run must not touch docker")
	assert.Empty(t, w.opens, "dry-run must not touch the database")
	assert.Empty(t, w.provisions)
	require.Len(t, res.Artifacts, 2, "the rendered compose files are the dry-run output")
	assert.Equal(t, "app-1", res.Artifacts[0].HostID)
	assert.Contains(t, res.Artifacts[0].Compose, "identityd:")
	assert.Contains(t, res.Artifacts[1].Compose, "bundle-postgres:", "the state host carries the state plane")
}

func TestApplyWithoutStateSectionNeedsDatabaseURL(t *testing.T) {
	plain := strings.Replace(twoHostConfig, `
state:
  postgres: {image: "postgres:16-alpine", env_file: "/etc/looming/postgres.env", data_dir: "/var/lib/looming/postgres", port: 5432}`, "", 1)

	w := newWorld(t, nil, nil)
	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, plain)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TOPOLOGY_DATABASE_URL")
	assert.Empty(t, w.runner.calls)

	// With the env URL, the state plane is skipped entirely: only the
	// two host ensures touch docker, no probe/up/exec for Postgres.
	w = newWorld(t, []scriptedCall{{}, {}}, nil)
	res, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  writeConfig(t, plain),
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.NoError(t, err)
	assert.False(t, res.Failed())
	assert.Equal(t, []string{"postgres://topology:t@10.0.0.11:5432/topology"}, w.opens)
	assert.Empty(t, w.runner.findCalls("compose", "-p", "looming", "-f", "-", "ps"), "no state-plane probe")
	assert.Empty(t, w.runner.findCalls("compose", "-p", "looming", "-f", "-", "exec"), "no pg_isready wait")
	assert.Len(t, w.composeUpCalls(), 2, "only the two host ensures")
	assert.Empty(t, w.provisions)
}

func TestApplyEnvURLWinsOverDerived(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure gw-1
		scriptedCall{}, // ensure app-1
	), map[string]string{stateEnvFile: stateEnvFileContent})

	_, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  writeConfig(t, twoHostConfig),
		DatabaseURL: "postgres://override:o@db.internal:5432/topology",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"postgres://override:o@db.internal:5432/topology"}, w.opens,
		"TOPOLOGY_DATABASE_URL is the operator's explicit override")
	// The state plane still ensured (it is the thing the URL points at).
	assert.Len(t, w.provisions, 1)
}

func TestApplyIdleHostConvergesByProjectRemoval(t *testing.T) {
	// three declared hosts; idle-1 carries no placements: the pipeline
	// removes its compose project instead of shipping an up.
	idleConfig := `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - {id: gw-1, address: 10.0.0.11}
  - {id: app-1, address: 10.0.0.12, ssh_user: root}
  - {id: idle-1, address: 10.0.0.99, ssh_user: root}
placements:
  - {component: gateway-front, host: gw-1, ports: {http: 8080}}
  - {component: identityd, host: app-1, ports: {http: 8081}}
`
	w := newWorld(t, []scriptedCall{
		{}, // ensure app-1
		{}, // ensure gw-1
		{}, // remove idle-1
	}, nil)

	res, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  writeConfig(t, idleConfig),
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.NoError(t, err)
	require.False(t, res.Failed())
	require.Len(t, res.Hosts, 3)
	assert.Equal(t, apply.HostResult{HostID: "app-1", Changed: true}, res.Hosts[0])
	assert.Equal(t, apply.HostResult{HostID: "gw-1", Changed: true}, res.Hosts[1])
	assert.Equal(t, apply.HostResult{HostID: "idle-1", Changed: true}, res.Hosts[2],
		"tearing the project down is a change")

	downs := w.downCalls()
	require.Len(t, downs, 1)
	assert.Equal(t, []string{"DOCKER_HOST=ssh://root@10.0.0.99"}, downs[0].env)
	assert.Len(t, w.composeUpCalls(), 2, "two ensures; idle hosts get no up")

	// Second apply: the stored empty-artifact hash matches → skip.
	second, err := w.pipeline.Apply(ctx, apply.Input{
		ConfigPath:  writeConfig(t, idleConfig),
		DatabaseURL: "postgres://topology:t@10.0.0.11:5432/topology",
	})
	require.NoError(t, err)
	for _, h := range second.Hosts {
		assert.True(t, h.Skipped, "unchanged artifacts skip entirely: %s", h.HostID)
	}
	assert.Len(t, w.downCalls(), 1, "no second down without a render change")
}

func TestApplyConfigErrorAbortsBeforeAnySideEffect(t *testing.T) {
	w := newWorld(t, nil, nil)
	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, "version: [")})
	assert.Error(t, err)
	assert.Empty(t, w.runner.calls)
	assert.Empty(t, w.opens)
}

func TestApplyOpenStoresErrorAborts(t *testing.T) {
	w := newWorld(t, pgUpScript(), map[string]string{stateEnvFile: stateEnvFileContent})
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(time.Duration) {},
		ReadFile: w.readFile,
		OpenStores: func(context.Context, string) (apply.Stores, error) {
			return apply.Stores{}, errors.New("connection refused")
		},
		ProvisionDB: func(context.Context, string, string) error { return nil },
	})

	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}

type failingRegistry struct{ *fakeRegistry }

func (failingRegistry) Register(context.Context, *hostdomain.Host) (*hostdomain.Host, error) {
	return nil, errors.New("registry down")
}

func TestApplyRegisterHostsErrorAborts(t *testing.T) {
	w := newWorld(t, pgUpScript(), map[string]string{stateEnvFile: stateEnvFileContent})
	stores := *w.stores
	stores.Registry = failingRegistry{w.registry}
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(time.Duration) {},
		ReadFile: w.readFile,
		OpenStores: func(context.Context, string) (apply.Stores, error) {
			return stores, nil
		},
		ProvisionDB: func(context.Context, string, string) error { return nil },
	})

	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register host")
	assert.Contains(t, err.Error(), "registry down")
}

type failingSaveStore struct{ *fakeStore }

func (failingSaveStore) Save(context.Context, domain.Topology) error {
	return errors.New("disk full")
}

func TestApplyDeclareErrorAborts(t *testing.T) {
	w := newWorld(t, pgUpScript(), map[string]string{stateEnvFile: stateEnvFileContent})
	stores := *w.stores
	stores.Topology = failingSaveStore{w.topology}
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(time.Duration) {},
		ReadFile: w.readFile,
		OpenStores: func(context.Context, string) (apply.Stores, error) {
			return stores, nil
		},
		ProvisionDB: func(context.Context, string, string) error { return nil },
	})

	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
}

// erroringArtifacts fails Current or Save as scripted.
type erroringArtifacts struct {
	inner   *fakeArtifacts
	current error
	save    error
}

func (e *erroringArtifacts) Current(ctx context.Context, hostID, kind string) (domain.RenderArtifact, error) {
	if e.current != nil {
		return domain.RenderArtifact{}, e.current
	}
	return e.inner.Current(ctx, hostID, kind)
}

func (e *erroringArtifacts) Save(ctx context.Context, a domain.RenderArtifact) error {
	if e.save != nil {
		return e.save
	}
	return e.inner.Save(ctx, a)
}

func TestConvergeArtifactReadErrorIsPerHost(t *testing.T) {
	w := newWorld(t, pgUpScript(), map[string]string{stateEnvFile: stateEnvFileContent})
	artifacts := &erroringArtifacts{inner: newFakeArtifacts(), current: errors.New("db unavailable")}
	stores := *w.stores
	stores.Artifacts = artifacts
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(time.Duration) {},
		ReadFile: w.readFile,
		OpenStores: func(context.Context, string) (apply.Stores, error) {
			return stores, nil
		},
		ProvisionDB: func(context.Context, string, string) error { return nil },
	})

	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	require.True(t, res.Failed())
	for _, h := range res.Hosts {
		require.Error(t, h.Err)
		assert.Contains(t, h.Err.Error(), "db unavailable")
	}
}

func TestConvergeArtifactSaveErrorReportsConvergedButUnanchored(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure app-1
		scriptedCall{}, // ensure gw-1
	), map[string]string{stateEnvFile: stateEnvFileContent})
	artifacts := &erroringArtifacts{inner: newFakeArtifacts(), save: errors.New("artifact write failed")}
	stores := *w.stores
	stores.Artifacts = artifacts
	w.pipeline = apply.NewPipeline(apply.Deps{
		Runner:   w.runner,
		Clock:    func() time.Time { return fixedNow },
		Sleep:    func(time.Duration) {},
		ReadFile: w.readFile,
		OpenStores: func(context.Context, string) (apply.Stores, error) {
			return stores, nil
		},
		ProvisionDB: func(context.Context, string, string) error { return nil },
	})

	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	require.True(t, res.Failed())
	for _, h := range res.Hosts {
		require.Error(t, h.Err)
		assert.False(t, h.Changed, "the error carries the outcome; changed stays false")
		assert.Contains(t, h.Err.Error(), "persist artifact after successful converge")
	}
	// The converge itself ran: docker up was issued for both hosts (the
	// state plane's up carries no --remove-orphans — on the state host,
	// placement containers are not orphans).
	assert.Len(t, w.composeUpCalls(), 2)
}

// TestApplyRendersGuideAfterConverge pins the T3 rule: a successful
// converge renders the guide from the persisted topology revision plus
// the config's presentation facts; an unchanged second apply skips the
// re-render entirely.
func TestApplyRendersGuideAfterConverge(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure gw-1
		scriptedCall{}, // ensure app-1
		scriptedCall{stdout: "looming-bundle-postgres-1\n"}, // run 2: ps
		scriptedCall{}, // run 2: pg_isready ok
	), map[string]string{stateEnvFile: stateEnvFileContent})

	namedConfig := strings.Replace(twoHostConfig,
		`access: {mode: public, transport: direct, endpoint: "10.0.0.10"}`,
		"access: {mode: public, transport: direct, endpoint: \"10.0.0.10\"}\ncluster: {name: \"prod cluster\"}", 1)
	path := writeConfig(t, namedConfig)

	first, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.NotNil(t, first.Guide)
	assert.True(t, first.Guide.Rendered)

	require.Len(t, w.guides.saved, 1)
	guide := w.guides.saved[0]
	assert.Equal(t, first.Revision, guide.RenderedRev)
	assert.Equal(t, "prod cluster", guide.Snapshot.ClusterName)
	assert.Equal(t, "http://10.0.0.12:8081", guide.Snapshot.IdentityURL)
	assert.Equal(t, "http://10.0.0.11:8080", guide.Snapshot.GatewayURL)
	assert.True(t, guide.Snapshot.AccessPublic)
	assert.NotEmpty(t, guide.Snapshot.RegisterHint)
	require.Len(t, guide.Snapshot.Steps, 4)

	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)
	require.NotNil(t, second.Guide)
	assert.False(t, second.Guide.Rendered, "same revision: no re-render")
	assert.Len(t, w.guides.saved, 1, "no second guide write")
}

// TestApplyRerendersGuideOnRevisionChange: a changed declare moves the
// revision, and the guide follows it.
func TestApplyRerendersGuideOnRevisionChange(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // run 1: ensure gw-1
		scriptedCall{}, // run 1: ensure app-1
		scriptedCall{stdout: "looming-bundle-postgres-1\n"},
		scriptedCall{}, // run 2: pg_isready ok
		scriptedCall{}, // run 2: ensure app-1 only
	), map[string]string{stateEnvFile: stateEnvFileContent})

	path := writeConfig(t, twoHostConfig)
	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: path})
	require.NoError(t, err)

	changed := strings.Replace(twoHostConfig, "http: 8081", "http: 8082", 1)
	second, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, changed)})
	require.NoError(t, err)
	require.NotNil(t, second.Guide)
	assert.True(t, second.Guide.Rendered)
	assert.Len(t, w.guides.saved, 2)
	assert.Equal(t, int64(2), w.guides.saved[1].RenderedRev)
	assert.Contains(t, w.guides.saved[1].Snapshot.IdentityURL, ":8082")
}

// TestApplyGuideFailureWarnsAndContinues: the converge already
// succeeded when the guide renders — a guide failure warns on the
// result instead of failing the run (the invite precedent).
func TestApplyGuideFailureWarnsAndContinues(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure gw-1
		scriptedCall{}, // ensure app-1
	), map[string]string{stateEnvFile: stateEnvFileContent})
	w.guides.err = errors.New("guide table locked")

	res, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	assert.False(t, res.Failed())
	require.NotNil(t, res.Guide)
	require.Error(t, res.Guide.Err)
	assert.Contains(t, res.Guide.Err.Error(), "guide table locked")
	assert.Empty(t, w.guides.saved)
}

// TestApplyGuideUsesConfigDefaults: with no cluster section the guide
// renders the default cluster name and the default CLI download URL.
func TestApplyGuideUsesConfigDefaults(t *testing.T) {
	w := newWorld(t, append(pgUpScript(),
		scriptedCall{}, // ensure gw-1
		scriptedCall{}, // ensure app-1
	), map[string]string{stateEnvFile: stateEnvFileContent})

	_, err := w.pipeline.Apply(ctx, apply.Input{ConfigPath: writeConfig(t, twoHostConfig)})
	require.NoError(t, err)
	require.Len(t, w.guides.saved, 1)
	assert.Equal(t, config.DefaultClusterName, w.guides.saved[0].Snapshot.ClusterName)
	assert.Equal(t, config.DefaultCLIDownloadURL, w.guides.saved[0].Snapshot.CLIDownloadURL)
}
