// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kikakkz/looming/platform/go/config"
	"github.com/kikakkz/looming/platform/go/exec"
	"github.com/kikakkz/looming/platform/go/render"
	domain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// State-plane ensure (topology-l1 §3 admin bootstrap journey): bring up
// the bundle-owned Postgres on the host apply runs on — the first host
// by definition in phase 1 — then make sure the topology database
// exists and is migrated. Idempotent: a healthy existing container is
// detected and `compose up` is skipped; CREATE DATABASE and the
// embedded migrations are guarded no-ops on re-apply.

// postgresWaitAttempts bounds the pg_isready poll: first boots on a
// loaded machine can take a while, but an apply that cannot reach
// Postgres must fail, not hang.
const postgresWaitAttempts = 30

// postgresReadyService is the compose service the readiness probe
// execs into.
const postgresReadyService = render.ComponentBundlePostgres

// stateCreds is the operator-provided postgres authentication parsed
// out of the env_file: apply never generates or prints credentials
// (T1 decision 5) — it only reads what the admin prepared.
type stateCreds struct {
	user     string
	password string
}

// parsePostgresEnvFile reads KEY=VALUE pairs from the operator's
// env_file. POSTGRES_USER defaults to postgres (the upstream image's
// default); POSTGRES_PASSWORD is mandatory — without it the state
// plane cannot come up, and inventing one is exactly the
// secret-materialization T1 forbids.
func parsePostgresEnvFile(readFile func(string) ([]byte, error), path string) (stateCreds, error) {
	data, err := readFile(path)
	if err != nil {
		return stateCreds{}, fmt.Errorf("state plane: read postgres env_file %q: %w", path, err)
	}
	creds := stateCreds{user: "postgres"}
	foundPassword := false
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return stateCreds{}, fmt.Errorf("state plane: env_file %q line %d is not KEY=VALUE: %q", path, lineNo+1, line)
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "POSTGRES_USER":
			if value != "" {
				creds.user = value
			}
		case "POSTGRES_PASSWORD":
			if value == "" {
				return stateCreds{}, fmt.Errorf("state plane: env_file %q sets an empty POSTGRES_PASSWORD", path)
			}
			creds.password = value
			foundPassword = true
		}
	}
	if !foundPassword {
		return stateCreds{}, fmt.Errorf("state plane: env_file %q must set POSTGRES_PASSWORD (apply never generates credentials)", path)
	}
	return creds, nil
}

// composeArgs builds the docker-compose invocation: the pipeline's
// project, config always from stdin (nothing is written to disk,
// locally or over the SSH tunnel).
func (p *Pipeline) composeArgs(rest ...string) []string {
	return append([]string{"compose", "-p", p.deps.Project, "-f", "-"}, rest...)
}

// ensureStatePlane converges the bundle Postgres and returns the
// topology database URL derived from the operator's env_file. Steps:
// container up when absent or stopped, bounded pg_isready wait, then
// CREATE DATABASE IF NOT EXISTS plus embedded migrations through the
// ProvisionDB seam, and finally every declared component database
// (create-only) through EnsureComponentDB — all BEFORE the converge
// step starts the containers that point at those databases (#130).
func (p *Pipeline) ensureStatePlane(ctx context.Context, sp config.StatePostgres, databases []string) (string, error) {
	creds, err := parsePostgresEnvFile(p.deps.ReadFile, sp.EnvFile)
	if err != nil {
		return "", err
	}

	compose, err := render.StateCompose(render.StatePostgres{
		Image:   sp.Image,
		EnvFile: sp.EnvFile,
		DataDir: sp.DataDir,
		Port:    sp.Port,
	})
	if err != nil {
		return "", err
	}

	running, err := p.postgresContainerRunning(ctx, compose)
	if err != nil {
		return "", err
	}
	if !running {
		if _, err := p.deps.Runner.Run(ctx, "docker", p.composeArgs("up", "-d"), []byte(compose), nil); err != nil {
			return "", fmt.Errorf("state plane: compose up: %w", err)
		}
	}

	if err := p.waitPostgresReady(ctx, compose, creds.user); err != nil {
		return "", err
	}

	adminURL := postgresURL(creds, sp.Port, "postgres")
	databaseURL := postgresURL(creds, sp.Port, DatabaseName)
	if err := p.deps.ProvisionDB(ctx, adminURL, databaseURL); err != nil {
		return "", fmt.Errorf("state plane: provision %s database: %w", DatabaseName, err)
	}

	if p.deps.EnsureComponentDB != nil {
		// Component databases ride the same maintenance connection and
		// catalog-guarded CREATE pattern as the topology database;
		// identityd's placement already points IDENTITY_DATABASE_URL at
		// the identity database — first boot finds it waiting.
		for _, database := range databases {
			if err := p.deps.EnsureComponentDB(ctx, adminURL, database); err != nil {
				return "", fmt.Errorf("state plane: ensure %s database: %w", database, err)
			}
		}
	}
	return databaseURL, nil
}

