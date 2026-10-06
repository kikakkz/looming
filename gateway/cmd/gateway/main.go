// SPDX-License-Identifier: Apache-2.0

// Command gateway runs the slice-B gateway: front layer served on a
// port, default engine proxying to a configured upstream, and the
// control plane's identity projection (KeyCache + Syncer over the
// identity feed) backing authentication. The static GATEWAY_KEYS
// stand-in is retired — identity is the key authority.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	controladapter "github.com/kikakkz/looming/gateway/internal/control/adapter"
	controlapp "github.com/kikakkz/looming/gateway/internal/control/app"
	controldomain "github.com/kikakkz/looming/gateway/internal/control/domain"
	defaultengine "github.com/kikakkz/looming/gateway/internal/engine/default"
	"github.com/kikakkz/looming/gateway/internal/front/adapter"
	frontapp "github.com/kikakkz/looming/gateway/internal/front/app"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("gateway exited", "err", err)
		os.Exit(1)
	}
}

// loadUpstream resolves the engine upstream from the environment and
// enforces the credential-safety gate: a static upstream auth value
// rides the wire to the upstream, so plain HTTP would hand it to any
// passive observer (CWE-319) — forbidden unless the operator opts out
// explicitly for a trusted network, the GATEWAY_IDENTITY_INSECURE
// precedent. (The bundle e2e's loopback deployment sets the opt-out:
// its upstream is a host-reachable fake engine, never a wire crossing.)
func loadUpstream() (*url.URL, string, error) {
	upstreamURL := os.Getenv("GATEWAY_UPSTREAM")
	if upstreamURL == "" {
		return nil, "", errConfig("GATEWAY_UPSTREAM")
	}
	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, "", err
	}
	upstreamAuth := os.Getenv("GATEWAY_UPSTREAM_AUTH")
	if upstreamAuth != "" && upstream.Scheme != "https" && os.Getenv("GATEWAY_UPSTREAM_INSECURE") != "1" {
		return nil, "", &configError{name: "GATEWAY_UPSTREAM must be https when GATEWAY_UPSTREAM_AUTH is set (trusted-network plain HTTP opts out with GATEWAY_UPSTREAM_INSECURE=1)"}
	}
	return upstream, upstreamAuth, nil
}

// run wires and serves; separated from main for the smoke test.
func run(log *slog.Logger) error {
	upstream, upstreamAuth, err := loadUpstream()
	if err != nil {
		return err
	}
	listen := os.Getenv("GATEWAY_LISTEN")
	if listen == "" {
		listen = ":8080"
	}

	engine := defaultengine.NewWithUpstream(upstream, upstreamAuth)
	queue := adapter.NewChanQueue(queueSize())
	go adapter.DrainInteractions(context.Background(), queue, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	authn, identityURL, err := wireIdentity(ctx, log)
	if err != nil {
		return err
	}

	front := frontapp.NewFront(
		authn,
		adapter.StaticAllowlist{ModelsBySubject: parseAllowlists(os.Getenv("GATEWAY_ALLOWLISTS"))},
		frontdomain.NewChain(), // no chain links yet: jev/laya land with the risk slice
		engine,
		adapter.BodyModelExtractor{},
		log,
		frontapp.WithRecording(queue, adapter.LogMeter{}, transcriptCap()),
	)

	// The public onboarding page rides the same listener at GET /
	// (topology-l1 §7). GATEWAY_TOPOLOGY_URL is optional: without it the
	// route answers a 404 stub. The token is required once the URL is
	// set (fail fast) and rides the env_file secret channel like every
	// other credential — never the rendered compose environment.
	// The phase-1 renderer derives an http:// link on the trusted
	// network, and the guide endpoint carries zero credentials by
	// invariant, so no https-only gate stands here (unlike the identity
	// link, which carries key material).
	guide, err := guideHandler(log)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", guide)
	mux.Handle("/", front)

	log.Info("gateway listening", "addr", listen, "upstream", upstream.String(), "identity", identityURL)
	server := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming responses: no global write deadline
		IdleTimeout:       60 * time.Second,
	}
	return server.ListenAndServe()
}

