// SPDX-License-Identifier: Apache-2.0
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	keydomain "github.com/kikakkz/looming/identity/internal/key/domain"
)

// Handler exposes the key use cases over HTTP. Gateway precedent: HTTP
// handling lives in the app layer; cmd wires the mux and the auth
// middleware (Bearer session on self routes, admin role on admin
// routes). Methods are mounted per route by cmd.
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxBodyBytes = 1 << 20

type issueRequest struct {
	Name string `json:"name"`
}

// IssueSelf handles POST /v1/self/keys. The raw secret appears exactly
// here (and in later reveal responses); the UI defaults to the masked
// view with a copy button.
func (h *Handler) IssueSelf(w http.ResponseWriter, r *http.Request) {
	var req issueRequest
	if !decode(w, r, &req) {
		return
	}
	k, secret, err := h.svc.Issue(r.Context(), actorFrom(r.Context()), req.Name)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":   k.ID,
		"key":  string(secret),
		"name": k.Name,
	})
}

// ListSelf handles GET /v1/self/keys: masked views only.
func (h *Handler) ListSelf(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context(), actorFrom(r.Context()))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": keyViews(items)})
}

// GetSelf handles GET /v1/self/keys/{id}: one masked view, owner-scoped.
func (h *Handler) GetSelf(w http.ResponseWriter, r *http.Request) {
	k, err := h.svc.Get(r.Context(), actorFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keyView(k))
}

// RevealSelf handles POST /v1/self/keys/{id}/reveal. Owner or admin
// (the self route admits any validated session; the handler dispatches
// on the session's roles) — and repeatable.
func (h *Handler) RevealSelf(w http.ResponseWriter, r *http.Request) {
	var secret keydomain.KeySecret
	var err error
	if info, ok := authnport.TokenInfoFrom(r.Context()); ok && hasRole(info.Roles, "admin") {
		secret, err = h.svc.AdminReveal(r.Context(), r.PathValue("id"))
	} else {
		secret, err = h.svc.Reveal(r.Context(), actorFrom(r.Context()), r.PathValue("id"))
	}
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": string(secret)})
}

// RevokeSelf handles DELETE /v1/self/keys/{id}: one-way, owner-scoped.
// Already-revoked maps to 409 already_revoked.
func (h *Handler) RevokeSelf(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Revoke(r.Context(), actorFrom(r.Context()), r.PathValue("id")); err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListForPrincipalAdmin handles GET /v1/admin/principals/{id}/keys.
func (h *Handler) ListForPrincipalAdmin(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.AdminList(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": keyViews(items)})
}

// RevokeAdmin handles DELETE /v1/admin/keys/{id}: one-way, any key.
func (h *Handler) RevokeAdmin(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.AdminRevoke(r.Context(), r.PathValue("id")); err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// keyView is the masked northbound shape: the hash and the sealed blob
// never leave the service (credential hygiene); reveal is the only
// plaintext exit.
func keyView(k *keydomain.LoomingKey) map[string]any {
	return map[string]any{
		"id":         k.ID,
		"name":       k.Name,
		"prefix":     k.Prefix,
		"last4":      k.Last4,
		"status":     string(k.Status),
		"created_at": k.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func keyViews(items []*keydomain.LoomingKey) []any {
	out := make([]any, 0, len(items))
	for _, k := range items {
		out = append(out, keyView(k))
	}
	return out
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer func() { _ = r.Body.Close() }()
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return false
	}
	return true
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

// writeUseCaseError maps domain and use-case errors onto the v1 error
// contract. Safe validation errors keep their detail; everything else
// returns the code only — backend internals stay server-side (CWE-209)
// and are logged instead.
func writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, safe := classifyUseCaseError(err)
	message := code
	if safe {
		message = err.Error()
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "key request failed", "err", err)
	}
	writeError(w, status, code, message)
}

func classifyUseCaseError(err error) (status int, code string, safe bool) {
	switch {
	case errors.Is(err, ErrPrincipalNotFound):
		return http.StatusNotFound, "not_found", false
	case errors.Is(err, ErrPrincipalInactive):
		return http.StatusForbidden, "principal_inactive", false
	case errors.Is(err, ErrIssueLimit):
		return http.StatusTooManyRequests, "issue_limit", false
	case errors.Is(err, ErrProvisionFailed):
		return http.StatusBadGateway, "provision_failed", false
	case errors.Is(err, keydomain.ErrNotFound):
		return http.StatusNotFound, "not_found", false
	case errors.Is(err, keydomain.ErrAlreadyRevoked):
		return http.StatusConflict, "already_revoked", false
	case errors.Is(err, keydomain.ErrConflict):
		return http.StatusConflict, "conflict", false
	case errors.Is(err, keydomain.ErrInvalidName):
		return http.StatusBadRequest, "invalid_request", true
	default:
		return http.StatusInternalServerError, "internal", false
	}
}
