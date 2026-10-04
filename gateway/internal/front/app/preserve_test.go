// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	defaultengine "github.com/kikakkz/looming/gateway/internal/engine/default"
	"github.com/kikakkz/looming/gateway/internal/front/adapter"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
	"go.uber.org/goleak"
)

// sliceOneFront builds the full pipeline against a real upstream and
// returns the front plus the interaction queue for assertions.
func sliceOneFront(t *testing.T, upstream *url.URL) (*Front, *adapter.ChanQueue, *meterRecorder) {
	t.Helper()
	queue := adapter.NewChanQueue(8)
	meters := &meterRecorder{mu: &sync.Mutex{}, meters: &[]frontdomain.MeterRecord{}}
	front := NewFront(
		adapter.StaticAuthenticator{Keys: map[string]string{"good-key": "ker"}},
		adapter.StaticAllowlist{ModelsBySubject: map[string][]string{"ker": {"gpt-5"}}},
		frontdomain.NewChain(),
		defaultengine.NewWithUpstream(upstream),
		adapter.BodyModelExtractor{},
		nil,
		WithRecording(queue, meters, 1<<20),
	)
	return front, queue, meters
}

type meterRecorder struct {
	mu     *sync.Mutex
	meters *[]frontdomain.MeterRecord
}

func (m meterRecorder) Record(_ context.Context, rec frontdomain.MeterRecord) {
	if m.mu == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	*m.meters = append(*m.meters, rec)
}

func TestPayloadPreservingEndToEnd(t *testing.T) {
	defer goleak.VerifyNone(t)
	const wantReq = `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`
	const wantResp = "data: {\"id\":\"1\"}\n\ndata: [DONE]\n\n"

	var upstreamGot []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamGot, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, wantResp)
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	front, queue, meters := sliceOneFront(t, upstreamURL)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(wantReq))
	req.Header.Set("Authorization", "Bearer good-key")
	rec := httptest.NewRecorder()
	front.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if string(upstreamGot) != wantReq {
		t.Fatalf("request body NOT preserved upstream:\nwant %q\ngot  %q", wantReq, upstreamGot)
	}
	if rec.Body.String() != wantResp {
		t.Fatalf("response body NOT preserved:\nwant %q\ngot  %q", wantResp, rec.Body.String())
	}
	interaction := <-queue.C
	if string(interaction.RequestBody) != wantReq {
		t.Fatalf("interaction request transcript mismatch: %q", interaction.RequestBody)
	}
	if string(interaction.ResponseBody) != wantResp {
		t.Fatalf("interaction response transcript mismatch: %q", interaction.ResponseBody)
	}
	if interaction.Truncated {
		t.Fatal("1MB cap must not truncate this response")
	}
	meters.mu.Lock()
	defer meters.mu.Unlock()
	if len(*meters.meters) != 1 || (*meters.meters)[0].Outcome != "completed" {
		t.Fatalf("completed call must meter exactly once: %+v", *meters.meters)
	}
}

func TestDenialsAreNotMetered(t *testing.T) {
	defer goleak.VerifyNone(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	front, _, meters := sliceOneFront(t, upstreamURL)

	bad := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	bad.Header.Set("Authorization", "Bearer wrong-key")
	front.ServeHTTP(httptest.NewRecorder(), bad)

	denied := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"claude-3"}`))
	denied.Header.Set("Authorization", "Bearer good-key")
	front.ServeHTTP(httptest.NewRecorder(), denied)

	meters.mu.Lock()
	defer meters.mu.Unlock()
	if len(*meters.meters) != 0 {
		t.Fatalf("denials must never be metered (AD-32 #5): %+v", *meters.meters)
	}
}

func TestSSEDisconnectLeavesNoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		for i := 0; i < 200; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(time.Millisecond)
		}
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL)
	front, _, _ := sliceOneFront(t, upstreamURL)

	body := `{"model":"gpt-5","messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-key")
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)

	// recorder without full buffering so the disconnect propagates
	rec := &cancelRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	done := make(chan struct{})
	go func() {
		front.ServeHTTP(rec, req)
		close(done)
	}()
	// let a few chunks through, then drop the client
	time.Sleep(5 * time.Millisecond)
	cancel()
	<-done
	_ = rec
}

// cancelRecorder cancels the client context on first write — simulating
// a client that hangs up as soon as data starts flowing.
type cancelRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelRecorder) Write(p []byte) (int, error) {
	c.once.Do(func() {
		go func() {
			time.Sleep(time.Millisecond)
			c.cancel()
		}()
	})
	return c.ResponseRecorder.Write(p)
}
