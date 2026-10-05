// SPDX-License-Identifier: Apache-2.0
package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kikakkz/looming/identity/internal/gatewayfeed/domain"
)

// Handler exposes the gateway-facing read side over HTTP: the feed
// snapshot with its blocking watch, and key validation. cmd mounts
// every route behind RequireServiceToken — this API is the internal
// data-plane contract (identity-l1 §5), never a user surface.
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxBodyBytes = 1 << 20

// Feed handles GET /v1/gateway/feed. Without watch it returns the
// snapshot immediately; with watch=1&since_rev=N it holds in the
// blocking-query shape until the revision advances or the watch
// timeout elapses, then returns the current snapshot regardless. The
// response is always a full projection — never a delta.
func (h *Handler) Feed(w http.ResponseWriter, r *http.Request) {
	sinceRev, err := parseSinceRev(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "since_rev must be a non-negative integer")
		return
	}
	var snap *domain.Snapshot
	if r.URL.Query().Get("watch") == "1" {
		snap, err = h.svc.Watch(r.Context(), sinceRev)
	} else {
		snap, err = h.svc.Snapshot(r.Context())
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "gateway feed snapshot failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "feed_unavailable", "feed_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, snapshotView(snap))
}

type validateRequest struct {
	Key string `json:"key"`
}

// Validate handles POST /v1/gateway/keys/validate: hash the raw key
// and resolve it. Revoked, non-active-owned, and unknown keys all map
// to 404 — the gateway turns that into an auth failure (fail closed),
// and no existence signal crosses the boundary.
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var req validateRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	hash := sha256.Sum256([]byte(req.Key))
	principalID, err := h.svc.Validate(r.Context(), hash[:])
	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "not_found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "gateway key validate failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "validate_unavailable", "validate_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principal_id": principalID,
		"status":       domain.KeyActive,
	})
}

// RequireServiceToken guards the gateway-facing routes with the
// bundle-internal service token (identity-l1 §5: mTLS/OAuth is the
// phase-2 upgrade, #109-#112). A missing or wrong bearer fails closed
// with 401; the token never appears in logs or error bodies.
func RequireServiceToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !subtleEqual(strings.TrimSpace(raw), token) {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

func parseSinceRev(r *http.Request) (uint64, error) {
	raw := r.URL.Query().Get("since_rev")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// snapshotView renders the full projection: base64 key hashes, both
// projections, and the stamping revision.
func snapshotView(snap *domain.Snapshot) map[string]any {
	keys := make([]any, 0, len(snap.Keys))
	for _, k := range snap.Keys {
		keys = append(keys, map[string]any{
			"hash":         base64.StdEncoding.EncodeToString(k.Hash),
			"principal_id": k.PrincipalID,
			"status":       k.Status,
		})
	}
	principals := make([]any, 0, len(snap.Principals))
	for _, p := range snap.Principals {
		principals = append(principals, map[string]any{
			"id":     p.ID,
			"status": p.Status,
		})
	}
	return map[string]any{
		"rev":        snap.Rev,
		"keys":       keys,
		"principals": principals,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

// subtleEqual compares two strings without early exit, so response
// timing carries no prefix-length signal (the token check runs on an
// unauthenticated surface).
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
