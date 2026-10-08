// SPDX-License-Identifier: Apache-2.0

package dbcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCmd(t *testing.T, cmdArgs ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewToken(&out)
	root.AddCommand(NewGuide(&out))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(cmdArgs)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

const testConfig = `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts: [{id: only, address: 10.0.0.11}]
placements:
  - {component: gateway-front, host: only, ports: {http: 8080}, config: {upstream: "http://10.0.0.13:4000"}}
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestGuideShowValidatesInputsBeforeAnyDB(t *testing.T) {
	t.Run("missing config surfaces config error", func(t *testing.T) {
		_, err := runCmd(t, "guide", "show", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config:")
	})
	t.Run("missing database url names the env", func(t *testing.T) {
		t.Setenv(databaseURLEnv, "")
		path := writeConfig(t, testConfig)
		_, err := runCmd(t, "guide", "show", "--config", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), databaseURLEnv)
	})
}

func TestTokenCreateValidatesRoleBeforeTouchingTheDatabase(t *testing.T) {
	_, err := runCmd(t, "create", "--role", "superuser")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role")

	_, err = runCmd(t, "create", "--role", "engine", "--ttl", "not-a-duration")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ttl")
}

func TestTokenCommandsRequireDatabaseURL(t *testing.T) {
	t.Setenv(databaseURLEnv, "")
	for _, args := range [][]string{
		{"create", "--role", "engine"},
		{"list"},
		{"revoke", "abcd"},
	} {
		_, err := runCmd(t, args...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), databaseURLEnv)
	}
}

func TestTokenHelpListsSubcommands(t *testing.T) {
	out, err := runCmd(t, "--help")
	require.NoError(t, err)
	for _, want := range []string{"create", "list", "revoke", "--database-url"} {
		assert.Contains(t, out, want)
	}
}

func TestTokenTreeShape(t *testing.T) {
	var out bytes.Buffer
	token := NewToken(&out)
	names := []string{}
	for _, c := range token.Commands() {
		names = append(names, c.Name())
	}
	assert.ElementsMatch(t, []string{"create", "list", "revoke"}, names)
	assert.NotNil(t, token.PersistentFlags().Lookup("database-url"))

	var guideOut bytes.Buffer
	guide := NewGuide(&guideOut)
	assert.Equal(t, "guide", guide.Name())
	assert.NotNil(t, guide.PersistentFlags().Lookup("config"))
	sub := []string{}
	for _, c := range guide.Commands() {
		sub = append(sub, c.Name())
	}
	assert.Equal(t, []string{"show"}, sub)
}

func TestResolveDatabaseURLFlagWinsOverEnv(t *testing.T) {
	t.Setenv(databaseURLEnv, "postgres://env")
	assert.Equal(t, "postgres://flag", resolveDatabaseURL("postgres://flag"))
	assert.Equal(t, "postgres://env", resolveDatabaseURL(""))
	t.Setenv(databaseURLEnv, "")
	assert.Equal(t, "", resolveDatabaseURL(""))
}
