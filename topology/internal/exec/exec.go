// SPDX-License-Identifier: Apache-2.0

// Package exec is the phase-1 executor channel (topology-l1 §6
// Renderer port, docker backend): the docker CLI with compose files
// shipped over stdin. The local backend converges the host apply runs
// on; the ssh backend sets DOCKER_HOST=ssh://<user>@<host> for the
// invocation — compose is parsed client-side and shipped over the
// admin's existing SSH keys, so no daemon port is exposed (T1 decision
// 1). A future pull-based host agent is a named deferral (trigger:
// SSH-unreachable topologies, #112 cross-NAT territory).
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Host is the executor's view of one declared host: where docker
// commands run. An empty SSHUser means local docker — the single-host
// degenerate case needs no tunnel at all.
type Host struct {
	ID      string
	Address string
	SSHUser string
}

// Runner abstracts process invocation so executor behavior is
// unit-testable without docker.
type Runner interface {
	// Run executes name with args, feeding stdin and returning stdout.
	// env entries extend (never replace) the process environment — the
	// ssh backend injects DOCKER_HOST this way. A non-zero exit
	// surfaces as *ExitError carrying stderr.
	Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, error)
}

// ExitError is a command that exited non-zero. Stderr rides along
// because command failures say why on stderr — surfacing it is what
// makes compose failures actionable for the operator.
type ExitError struct {
	Name   string
	Args   []string
	Stderr string
	Code   int
}

// Error names the command and its stderr.
func (e *ExitError) Error() string {
	return fmt.Sprintf("exec: %s %v failed (exit %d): %s", e.Name, e.Args, e.Code, e.TruncatedStderr())
}

// TruncatedStderr bounds the stderr tail that rides into error strings.
func (e *ExitError) TruncatedStderr() string {
	const max = 4096
	if len(e.Stderr) <= max {
		return e.Stderr
	}
	return "…" + e.Stderr[len(e.Stderr)-max:]
}

// LocalRunner runs commands on the machine the caller runs on.
type LocalRunner struct{}

// Run implements Runner with os/exec: stdin bytes, captured stdout,
// exit status classified into *ExitError.
func (LocalRunner) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// env entries extend the ambient environment — the exec surface
	// contract.
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &ExitError{Name: name, Args: args, Stderr: stderr.String(), Code: exitErr.ExitCode()}
		}
		return nil, fmt.Errorf("exec: %s %v: %w", name, args, err)
	}
	return stdout.Bytes(), nil
}

// Executor converges one host's compose project through a Runner.
type Executor struct {
	runner  Runner
	project string
}

// NewExecutor wires the executor; project is the compose project name
// (render.Project — "looming").
func NewExecutor(r Runner, project string) *Executor {
	return &Executor{runner: r, project: project}
}

// Ensure ships composeYAML to the host and converges the project:
// `docker compose -p <project> -f - up -d` with the YAML on stdin, so
// nothing is written to the remote filesystem. SSH hosts run with
// DOCKER_HOST=ssh://<user>@<address>. Render-diff convergence decides
// whether to call Ensure at all — the executor itself is
// unconditional, and reports changed=true when the converge ran.
func (e *Executor) Ensure(ctx context.Context, h Host, composeYAML string) (bool, error) {
	args := []string{"compose", "-p", e.project, "-f", "-", "up", "-d"}
	var env []string
	if h.SSHUser != "" {
		env = append(env, "DOCKER_HOST=ssh://"+h.SSHUser+"@"+h.Address)
	}
	if _, err := e.runner.Run(ctx, "docker", args, []byte(composeYAML), env); err != nil {
		return false, fmt.Errorf("exec: converge host %q: %w", h.ID, err)
	}
	return true, nil
}
