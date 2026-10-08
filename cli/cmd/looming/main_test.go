// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRootHelpListsBothFaces pins the single-binary-two-faces contract
// (AD-37): the user face and the admin face share one root command.
func TestRootHelpListsBothFaces(t *testing.T) {
	var out bytes.Buffer
	cmd := root()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	for _, want := range []string{
		// user face (cli-l1 §4)
		"onboard", "configure", "usage",
		// admin face (CLI-1, #108)
		"apply", "token", "guide", "join",
	} {
		assert.Contains(t, out.String(), want)
	}
}
