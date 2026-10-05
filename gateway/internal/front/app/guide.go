// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// GuideHandler serves GET / — the cluster's public onboarding page
// (topology-l1 §3 journey 3): unauthenticated, zero-JS, rendered from
// the topology-fetched guide. Freshness policy lives here: an
// in-memory last-known-good cache with a TTL — guide data changes at
// apply-time frequency, so no watch or caching proxy stands behind it
// (T3 scope guard).
type GuideHandler struct {
	source port.GuideSource
	ttl    time.Duration
	clock  func() time.Time
	log    *slog.Logger

	mu        sync.Mutex
	cached    port.Guide
	fetchedAt time.Time
	hasCache  bool
}

// NewGuideHandler wires the handler over a guide source. ttl <= 0
// falls back to DefaultGuideTTL.
func NewGuideHandler(source port.GuideSource, ttl time.Duration, clock func() time.Time, log *slog.Logger) *GuideHandler {
	if ttl <= 0 {
		ttl = DefaultGuideTTL
	}
	if log == nil {
		log = slog.Default()
	}
	return &GuideHandler{source: source, ttl: ttl, clock: clock, log: log}
}

// ServeHTTP renders the page. Fetch failure with a warm cache serves
// the last known copy; a cold failure is a 503 with a retry hint;
// access.public=false is a plain 404 — the flag never leaks through a
// distinguishable body.
func (h *GuideHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	guide, err := h.current(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "guide fetch failed", "err", err)
		http.Error(w, "the onboarding guide is temporarily unavailable — retry shortly", http.StatusServiceUnavailable)
		return
	}
	if !guide.AccessPublic {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := guidePageTemplate.Execute(w, newPageView(guide)); err != nil {
		// The template writes as it evaluates; a late failure can leave a
		// truncated page. Log it — there is no clean recovery mid-stream.
		h.log.ErrorContext(r.Context(), "guide page render failed", "err", err)
	}
}

// current returns the freshest guide the handler can produce: the
// cache while fresh, a refetch when stale, last-known-good when the
// refetch fails, and the fetch error when nothing was ever cached.
func (h *GuideHandler) current(ctx context.Context) (port.Guide, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.hasCache && h.clock().Sub(h.fetchedAt) < h.ttl {
		return h.cached, nil
	}
	guide, err := h.source.FetchGuide(ctx)
	if err != nil {
		if h.hasCache {
			h.log.WarnContext(ctx, "guide fetch failed; serving last known copy", "err", err)
			return h.cached, nil
		}
		return port.Guide{}, err
	}
	h.cached, h.fetchedAt, h.hasCache = guide, h.clock(), true
	return guide, nil
}

// DefaultGuideTTL is how long a fetched guide serves without a
// refetch: guide data changes at apply-time frequency, so a minute of
// staleness is invisible (overridable with GATEWAY_GUIDE_TTL).
const DefaultGuideTTL = time.Minute

// pageView maps the guide onto the template's shape: the curl register
// example and the config skeleton compose their lines from the guide
// facts here so the template stays dumb text with holes.
type pageView struct {
	port.Guide
	RegisterCurl string
	ConfigBlock  string
}

// newPageView builds the template view: the register example targets
// identity's self-register endpoint (the house register path); the
// config skeleton is the env-style block an operator pastes into an
// agent's environment.
func newPageView(g port.Guide) pageView {
	return pageView{
		Guide: g,
		RegisterCurl: "curl -sS -X POST " + g.IdentityURL + "/v1/self/register " +
			"-H 'Content-Type: application/json' " +
			`-d '{"username":"<choose>","password":"<choose>","email":"<you>","invite_token":"<from your admin>"}'`,
		ConfigBlock: "LOOMING_GATEWAY_URL=" + g.GatewayURL + "\nLOOMING_API_KEY=<paste your key>",
	}
}

// GuideNotConfigured answers the guide route when the deployment
// carries no GATEWAY_TOPOLOGY_URL: the route exists (a bare 404 would
// look like a routing bug), and the body says why.
func GuideNotConfigured(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "the guide is not configured on this gateway", http.StatusNotFound)
}

// guidePageTemplate is the zero-JS onboarding page. Every value is
// operator-supplied guide data, so everything routes through
// html/template's escaping — no safe-HTML holes.
var guidePageTemplate = template.Must(template.New("guide").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.ClusterName}} — Looming onboarding</title>
<style>
body { font-family: system-ui, sans-serif; margin: 2rem auto; max-width: 46rem; padding: 0 1rem; color: #1a1a1a; }
h1 { margin-bottom: 0.25rem; }
.hint { color: #555; }
code, pre { background: #f4f4f4; }
pre { padding: 0.75rem; overflow-x: auto; }
table { border-collapse: collapse; }
th, td { text-align: left; padding: 0.25rem 0.75rem 0.25rem 0; }
</style>
</head>
<body>
<main>
<h1>{{.ClusterName}}</h1>
<p class="hint">{{.RegisterHint}}</p>
<h2>Get started</h2>
<ol>
{{range .Steps}}<li>{{.}}</li>
{{end}}</ol>
<h2>Endpoints</h2>
<table>
<tbody>
<tr><th scope="row">Identity</th><td><code>{{.IdentityURL}}</code></td></tr>
<tr><th scope="row">Gateway</th><td><code>{{.GatewayURL}}</code></td></tr>
</tbody>
</table>
<p>Download the CLI: <a href="{{.CLIDownloadURL}}">{{.CLIDownloadURL}}</a></p>
<h2>Register</h2>
<pre class="code">{{.RegisterCurl}}</pre>
<h2>Configure your agents</h2>
<pre class="code">{{.ConfigBlock}}</pre>
</main>
</body>
</html>
`))
