// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kikakkz/looming/gateway/internal/front/domain"
)

func TestBodyModelExtractorParsesAndRestores(t *testing.T) {
	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	m, err := BodyModelExtractor{}.Extract(req)
	if err != nil {
		t.Fatal(err)
	}
	if m != "gpt-5" {
		t.Fatalf("model = %q", m)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != body {
		t.Fatalf("body must be restored byte-identical for upstream; got %q", restored)
	}
	if req.GetBody == nil {
		t.Fatal("GetBody must be set so the engine can re-read")
	}
}

func TestBodyModelExtractorRejectsMalformed(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{not json"))
	extractor := BodyModelExtractor{}
	if _, err := extractor.Extract(req); err == nil {
		t.Fatal("malformed body must error (400 upstream of the pipeline)")
	}
}

func TestChanQueueEnqueueNonBlocking(t *testing.T) {
	q := NewChanQueue(1)
	q.Enqueue(context.Background(), domain.InteractionBody{Subject: "a"})
	q.Enqueue(context.Background(), domain.InteractionBody{Subject: "b"}) // buffer full: loud drop, no block
	got := <-q.C
	if got.Subject != "a" {
		t.Fatalf("first enqueued must drain first, got %q", got.Subject)
	}
}

func TestLogMeterRecordsALine(t *testing.T) {
	// The slice-1 sink is a structured log line; the contract under
	// test is that Record never panics and accepts the shape.
	LogMeter{}.Record(context.Background(), domain.MeterRecord{Subject: "ker", Model: "gpt-5", Outcome: "completed"})
}

func TestBodyModelExtractorCapsOversize(t *testing.T) {
	big := strings.Repeat("x", 64)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(big))
	extractor := BodyModelExtractor{MaxBodyBytes: 8}
	if _, err := extractor.Extract(req); err == nil {
		t.Fatalf("oversize body must error, got %v", err)
	}
}
