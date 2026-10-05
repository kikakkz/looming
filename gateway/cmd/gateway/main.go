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

// run wires and serves; separated from main for the smoke test.
func run(log *slog.Logger) error {
	upstreamURL := os.Getenv("GATEWAY_UPSTREAM")
	if upstreamURL == "" {
		return errConfig("GATEWAY_UPSTREAM")
	}
	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		return err
	}
	listen := os.Getenv("GATEWAY_LISTEN")
	if listen == "" {
		listen = ":8080"
	}

	upstreamAuth := os.Getenv("GATEWAY_UPSTREAM_AUTH")
	if upstreamAuth != "" && upstream.Scheme != "https" {
		return &configError{name: "GATEWAY_UPSTREAM must be https when GATEWAY_UPSTREAM_AUTH is set"}
	}
	engine := defaultengine.NewWithUpstream(upstream, upstreamAuth)
	queue := adapter.NewChanQueue(queueSize())
	go adapter.DrainInteractions(context.Background(), queue, log)

	identityURL := os.Getenv("GATEWAY_IDENTITY_URL")
	if identityURL == "" {
		return errConfig("GATEWAY_IDENTITY_URL")
	}
	identityToken := os.Getenv("GATEWAY_IDENTITY_TOKEN")
	if identityToken == "" {
		return errConfig("GATEWAY_IDENTITY_TOKEN")
	}
	watchTimeout, err := envDuration("GATEWAY_IDENTITY_WATCH_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}
	staleAfter, err := envDuration("GATEWAY_IDENTITY_SYNC_STALE_AFTER", 2*watchTimeout)
	if err != nil {
		return err
	}

	// The identity projection: the syncer keeps the KeyCache a faithful
	// copy of the authority's feed; the authenticator authorizes off
	// the cache and falls back to the origin validate endpoint. Both
	// ride the same signal context so process shutdown stops the sync
	// loop (the server keeps its existing semantics).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	identityClient := controladapter.NewIdentityClient(identityURL, identityToken, watchTimeout)
	keyCache := controlapp.NewKeyCache(time.Now)
	syncer := controlapp.NewSyncer(identityClient, keyCache,
		controldomain.Backoffer{Base: time.Second, Cap: 30 * time.Second},
		staleAfter, time.Now, log)
	go func() {
		if err := syncer.Run(ctx); err != nil {
			log.Error("identity syncer stopped", "err", err)
		}
	}()
	authn := controladapter.NewIdentityAuthenticator(keyCache, identityClient, positiveTTL(), time.Now, log)

	front := frontapp.NewFront(
		authn,
		adapter.StaticAllowlist{ModelsBySubject: parseAllowlists(os.Getenv("GATEWAY_ALLOWLISTS"))},
		frontdomain.NewChain(), // no chain links yet: jev/laya land with the risk slice
		engine,
		adapter.BodyModelExtractor{},
		log,
		frontapp.WithRecording(queue, adapter.LogMeter{}, transcriptCap()),
	)

	log.Info("gateway listening", "addr", listen, "upstream", upstreamURL, "identity", identityURL)
	server := &http.Server{
		Addr:              listen,
		Handler:           front,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming responses: no global write deadline
		IdleTimeout:       60 * time.Second,
	}
	return server.ListenAndServe()
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
