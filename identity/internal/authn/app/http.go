// SPDX-License-Identifier: Apache-2.0
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
)

// Handler exposes the authn use cases over HTTP; cmd mounts the routes.
type Handler struct {
	login *LoginService
}

// NewHandler wires the handler.
func NewHandler(login *LoginService) *Handler {
	return &Handler{login: login}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login handles POST /v1/self/login. Token validation for the Bearer
// routes is a direct Provider call consumed by the cmd middleware —
// thin enough to need no use case of its own.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	raw, expiresAt, err := h.login.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredential) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
			return
		}
		slog.ErrorContext(r.Context(), "login failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      raw,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
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
