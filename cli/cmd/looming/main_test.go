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

// TestTopologyGroupCarriesAdviseAndFacts pins the slice-1.3 command
// tree: one `topology` group (advise's parent since the round-4
// review) with exactly the advise and facts children — the facts
// subtree must never register a second topology group.
func TestTopologyGroupCarriesAdviseAndFacts(t *testing.T) {
	var out bytes.Buffer
	cmd := root()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"topology", "--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	usage := out.String()
	assert.Contains(t, usage, "advise")
	assert.Contains(t, usage, "facts")

	out.Reset()
	cmd = root()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"topology", "facts", "--help"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.Contains(t, out.String(), "pull")
}
