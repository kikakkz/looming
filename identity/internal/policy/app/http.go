// SPDX-License-Identifier: Apache-2.0
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	"github.com/kikakkz/looming/identity/internal/policy/domain"
)

// Handler exposes the policy use cases over HTTP; cmd mounts the routes
// behind the admin middleware.
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type policyRequest struct {
	Mode string `json:"mode"`
}

// GetAdmin handles GET /v1/admin/policy.
func (h *Handler) GetAdmin(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "policy get failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": string(p.Mode)})
}

// SetAdmin handles PUT /v1/admin/policy.
func (h *Handler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var req policyRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	// The audit column names the acting admin; the middleware has
	// already validated the session and stored its claims.
	actor := "unknown"
	if info, ok := authnport.TokenInfoFrom(r.Context()); ok {
		actor = info.PrincipalID
	}
	p, err := h.svc.Set(r.Context(), domain.Mode(req.Mode), actor)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidMode) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "policy set failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": string(p.Mode)})
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
