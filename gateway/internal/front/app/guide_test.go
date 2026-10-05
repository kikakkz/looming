// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	frontapp "github.com/kikakkz/looming/gateway/internal/front/app"
	"github.com/kikakkz/looming/gateway/internal/front/port"
)

var guideTestNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeGuideSource scripts the topology origin.
type fakeGuideSource struct {
	guide port.Guide
	err   error
	calls int
}

func (f *fakeGuideSource) FetchGuide(context.Context) (port.Guide, error) {
	f.calls++
	if f.err != nil {
		return port.Guide{}, f.err
	}
	return f.guide, nil
}

func publicGuide() port.Guide {
	return port.Guide{
		ClusterName:    "prod cluster",
		AccessPublic:   true,
		CLIDownloadURL: "https://releases.example.com/looming",
		IdentityURL:    "http://10.0.0.12:8081",
		GatewayURL:     "http://10.0.0.11:8080",
		Steps:          []string{"Download the Looming CLI.", "Register.", "Create an API key.", "Configure your agents."},
		RegisterHint:   "No account yet? Ask your cluster admin for an invite token — registration needs one.",
	}
}

func newGuideHandler(src port.GuideSource, ttl time.Duration) *frontapp.GuideHandler {
	return frontapp.NewGuideHandler(src, ttl, func() time.Time { return guideTestNow },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func getGuide(h *frontapp.GuideHandler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuidePageServesFullOnboardingPage(t *testing.T) {
	src := &fakeGuideSource{guide: publicGuide()}
	h := newGuideHandler(src, time.Minute)

	rec := getGuide(h)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html", "the page is html")

	// Heading, steps, endpoints, download link, register example,
	// config skeleton — the page contract (topology-l1 §3 journey 3).
	assert.Contains(t, body, "prod cluster")
	assert.Contains(t, body, "<ol>")
	assert.Contains(t, body, "Download the Looming CLI.")
	assert.Contains(t, body, "Create an API key.")
	assert.Contains(t, body, "10.0.0.12:8081")
	assert.Contains(t, body, "10.0.0.11:8080")
	assert.Contains(t, body, "https://releases.example.com/looming")
	assert.Contains(t, body, "curl -sS -X POST http://10.0.0.12:8081/v1/self/register")
	assert.Contains(t, body, "LOOMING_GATEWAY_URL=http://10.0.0.11:8080")
	assert.Contains(t, body, "Ask your cluster admin")
	assert.NotContains(t, body, "<script", "zero-JS page")
}

func TestGuidePageTTLCache(t *testing.T) {
	src := &fakeGuideSource{guide: publicGuide()}
	h := newGuideHandler(src, time.Minute)

	first := getGuide(h)
	require.Equal(t, http.StatusOK, first.Code)

	second := getGuide(h)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, 1, src.calls, "a fresh cache serves without refetching")
}

func TestGuidePageRefetchesAfterTTL(t *testing.T) {
	src := &fakeGuideSource{guide: publicGuide()}
	now := guideTestNow
	h := frontapp.NewGuideHandler(src, time.Minute, func() time.Time { return now },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	getGuide(h)
	now = now.Add(2 * time.Minute)
	getGuide(h)
	assert.Equal(t, 2, src.calls, "expired TTL refetches")
}

func TestGuidePageServesLastKnownGoodOnFetchFailure(t *testing.T) {
	src := &fakeGuideSource{guide: publicGuide()}
	now := guideTestNow
	h := frontapp.NewGuideHandler(src, time.Minute, func() time.Time { return now },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Equal(t, http.StatusOK, getGuide(h).Code)

	// The origin goes dark after the TTL: the warm cache still serves.
	now = now.Add(2 * time.Minute)
	src.err = errors.New("topologyd down")
	rec := getGuide(h)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "prod cluster")
	assert.Equal(t, 2, src.calls)
}

func TestGuidePageColdFailureIs503(t *testing.T) {
	src := &fakeGuideSource{err: errors.New("topologyd down")}
	h := newGuideHandler(src, time.Minute)

	rec := getGuide(h)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "retry")
}

func TestGuidePageAccessPrivateIs404(t *testing.T) {
	g := publicGuide()
	g.AccessPublic = false
	src := &fakeGuideSource{guide: g}
	now := guideTestNow
	h := frontapp.NewGuideHandler(src, time.Minute, func() time.Time { return now },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Cold: the fetch happens, the page stays hidden.
	require.Equal(t, http.StatusNotFound, getGuide(h).Code)

	// Warm: cached or not, access.public=false never serves.
	now = now.Add(2 * time.Minute)
	src.guide, src.err = port.Guide{}, nil
	require.Equal(t, http.StatusNotFound, getGuide(h).Code,
		"the visibility flag overrides the cache")
}

func TestGuidePageEscapesOperatorSuppliedData(t *testing.T) {
	g := publicGuide()
	g.ClusterName = `<script>alert("x")</script>`
	g.CLIDownloadURL = "https://example.com/?q=<b>"
	src := &fakeGuideSource{guide: g}
	h := newGuideHandler(src, time.Minute)

	rec := getGuide(h)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, "<script>alert", "guide data is operator-supplied: everything escapes")
	assert.Contains(t, body, "&lt;script&gt;")
	assert.Contains(t, body, "&lt;b&gt;")
}

func TestGuideNotConfiguredStub(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	frontapp.GuideNotConfigured(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "guide is not configured")
}

// slowGuideSource blocks each fetch so overlapping requests are
// observable.
type slowGuideSource struct {
	guide port.Guide
	calls int
	delay time.Duration
}

func (f *slowGuideSource) FetchGuide(context.Context) (port.Guide, error) {
	f.calls++
	time.Sleep(f.delay)
	return f.guide, nil
}

// TestGuidePageStaleRefreshSingleFlight: a stale cache must not make
// concurrent requests queue behind the origin — one fetch runs, the
// rest take the stale copy or the settled outcome (review finding on
// #122).
func TestGuidePageStaleRefreshSingleFlight(t *testing.T) {
	src := &slowGuideSource{guide: publicGuide(), delay: 150 * time.Millisecond}
	now := guideTestNow
	h := frontapp.NewGuideHandler(src, time.Minute, func() time.Time { return now },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Equal(t, http.StatusOK, getGuide(h).Code)
	now = now.Add(2 * time.Minute) // stale: the next requests trigger a refresh

	const concurrent = 8
	recs := make(chan *httptest.ResponseRecorder, concurrent)
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs <- getGuide(h)
		}()
	}
	wg.Wait()
	close(recs)

	for rec := range recs {
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "prod cluster")
	}
	assert.Equal(t, 2, src.calls, "one priming fetch + exactly one refresh, whatever the concurrency")
}

// slowFailSource blocks each fetch, then fails — an origin that is
// slow AND down.
type slowFailSource struct {
	calls int
	delay time.Duration
	err   error
}

func (f *slowFailSource) FetchGuide(context.Context) (port.Guide, error) {
	f.calls++
	time.Sleep(f.delay)
	return port.Guide{}, f.err
}

// TestGuidePageColdRefreshSharesFailure: with nothing cached, waiters
// on an in-flight fetch share its failure instead of each issuing a
// new one.
func TestGuidePageColdRefreshSharesFailure(t *testing.T) {
	src := &slowFailSource{delay: 200 * time.Millisecond, err: errors.New("origin down")}
	h := newGuideHandler(src, time.Minute)

	const concurrent = 6
	var wg sync.WaitGroup
	codes := make(chan int, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- getGuide(h).Code
		}()
	}
	wg.Wait()
	close(codes)

	for code := range codes {
		assert.Equal(t, http.StatusServiceUnavailable, code)
	}
	assert.Equal(t, 1, src.calls, "one fetch serves every cold request")
}
