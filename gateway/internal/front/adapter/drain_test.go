// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/kikakkz/looming/gateway/internal/front/domain"
)

func TestDrainInteractionsConsumesUntilContextEnds(t *testing.T) {
	q := NewChanQueue(4)
	log := slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		DrainInteractions(ctx, q, log)
		close(done)
	}()
	q.Enqueue(context.Background(), domain.InteractionBody{Subject: "ker", Model: "gpt-5"})

	deadline := time.Now().Add(2 * time.Second)
	for len(q.C) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("queue was not drained in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(q.C) != 0 {
		t.Fatal("item must be consumed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drainer must exit on context cancel")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
