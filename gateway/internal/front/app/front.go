// SPDX-License-Identifier: Apache-2.0
package app

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kikakkz/looming/gateway/internal/engineplane"
	frontdomain "github.com/kikakkz/looming/gateway/internal/front/domain"
	"github.com/kikakkz/looming/gateway/internal/front/port"
)

// Front is the front layer pipeline (gateway-l1 §3 journey 1). The
// order is architecture: Authenticate → ModelAllowed → chain → forward.
type Front struct {
	authn         port.Authenticator
	allowlist     port.SubjectAllowlist
	chain         *frontdomain.Chain
	engine        engineplane.Forwarder
	model         port.ModelExtractor
	queue         port.InteractionQueue
	meter         port.MeterSink
	transcriptCap int
	log           *slog.Logger
}

// FrontOption tunes the pipeline; NewFront keeps the slice-0 signature
// workable while the recorder hooks arrive.
type FrontOption func(*Front)

// WithRecording attaches the interaction queue and meter sink and sets
// the transcript capture cap (0 disables capture).
func WithRecording(q port.InteractionQueue, m port.MeterSink, transcriptCap int) FrontOption {
	return func(f *Front) {
		f.queue = q
		f.meter = m
		f.transcriptCap = transcriptCap
	}
}

func NewFront(a port.Authenticator, al port.SubjectAllowlist, c *frontdomain.Chain, e engineplane.Forwarder, m port.ModelExtractor, log *slog.Logger, opts ...FrontOption) *Front {
	if log == nil {
		log = slog.Default()
	}
	f := &Front{authn: a, allowlist: al, chain: c, engine: e, model: m, log: log}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// ServeHTTP runs the pipeline. Every denial is a DecisionEvent-shaped
// log line southbound; the client-facing body stays generic (AD-32 Q6:
// northbound 401s do not distinguish not-found from not-provisioned).
func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := bearerToken(r)
	id, err := f.authn.Authenticate(r.Context(), key)
	if err != nil {
		f.deny(w, r, "authn", key, "")
		return
	}
	// The LoomingKey dies at the authn boundary: nothing downstream
	// (engine slot, recorder, transcripts) may ever see it — the only
	// southbound auth material is the explicit engine credential
	// (gateway-l1 §6: Looming key northbound only, engine credential
	// southbound only). The engine keeps its own strip-or-replace as
	// defense in depth for direct engine callers.
	r.Header.Del("Authorization")
	subject := id.Subject
	model, err := f.model.Extract(r)
	if err != nil {
		f.log.InfoContext(r.Context(), "request denied",
			"layer", "malformed_body", "status", http.StatusBadRequest, "key_sha", hashKey(key))
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	// The extractor restored the body (port contract). Read it fully
	// for forwarding; the transcript keeps a capped copy only — the
	// upstream must receive every byte regardless of the cap.
	var reqBody []byte
	if f.queue != nil && f.transcriptCap > 0 {
		full, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(full))
		r.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(full)), nil
		}
		if len(full) > f.transcriptCap {
			reqBody = append([]byte(nil), full[:f.transcriptCap]...)
		} else {
			reqBody = full
		}
	}
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
	// Forwarding mechanics belong to the engine slot (AD-32). The
	// recorder tees the response for the async interaction emit
	// (storm Q3): capture bounded, never block the data plane. Only the
	// engine credential crosses here — the LoomingKey stays in the
	// authn step (gateway-l1 §6: engine credential southbound only).
	cw := w
	if f.queue != nil && f.transcriptCap > 0 {
		cw = WrapResponse(w, f.transcriptCap)
	}
	if err := f.engine.Forward(r.Context(), cw, r, id.EngineCredential); err != nil {
		f.log.ErrorContext(r.Context(), "engine forward failed", "subject", subject, "err", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	f.emit(subject, model, reqBody, r, cw)
}

// emit assembles the InteractionBody and meters the completed call.
// Denials never reach here — metering is usage-only (AD-32 #5).
func (f *Front) emit(subject, model string, reqBody []byte, r *http.Request, w http.ResponseWriter) {
	if f.queue == nil && f.meter == nil {
		return
	}
	var respBody []byte
	var truncated bool
	if cw, ok := w.(*CapturingWriter); ok {
		respBody, truncated = cw.Transcript()
	}
	if f.queue != nil {
		f.queue.Enqueue(r.Context(), frontdomain.InteractionBody{
			Subject:      subject,
			Model:        model,
			RequestBody:  reqBody,
			ResponseBody: respBody,
			Truncated:    truncated,
		})
	}
	if f.meter != nil {
		f.meter.Record(r.Context(), frontdomain.MeterRecord{
			Subject: subject, Model: model, Outcome: "completed",
		})
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
