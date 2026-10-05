// SPDX-License-Identifier: Apache-2.0

package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/exec"
)

// recordedCall is one invocation the fake runner saw.
type recordedCall struct {
	name  string
	args  []string
	stdin string
	env   []string
}

// fakeRunner scripts stdout/stderr per call and records everything.
type fakeRunner struct {
	calls    []recordedCall
	stdout   []string
	stderr   []string
	failFrom int // call index (1-based) at which to fail; 0 = never
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin []byte, env []string) ([]byte, error) {
	f.calls = append(f.calls, recordedCall{name: name, args: args, stdin: string(stdin), env: env})
	i := len(f.calls) - 1
	var stderr string
	if i < len(f.stderr) {
		stderr = f.stderr[i]
	}
	if f.failFrom > 0 && len(f.calls) >= f.failFrom {
		return nil, &exec.ExitError{Name: name, Args: args, Stderr: stderr, Code: 17}
	}
	var stdout string
	if i < len(f.stdout) {
		stdout = f.stdout[i]
	}
	return []byte(stdout), nil
}

func TestEnsureLocalRunsComposeUpWithStdin(t *testing.T) {
	runner := &fakeRunner{}
	ex := exec.NewExecutor(runner)

	changed, err := ex.Ensure(context.Background(), exec.Host{ID: "gw-1", Address: "10.0.0.11"}, "looming", "name: looming\n")
	require.NoError(t, err)
	assert.True(t, changed)

	require.Len(t, runner.calls, 1)
	call := runner.calls[0]
	assert.Equal(t, "docker", call.name)
	assert.Equal(t, []string{"compose", "-p", "looming", "-f", "-", "up", "-d"}, call.args)
	assert.Equal(t, "name: looming\n", call.stdin, "compose YAML ships on stdin; nothing touches the remote fs")
	assert.Empty(t, call.env, "local backend injects no DOCKER_HOST")
}

func TestEnsureSSHInjectsDockerHostEnv(t *testing.T) {
	runner := &fakeRunner{}
	ex := exec.NewExecutor(runner)

	_, err := ex.Ensure(context.Background(),
		exec.Host{ID: "app-1", Address: "10.0.0.12", SSHUser: "root"}, "looming", "services: {}\n")
	require.NoError(t, err)

	require.Len(t, runner.calls, 1)
	assert.Equal(t, []string{"DOCKER_HOST=ssh://root@10.0.0.12"}, runner.calls[0].env,
		"ssh backend rides docker's native SSH transport — no daemon port exposed")
}

func TestEnsureFailureSurfacesStderr(t *testing.T) {
	runner := &fakeRunner{stderr: []string{"no configuration file provided: not found"}}
	runner.failFrom = 1
	ex := exec.NewExecutor(runner)

	_, err := ex.Ensure(context.Background(), exec.Host{ID: "gw-1", Address: "10.0.0.11"}, "looming", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `converge host "gw-1"`)
	assert.Contains(t, err.Error(), "no configuration file provided",
		"the operator needs compose's stderr to see why the converge failed")

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 17, exitErr.Code)
	assert.Equal(t, "no configuration file provided: not found", exitErr.Stderr)
}

func TestExitErrorTruncatesLongStderr(t *testing.T) {
	long := strings.Repeat("x", 5000)
	msg := (&exec.ExitError{Name: "docker", Args: []string{"compose"}, Stderr: long, Code: 1}).Error()
	assert.Contains(t, msg, "exit 1")
	assert.Contains(t, msg, "…")
	assert.Less(t, len(msg), len(long), "stderr is tail-truncated, not inlined whole")
	assert.True(t, strings.HasSuffix(msg, strings.Repeat("x", 100)))
}

// TestLocalRunnerRoundTrip exercises LocalRunner against the test
// binary itself (the os/exec helper-process pattern): the helper
// echoes its stdin, proves env extension, and exits with a chosen
// code — no docker, no network, no clock.
func TestLocalRunnerRoundTrip(t *testing.T) {
	runner := exec.LocalRunner{}
	out, err := runner.Run(context.Background(), os.Args[0],
		[]string{"-test.run=TestLocalRunnerHelperProcess", "--", "echo"},
		[]byte("hello stdin"),
		[]string{"GO_WANT_HELPER_PROCESS=1", "LOOMING_HELPER_MARK=seen"})
	require.NoError(t, err)
	assert.Equal(t, "stdin=hello stdin;env=seen\n", string(out))
}

func TestLocalRunnerSurfacesExitCodeAndStderr(t *testing.T) {
	runner := exec.LocalRunner{}
	_, err := runner.Run(context.Background(), os.Args[0],
		[]string{"-test.run=TestLocalRunnerHelperProcess", "--", "fail"},
		nil, []string{"GO_WANT_HELPER_PROCESS=1"})
	require.Error(t, err)
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 3, exitErr.Code)
	assert.Equal(t, "boom\n", exitErr.Stderr)
}

// TestLocalRunnerHelperProcess is not a test — it is the child program
// LocalRunner tests invoke (selected by -test.run).
func TestLocalRunnerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	stdin, _ := io.ReadAll(os.Stdin)
	switch args[0] {
	case "echo":
		fmt.Printf("stdin=%s;env=%s\n", stdin, os.Getenv("LOOMING_HELPER_MARK"))
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "boom\n")
		os.Exit(3)
	}
	os.Exit(2)
}