// postgresContainerRunning reports whether the bundle-postgres
// container is up: compose ps -q with a running-status filter prints
// the container id when healthy and nothing otherwise.
func (p *Pipeline) postgresContainerRunning(ctx context.Context, compose string) (bool, error) {
	out, err := p.deps.Runner.Run(ctx, "docker",
		p.composeArgs("ps", "-q", "--status", "running", postgresReadyService),
		[]byte(compose), nil)
	if err != nil {
		return false, fmt.Errorf("state plane: probe postgres container: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// waitPostgresReady polls pg_isready inside the container with bounded
// linear backoff (the Sleep seam keeps unit tests off the clock). A
// container that never accepts connections fails the apply.
func (p *Pipeline) waitPostgresReady(ctx context.Context, compose, user string) error {
	// -h 127.0.0.1 probes TCP: a fresh data dir boots a temporary
	// server that accepts only Unix-socket connections, and a socket
	// probe could report ready before the real listener exists.
	args := p.composeArgs("exec", "-T", postgresReadyService, "pg_isready", "-h", "127.0.0.1", "-U", user)
	for attempt := 1; attempt <= postgresWaitAttempts; attempt++ {
		if _, err := p.deps.Runner.Run(ctx, "docker", args, []byte(compose), nil); err == nil {
			return nil
		} else {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return fmt.Errorf("state plane: pg_isready probe: %w", err)
			}
		}
		if attempt < postgresWaitAttempts {
			// Linear backoff capped at five seconds per pause.
			pause := time.Duration(attempt) * time.Second
			if pause > 5*time.Second {
				pause = 5 * time.Second
			}
			p.deps.Sleep(pause)
		}
	}
	return fmt.Errorf("state plane: postgres did not become ready within %d pg_isready probes", postgresWaitAttempts)
}

// postgresURL builds the connection URL for one database on the
// bundle Postgres. Host is the loopback: the state plane is local by
// definition in phase 1 (apply runs on the first host). The bundle
// cluster is created with SSL off (plain trusted network, phase-1
// envelope), so the derived URLs pin sslmode=disable rather than
// probing — the pgtest precedent.
func postgresURL(creds stateCreds, port int, database string) string {
	hostPort := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	auth := url.UserPassword(creds.user, creds.password)
	u := &url.URL{Scheme: "postgres", User: auth, Host: hostPort, Path: database}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// componentDatabaseNames maps a placed phase-1 component to the
// database the state plane must create for it (AD-36
// database-per-component): identityd points IDENTITY_DATABASE_URL at
// the identity database via its placement's database_url config, and
// topologyd serves the topology one. Components without an entry own
// no database of their own.
var componentDatabaseNames = map[string]string{
	domain.ComponentIdentityd: "identity",
	domain.ComponentTopologyd: DatabaseName,
}

// componentDatabases derives the distinct, sorted database names the
// declared placements require (#130). A component may sit on several
// hosts; the create list deduplicates so each database is ensured
// exactly once per apply.
func componentDatabases(placements []config.Placement) []string {
	seen := map[string]bool{}
	var databases []string
	for _, pl := range placements {
		database, ok := componentDatabaseNames[pl.Component]
		if !ok || seen[database] {
			continue
		}
		seen[database] = true
		databases = append(databases, database)
	}
	sort.Strings(databases)
	return databases
}
