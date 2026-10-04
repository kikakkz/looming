// SPDX-License-Identifier: Apache-2.0

package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCapturingWriterPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 1024)
	if _, err := cw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("passthrough broken: %q", rec.Body.String())
	}
	body, truncated := cw.Transcript()
	if truncated || string(body) != "hello" {
		t.Fatalf("transcript wrong: %q truncated=%v", body, truncated)
	}
}

func TestCapturingWriterTruncatesAtCap(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 3)
	if _, err := cw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	body, truncated := cw.Transcript()
	if !truncated || string(body) != "hel" {
		t.Fatalf("want truncated 'hel', got %q truncated=%v", body, truncated)
	}
	if rec.Body.String() != "hello" {
		t.Fatal("passthrough must not be truncated")
	}
}

func TestCapturingWriterRecordsStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 8)
	cw.WriteHeader(201)
	if _, err := cw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if cw.Status() != 201 {
		t.Fatalf("status not recorded: %d", cw.Status())
	}
}

func TestCapturingWriterStatusDefaultsToOK(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 8)
	if cw.Status() != 200 {
		t.Fatalf("implicit status must read 200, got %d", cw.Status())
	}
}

func TestCapturingWriterUnwraps(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 8)
	if cw.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap must return the underlying writer")
	}
}

func TestCapturingWriterFlushPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := WrapResponse(rec, 8)
	cw.Flush()
	if rec.Flushed != true {
		t.Fatal("flush must reach the underlying writer")
	}
}

var _ io.Writer = (*CapturingWriter)(nil)
