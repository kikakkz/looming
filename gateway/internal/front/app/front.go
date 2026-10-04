// SPDX-License-Identifier: Apache-2.0
package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// ModelExtractor pulls the requested model id from the request. Real
// body parsing lands with the forwarding slice; the seam exists now so
// the pipeline is testable end to end.
type ModelExtractor func(*http.Request) string

// Front is the front layer pipeline (gateway-l1 §3 journey 1). The
// order is architecture: Authenticate → ModelAllowed → chain → forward.
type Front struct {
	authn     port.Authenticator
	allowlist port.SubjectAllowlist
	chain     *frontdomain.Chain
	engine    engineplane.Forwarder
	model     ModelExtractor
	log       *slog.Logger
}

func NewFront(a port.Authenticator, al port.SubjectAllowlist, c *frontdomain.Chain, e engineplane.Forwarder, m ModelExtractor, log *slog.Logger) *Front {
	if log == nil {
		log = slog.Default()
	}
	return &Front{authn: a, allowlist: al, chain: c, engine: e, model: m, log: log}
}

// ServeHTTP runs the pipeline. Every denial is a DecisionEvent-shaped
// log line southbound; the client-facing body stays generic (AD-32 Q6:
// northbound 401s do not distinguish not-found from not-provisioned).
func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := bearerToken(r)
	subject, err := f.authn.Authenticate(r.Context(), key)
	if err != nil {
		f.deny(w, r, "authn", key, "")
		return
	}
	model := f.model(r)
	models, err := f.allowlist.Models(r.Context(), subject)
	if err != nil {
		// Lookup failure is an infrastructure fault, not an
		// authorization denial — logging it as one would poison the
		// audit stream and hide the real problem (storm Q4).
		f.log.ErrorContext(r.Context(), "allowlist lookup failed",
			"subject", subject, "err", err)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if frontdomain.ModelAllowed(models, model) != frontdomain.Allow {
		f.denyStatus(w, r, http.StatusForbidden, "model_permission", key, subject)
		return
	}
	lc := frontdomain.LinkContext{Subject: subject, Model: model}
	if err := f.chain.Run(r.Context(), lc, r); err != nil {
		f.deny(w, r, "interception", key, subject)
		return
	}
	// Forwarding mechanics belong to the engine slot (AD-32).
	if err := f.engine.Forward(r.Context(), w, r); err != nil {
		f.log.ErrorContext(r.Context(), "engine forward failed", "subject", subject, "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
}

func (f *Front) deny(w http.ResponseWriter, r *http.Request, layer, key, subject string) {
	f.denyStatus(w, r, http.StatusUnauthorized, layer, key, subject)
}

// denyStatus separates authn failures (401 — unauthenticated, per the
// AD-32 northbound sameness rule) from authenticated authorisation
// denials (403 — model permission).
func (f *Front) denyStatus(w http.ResponseWriter, r *http.Request, status int, layer, key, subject string) {
	f.log.InfoContext(r.Context(), "request denied",
		"layer", layer, "status", status, "key_sha", hashKey(key), "subject", subject)
	http.Error(w, http.StatusText(status), status)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	return strings.TrimPrefix(h, "Bearer ")
}

func hashKey(key string) string {
	// Log identifiers only, never raw keys (credential hygiene).
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}
