// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/config"
	"github.com/kikakkz/looming/platform/go/exec"
	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

func readOK(content string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(content), nil }
}

func TestParsePostgresEnvFile(t *testing.T) {
	creds, err := parsePostgresEnvFile(readOK(`
# operator-prepared secrets
POSTGRES_USER=looming

POSTGRES_PASSWORD='s3 cret'
`), "/etc/looming/postgres.env")
	require.NoError(t, err)
	assert.Equal(t, "looming", creds.user)
	assert.Equal(t, "s3 cret", creds.password, "quotes are stripped, values are verbatim otherwise")
}

func TestParsePostgresEnvFileDefaultsUser(t *testing.T) {
	creds, err := parsePostgresEnvFile(readOK("POSTGRES_PASSWORD=pw\n"), "/env")
	require.NoError(t, err)
	assert.Equal(t, "postgres", creds.user, "the upstream image's default")
}

func TestParsePostgresEnvFileFailures(t *testing.T) {
	cases := []struct {
		name    string
		content string
		readErr bool
		want    string
	}{
		{"missing password", "POSTGRES_USER=x\n", false, "POSTGRES_PASSWORD"},
		{"empty password", "POSTGRES_PASSWORD=\n", false, "empty POSTGRES_PASSWORD"},
		{"not key=value", "POSTGRES_PASSWORD\n", false, "KEY=VALUE"},
		{"unreadable file", "", true, "read postgres env_file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read := readOK(tc.content)
			if tc.readErr {
				read = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
			}
			_, err := parsePostgresEnvFile(read, "/env")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// failingRunner always fails pg_isready-style probes.
type failingRunner struct{ calls int }

func (f *failingRunner) Run(context.Context, string, []string, []byte, []string) ([]byte, error) {
	f.calls++
	return nil, &exec.ExitError{Name: "docker", Code: 2, Stderr: "not ready"}
}

func TestWaitPostgresReadyIsBounded(t *testing.T) {
	runner := &failingRunner{}
	var sleeps []time.Duration
	p := NewPipeline(Deps{
		Runner: runner,
		Clock:  func() time.Time { return fixedClock },
		Sleep:  func(d time.Duration) { sleeps = append(sleeps, d) },
	})

	err := p.waitPostgresReady(context.Background(), "name: looming\n", "postgres")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not become ready")
	assert.Equal(t, postgresWaitAttempts, runner.calls)
	assert.Len(t, sleeps, postgresWaitAttempts-1, "no pause after the final probe")
	for _, s := range sleeps {
		assert.LessOrEqual(t, s, 5*time.Second, "backoff is capped")
	}
}

var fixedClock = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestPostgresURL(t *testing.T) {
	u := postgresURL(stateCreds{user: "postgres", password: "p@ss:w/rd"}, 5433, "topology")
	assert.Equal(t, "postgres://postgres:p%40ss%3Aw%2Frd@127.0.0.1:5433/topology?sslmode=disable", u,
		"credentials are URL-escaped")
}

func TestHostDBIDIsDeterministic(t *testing.T) {
	first := hostDBID("gw-1")
	assert.Equal(t, first, hostDBID("gw-1"), "the same operator id maps to the same database row")
	assert.NotEqual(t, first, hostDBID("app-1"))
	assert.True(t, strings.Count(first, "-") >= 4, "uuid shape")
}

// probeCountingRunner serves a fixed script and counts ps/exec vs up
// calls by their compose subcommand.
type probeCountingRunner struct {
	script []scriptedResponse
	calls  int
}

type scriptedResponse struct {
	stdout string
}

func (r *probeCountingRunner) Run(_ context.Context, _ string, args []string, _ []byte, _ []string) ([]byte, error) {
	step := r.script[r.calls]
	r.calls++
	return []byte(step.stdout), nil
}

func TestEnsureStatePlaneSkipsUpWhenRunning(t *testing.T) {
	runner := &probeCountingRunner{script: []scriptedResponse{
		{stdout: "looming-bundle-postgres-1\n"}, // ps: running
		{},                                      // pg_isready ok
	}}
	var provisioned []string
	p := NewPipeline(Deps{
		Runner:   runner,
		Clock:    func() time.Time { return fixedClock },
		Sleep:    func(time.Duration) {},
		ReadFile: readOK("POSTGRES_PASSWORD=pw\n"),
		ProvisionDB: func(_ context.Context, _, databaseURL string) error {
			provisioned = append(provisioned, databaseURL)
			return nil
		},
	})

	got, err := p.ensureStatePlane(context.Background(), config.StatePostgres{
		Image:   "postgres:16-alpine",
		EnvFile: "/etc/looming/postgres.env",
		DataDir: "/var/lib/looming/postgres",
		Port:    5432,
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "postgres://postgres:pw@127.0.0.1:5432/topology?sslmode=disable", got)
	assert.Equal(t, 2, runner.calls, "probe + readiness only: compose up is skipped on a healthy container")
	assert.Equal(t, []string{"postgres://postgres:pw@127.0.0.1:5432/topology?sslmode=disable"}, provisioned)
}

// scriptedSequence serves canned responses in order (internal-package
// sibling of the external fakeRunner).
type scriptedSequence struct {
	responses []sequenceStep
	calls     int
}

type sequenceStep struct {
	stdout string
	err    error
}

func (s *scriptedSequence) Run(context.Context, string, []string, []byte, []string) ([]byte, error) {
	step := s.responses[s.calls]
	s.calls++
	return []byte(step.stdout), step.err
}

func statePlaneDeps(r exec.Runner) Deps {
	return Deps{
		Runner:   r,
		Clock:    func() time.Time { return fixedClock },
		Sleep:    func(time.Duration) {},
		ReadFile: readOK("POSTGRES_PASSWORD=pw\n"),
		ProvisionDB: func(context.Context, string, string) error {
			return nil
		},
	}
}

var statePostgresFixture = config.StatePostgres{
	Image:   "postgres:16-alpine",
	EnvFile: "/etc/looming/postgres.env",
	DataDir: "/var/lib/looming/postgres",
	Port:    5432,
}

func TestEnsureStatePlaneProbeError(t *testing.T) {
	r := &scriptedSequence{responses: []sequenceStep{
		{err: errors.New("docker daemon unavailable")},
	}}
	p := NewPipeline(statePlaneDeps(r))

	_, err := p.ensureStatePlane(context.Background(), statePostgresFixture, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "probe postgres container")
}

func TestEnsureStatePlaneComposeUpError(t *testing.T) {
	r := &scriptedSequence{responses: []sequenceStep{
		{stdout: ""}, // ps: not running
		{err: &exec.ExitError{Name: "docker", Code: 1, Stderr: "bind: address already in use"}},
	}}
	p := NewPipeline(statePlaneDeps(r))

	_, err := p.ensureStatePlane(context.Background(), statePostgresFixture, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compose up")
	assert.Contains(t, err.Error(), "address already in use")
}

func TestWaitPostgresReadyNonExitErrorStopsImmediately(t *testing.T) {
	r := &scriptedSequence{responses: []sequenceStep{
		{err: errors.New("docker exec failed to start")},
	}}
	p := NewPipeline(statePlaneDeps(r))

	err := p.waitPostgresReady(context.Background(), "name: looming\n", "postgres")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pg_isready probe")
	assert.Equal(t, 1, r.calls, "a non-exit failure is not retried: the container is broken, not slow")
}

// TestComponentDatabases pins the placement → database derivation
// (#130): an identityd placement claims the identity database, a
// topologyd placement the topology one; the list is sorted and
// deduplicated (one component may sit on several hosts) and carries
// nothing for components without a database of their own.
func TestComponentDatabases(t *testing.T) {
	placement := func(component string) config.Placement {
		return config.Placement{Component: component, Host: "local"}
	}
	cases := []struct {
		name       string
		components []string
		want       []string
	}{
		{"identityd placement", []string{domain.ComponentIdentityd}, []string{"identity"}},
		{"topologyd placement", []string{domain.ComponentTopologyd}, []string{DatabaseName}},
		{"both, sorted regardless of declaration order",
			[]string{domain.ComponentTopologyd, domain.ComponentGatewayFront, domain.ComponentIdentityd},
			[]string{"identity", DatabaseName}},
		{"gateway-front carries no database", []string{domain.ComponentGatewayFront}, nil},
		{"identityd on two hosts deduplicates",
			[]string{domain.ComponentIdentityd, domain.ComponentIdentityd}, []string{"identity"}},
		{"no placements", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var placements []config.Placement
			for _, component := range tc.components {
				placements = append(placements, placement(component))
			}
			assert.Equal(t, tc.want, componentDatabases(placements))
		})
	}
}

// TestEnsureStatePlaneProvisionsComponentDatabases pins the state
// plane's first-boot contract (#130): after the topology database is
// provisioned (create + migrate), every declared component database is
// created over the same maintenance connection — BEFORE the converge
// step starts any container, so identityd's first boot finds its
// database waiting.
func TestEnsureStatePlaneProvisionsComponentDatabases(t *testing.T) {
	r := &scriptedSequence{responses: []sequenceStep{
		{stdout: ""}, // ps: not running
		{},           // compose up -d
		{},           // pg_isready ok
	}}
	var order []string
	p := NewPipeline(Deps{
		Runner:   r,
		Clock:    func() time.Time { return fixedClock },
		Sleep:    func(time.Duration) {},
		ReadFile: readOK("POSTGRES_PASSWORD=pw\n"),
		ProvisionDB: func(_ context.Context, adminURL, databaseURL string) error {
			order = append(order, "provision:"+databaseURL)
			return nil
		},
		EnsureComponentDB: func(_ context.Context, adminURL, database string) error {
			order = append(order, "ensure:"+adminURL+":"+database)
			return nil
		},
	})

	_, err := p.ensureStatePlane(context.Background(), statePostgresFixture, []string{"identity", DatabaseName})
	require.NoError(t, err)
	adminURL := "postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable"
	assert.Equal(t, []string{
		"provision:postgres://postgres:pw@127.0.0.1:5432/topology?sslmode=disable",
		"ensure:" + adminURL + ":identity",
		"ensure:" + adminURL + ":" + DatabaseName,
	}, order, "the topology database provisions first, component databases follow on the maintenance connection")
}

// TestEnsureStatePlaneComponentDatabaseErrorAborts: a component
// database that cannot be created fails the state-plane ensure — the
// apply aborts BEFORE any converge step would start a container that
// crash-loops without its database.
func TestEnsureStatePlaneComponentDatabaseErrorAborts(t *testing.T) {
	r := &scriptedSequence{responses: []sequenceStep{
		{stdout: ""}, // ps: not running
		{},           // compose up -d
		{},           // pg_isready ok
	}}
	p := NewPipeline(Deps{
		Runner:   r,
		Clock:    func() time.Time { return fixedClock },
		Sleep:    func(time.Duration) {},
		ReadFile: readOK("POSTGRES_PASSWORD=pw\n"),
		ProvisionDB: func(context.Context, string, string) error {
			return nil
		},
		EnsureComponentDB: func(context.Context, string, string) error {
			return errors.New("permission denied")
		},
	})

	_, err := p.ensureStatePlane(context.Background(), statePostgresFixture, []string{"identity"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity", "the failing database is named for the operator")
}
