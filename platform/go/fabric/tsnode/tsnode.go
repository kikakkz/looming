// SPDX-License-Identifier: Apache-2.0

package tsnode

import (
	"errors"
	"fmt"
	"net"

	"tailscale.com/tsnet"

	"github.com/kikakkz/looming/platform/go/fabric"
)

var (
	ErrNotStarted = errors.New("tsnode: node is not started")
	ErrClosed     = errors.New("tsnode: node is closed")
)

type Node struct {
	cfg    fabric.NodeConfig
	srv    *tsnet.Server
	closed bool
}

func New(cfg fabric.NodeConfig) *Node {
	return &Node{cfg: cfg}
}

func (n *Node) Start() error {
	if err := n.cfg.Validate(); err != nil {
		return err
	}
	srv := &tsnet.Server{
		ControlURL: n.cfg.ControlURL,
		AuthKey:    n.cfg.AuthKey,
		Dir:        n.cfg.StateDir,
		Hostname:   n.cfg.Hostname,
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("tsnode: starting node: %w", err)
	}
	n.srv = srv
	n.closed = false
	return nil
}

func (n *Node) Listen(network, addr string) (net.Listener, error) {
	if n.closed {
		return nil, ErrClosed
	}
	if n.srv == nil {
		return nil, ErrNotStarted
	}
	return n.srv.Listen(network, addr)
}

func (n *Node) Close() error {
	if n.closed {
		return nil
	}
	n.closed = true
	if n.srv == nil {
		return nil
	}
	return n.srv.Close()
}
