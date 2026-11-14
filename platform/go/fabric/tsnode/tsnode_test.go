// SPDX-License-Identifier: Apache-2.0

package tsnode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/platform/go/fabric"
	"github.com/kikakkz/looming/platform/go/fabric/tsnode"
)

func validConfig(t *testing.T) fabric.NodeConfig {
	return fabric.NodeConfig{
		ControlURL: "https://headscale.example.com",
		AuthKey:    "tskey-auth-k0123456789",
		StateDir:   t.TempDir(),
		Hostname:   "gateway-1",
	}
}

func TestStartValidatesConfigBeforeStarting(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fabric.NodeConfig)
	}{
		{"missing control url", func(c *fabric.NodeConfig) { c.ControlURL = "" }},
		{"missing auth key", func(c *fabric.NodeConfig) { c.AuthKey = "" }},
		{"missing state dir", func(c *fabric.NodeConfig) { c.StateDir = "" }},
		{"missing hostname", func(c *fabric.NodeConfig) { c.Hostname = "" }},
		{"bad hostname", func(c *fabric.NodeConfig) { c.Hostname = "Bad_Hostname" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(&cfg)
			node := tsnode.New(cfg)
			err := node.Start()
			require.Error(t, err)
			_, lerr := node.Listen("tcp", ":0")
			assert.ErrorIs(t, lerr, tsnode.ErrNotStarted)
			assert.NoError(t, node.Close())
		})
	}
}

func TestListenAfterCloseRejected(t *testing.T) {
	node := tsnode.New(validConfig(t))
	require.NoError(t, node.Close())
	assert.NoError(t, node.Close())
	_, err := node.Listen("tcp", ":0")
	assert.ErrorIs(t, err, tsnode.ErrClosed)
}
