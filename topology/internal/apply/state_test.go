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

	"github.com/kikakkz/looming/topology/internal/config"
	"github.com/kikakkz/looming/topology/internal/exec"
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
	})
	require.NoError(t, err)
	assert.Equal(t, "postgres://postgres:pw@127.0.0.1:5432/topology?sslmode=disable", got)
	assert.Equal(t, 2, runner.calls, "probe + readiness only: compose up is skipped on a healthy container")
	assert.Equal(t, []string{"postgres://postgres:pw@127.0.0.1:5432/topology?sslmode=disable"}, provisioned)
}
