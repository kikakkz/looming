// SPDX-License-Identifier: Apache-2.0

package app

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	domain "github.com/kikakkz/looming/platform/go/guidedomain"
)

// Handler exposes the guide's read path over HTTP — topology-l1 §6's
// GuidePublisher surface (GET /v1/internal/guide), served by topologyd
// for the gateway's cached fetch. The response is the snapshot JSON
// verbatim, including access_public: the flag is not secret — the
// gateway decides the page's visibility from it.
type Handler struct {
	svc   *Service
	token string
}

// NewHandler wires the handler. serviceToken is the expected
// "Authorization: Bearer" value; empty means the deployment never
// configured TOPOLOGY_SERVICE_TOKEN and the endpoint answers 503 (the
// guide cannot be published without its guard).
func NewHandler(svc *Service, serviceToken string) *Handler {
	return &Handler{svc: svc, token: serviceToken}
}

// Guide handles GET /v1/internal/guide: service-token guard, then the
// persisted snapshot. The house v1 error envelope answers every
// non-200 (identity precedent).
func (h *Handler) Guide(w http.ResponseWriter, r *http.Request) {
	if h.token == "" {
		writeGuideError(w, http.StatusServiceUnavailable, "guide_unavailable", "TOPOLOGY_SERVICE_TOKEN is not configured")
		return
	}
	if !h.authorized(r.Header.Get("Authorization")) {
		writeGuideError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		return
	}

	guide, err := h.svc.Current(r.Context())
	switch {
	case errors.Is(err, domain.ErrNoGuide):
		writeGuideError(w, http.StatusNotFound, "not_found", "no guide rendered yet — run `looming apply`")
	case err != nil:
		slog.ErrorContext(r.Context(), "guide read failed", "err", err)
		writeGuideError(w, http.StatusInternalServerError, "internal", "internal")
	default:
		writeGuideJSON(w, http.StatusOK, guide.Snapshot)
	}
}

// authorized compares the presented bearer token in constant time —
// the token is a shared secret and the endpoint is unauthenticated
// otherwise.
func (h *Handler) authorized(header string) bool {
	presented, found := strings.CutPrefix(header, "Bearer ")
	if !found || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.token)) == 1
}

func writeGuideJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeGuideError(w http.ResponseWriter, status int, code, message string) {
	writeGuideJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
