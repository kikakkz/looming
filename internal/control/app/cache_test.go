package app

import (
	"context"
	"testing"
)

func TestCacheAppliesMonotonicRevisions(t *testing.T) {
	c := NewModelAllowlistCache(context.Background())
	defer c.Close()
	c.Upsert(1, "ker", []string{"gpt-5"})
	snap := c.Get()
	if snap.Rev != 1 || len(snap.V["ker"]) != 1 {
		t.Fatalf("want rev 1 with ker's models, got %+v", snap)
	}
}

func TestCacheDropsStaleRevisions(t *testing.T) {
	c := NewModelAllowlistCache(context.Background())
	defer c.Close()
	c.Upsert(5, "ker", []string{"gpt-5"})
	c.Upsert(3, "ker", []string{"claude-3"}) // stale
	snap := c.Get()
	if snap.Rev != 5 || snap.V["ker"][0] != "gpt-5" {
		t.Fatalf("stale update must be suppressed, got %+v", snap)
	}
}

func TestWriterIsolatedFromMutatingReaders(t *testing.T) {
	// A reader violating the no-mutate contract must not corrupt the
	// writer's state: clone-on-store is the isolation boundary.
	c := NewModelAllowlistCache(context.Background())
	defer c.Close()
	c.Upsert(1, "ker", []string{"gpt-5"})
	snap := c.Get()
	snap.V["ker"][0] = "mutated"
	c.Upsert(2, "ker", []string{"gpt-5"})
	fresh := c.Get()
	if fresh.Rev != 2 || fresh.V["ker"][0] != "gpt-5" {
		t.Fatalf("writer corrupted by reader mutation: %+v", fresh)
	}
}