// wireIdentity builds the identity projection the front authenticates
// against: the syncer keeps the KeyCache a faithful copy of the
// authority's feed; the authenticator authorizes off the cache and
// falls back to the origin validate endpoint. Both ride the caller's
// signal context so process shutdown stops the sync loop. The identity
// URL comes back for the boot log.
func wireIdentity(ctx context.Context, log *slog.Logger) (*controladapter.IdentityAuthenticator, string, error) {
	identityURL := os.Getenv("GATEWAY_IDENTITY_URL")
	if identityURL == "" {
		return nil, "", errConfig("GATEWAY_IDENTITY_URL")
	}
	identityTarget, err := url.Parse(identityURL)
	if err != nil {
		return nil, "", err
	}
	// The service token and raw validate keys ride this link; plain
	// HTTP needs an explicit trusted-network opt-out (the bundle's
	// loopback deployments set it, anything crossed-hosts must not).
	if identityTarget.Scheme != "https" && os.Getenv("GATEWAY_IDENTITY_INSECURE") != "1" {
		return nil, "", &configError{name: "GATEWAY_IDENTITY_URL must be https unless GATEWAY_IDENTITY_INSECURE=1 (trusted network)"}
	}
	identityToken := os.Getenv("GATEWAY_IDENTITY_TOKEN")
	if identityToken == "" {
		return nil, "", errConfig("GATEWAY_IDENTITY_TOKEN")
	}
	watchTimeout, err := envDuration("GATEWAY_IDENTITY_WATCH_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, "", err
	}
	staleAfter, err := envDuration("GATEWAY_IDENTITY_SYNC_STALE_AFTER", 2*watchTimeout)
	if err != nil {
		return nil, "", err
	}

	identityClient := controladapter.NewIdentityClient(identityURL, identityToken, watchTimeout)
	keyCache := controlapp.NewKeyCache(time.Now)
	syncer := controlapp.NewSyncer(identityClient, keyCache,
		controldomain.Backoffer{Base: time.Second, Cap: 30 * time.Second},
		staleAfter, time.Now, log)
	go func() {
		if runErr := syncer.Run(ctx); runErr != nil {
			log.Error("identity syncer stopped", "err", runErr)
		}
	}()
	return controladapter.NewIdentityAuthenticator(keyCache, identityClient, positiveTTL(), time.Now, log, syncer.Healthy), identityURL, nil
}

// guideHandler builds the GET / handler from the environment:
// GATEWAY_TOPOLOGY_URL (absent → not-configured stub), the required
// GATEWAY_TOPOLOGY_TOKEN, and the GATEWAY_GUIDE_TTL freshness window.
func guideHandler(log *slog.Logger) (http.Handler, error) {
	topologyURL := os.Getenv("GATEWAY_TOPOLOGY_URL")
	if topologyURL == "" {
		return http.HandlerFunc(frontapp.GuideNotConfigured), nil
	}
	token := os.Getenv("GATEWAY_TOPOLOGY_TOKEN")
	if token == "" {
		// Fail fast: a guide route pointing at an origin the gateway
		// cannot authenticate to would 503 in a loop.
		return nil, errConfig("GATEWAY_TOPOLOGY_TOKEN is required when GATEWAY_TOPOLOGY_URL is set")
	}
	ttl, err := envDuration("GATEWAY_GUIDE_TTL", frontapp.DefaultGuideTTL)
	if err != nil {
		return nil, err
	}
	client := adapter.NewGuideClient(topologyURL, token)
	return frontapp.NewGuideHandler(client, ttl, time.Now, log), nil
}

// positiveTTL is how long an origin-confirmed key authorizes without
// revalidation; it composes with the feed watch for the revocation
// bound (watch latency + TTL).
func positiveTTL() time.Duration { return 30 * time.Second }

// envDuration parses an optional duration env var, defaulting when
// unset and failing fast on a malformed value (a silent default would
// misconfigure the sync cadence).
func envDuration(name string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, &configError{name: name + " must be a duration like 30s"}
	}
	return d, nil
}

func errConfig(name string) error {
	return &configError{name: name}
}

type configError struct{ name string }

func (c *configError) Error() string {
	return "missing required config: " + c.name
}

// parseAllowlists parses "subject=model|model,subject=model".
func parseAllowlists(s string) map[string][]string {
	out := map[string][]string{}
	for _, group := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(group), "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[kv[0]] = strings.Split(kv[1], "|")
	}
	return out
}

func queueSize() int { return 128 }

func transcriptCap() int { return 1 << 20 }
