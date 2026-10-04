// SPDX-License-Identifier: Apache-2.0

// Command gateway runs the slice-1 gateway: front layer served on a
// port, default engine proxying to a configured upstream, static
// config stand-ins for the identity projection (cache wiring lands
// with the control-plane slice).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return &configError{name: "GATEWAY_UPSTREAM must be http or https"}
	}
	if upstream.Host == "" {
		return &configError{name: "GATEWAY_UPSTREAM must include a host"}
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
	front := frontapp.NewFront(
		adapter.StaticAuthenticator{Keys: parseKeys(os.Getenv("GATEWAY_KEYS"))},
		adapter.StaticAllowlist{ModelsBySubject: parseAllowlists(os.Getenv("GATEWAY_ALLOWLISTS"))},
		frontdomain.NewChain(), // no chain links yet: jev/laya land with the risk slice
		engine,
		adapter.BodyModelExtractor{},
		log,
		frontapp.WithRecording(queue, adapter.LogMeter{}, transcriptCap()),
	)

	log.Info("gateway listening", "addr", listen, "upstream", upstreamURL)
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

func errConfig(name string) error {
	return &configError{name: name}
}

type configError struct{ name string }

func (c *configError) Error() string {
	return "missing required config: " + c.name
}

// parseKeys parses "key=subject,key=subject".
func parseKeys(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	return out
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
