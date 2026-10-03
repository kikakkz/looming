package domain

import (
	"context"
	"errors"
	"net/http"
)

// Verdict is one chain link's decision. Like Decision, the zero value
// denies: a link that cannot evaluate must not pass traffic.
type Verdict bool

const (
	VerdictBlock Verdict = false
	VerdictPass  Verdict = true
)

// LinkContext carries what a chain link may inspect. Only fields the
// architecture intends to expose — links never see engine internals.
type LinkContext struct {
	Subject string // authenticated Looming subject
	Model   string // requested model id
}

// ChainLink is the interception-chain SPI (the Traefik-plugin-shaped
// contract): an ordered middleware with fail-closed semantics.
// jev/laya risk-control links are one implementation class.
type ChainLink interface {
	// Evaluate inspects the request and returns a verdict. An error
	// blocks: evaluation failures never open the gate.
	Evaluate(ctx context.Context, lc LinkContext, r *http.Request) (Verdict, error)
}

// Chain runs links in order. First block or error stops the chain and
// denies; every link must pass.
type Chain struct {
	links []ChainLink
}

func NewChain(links ...ChainLink) *Chain {
	return &Chain{links: links}
}

// ErrBlocked marks a chain denial; the caller maps it to a 4xx.
var ErrBlocked = errors.New("dp: interception chain blocked the request")

// Run evaluates all links in order. Returns nil only when every link
// passes.
func (c *Chain) Run(ctx context.Context, lc LinkContext, r *http.Request) error {
	for _, link := range c.links {
		v, err := link.Evaluate(ctx, lc, r)
		if err != nil {
			return err
		}
		if v != VerdictPass {
			return ErrBlocked
		}
	}
	return nil
}
