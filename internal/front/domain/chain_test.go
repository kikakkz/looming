package domain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubLink struct {
	v   Verdict
	err error
}

func (s stubLink) Evaluate(_ context.Context, _ LinkContext, _ *http.Request) (Verdict, error) {
	return s.v, s.err
}

type countingLink struct{ n *bool }

func (c *countingLink) Evaluate(_ context.Context, _ LinkContext, _ *http.Request) (Verdict, error) {
	*c.n = true
	return VerdictPass, nil
}

func TestChainPassesWhenAllPass(t *testing.T) {
	c := NewChain(stubLink{v: VerdictPass}, stubLink{v: VerdictPass})
	if err := c.Run(context.Background(), LinkContext{}, httptest.NewRequest("POST", "/v1/chat/completions", nil)); err != nil {
		t.Fatalf("want pass, got %v", err)
	}
}

func TestChainBlocksOnFirstBlock(t *testing.T) {
	called := false
	c := NewChain(stubLink{v: VerdictBlock}, &countingLink{n: &called})
	if err := c.Run(context.Background(), LinkContext{}, httptest.NewRequest("POST", "/", nil)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
	if called {
		t.Fatal("later links must not run after a block")
	}
}

func TestChainErrorBlocks(t *testing.T) {
	sentinel := errors.New("jev eval fault")
	c := NewChain(stubLink{v: VerdictPass, err: sentinel})
	if err := c.Run(context.Background(), LinkContext{}, httptest.NewRequest("POST", "/", nil)); !errors.Is(err, sentinel) {
		t.Fatalf("evaluation error must propagate and block, got %v", err)
	}
}
