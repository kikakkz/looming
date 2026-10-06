// SPDX-License-Identifier: Apache-2.0
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	quotadomain "github.com/kikakkz/looming/identity/internal/quota/domain"
)

// Handler exposes the quota use cases over HTTP. cmd mounts the admin
// routes behind the admin middleware and the self route behind the
// session middleware (identity-l1 §6).
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxBodyBytes = 1 << 20

// quotaRequest decodes the admin set body. Every field is a pointer:
// an omitted amount would otherwise default to 0 — a valid but BLOCKING
// quota — so absence must be a 400, never a silent block.
type quotaRequest struct {
	Amount     *int64  `json:"amount"`
	Unit       *string `json:"unit"`
	WindowDays *int    `json:"window_days"`
}

// SetAdmin handles PUT /v1/admin/principals/{id}/quota. The response is
// always 200 on a persisted authority change; per-entry projection
// failures ride along in budget_update_failures (identity-l1 §4: the
// engine budget is the quota's projection and may lag visibly).
func (h *Handler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	var req quotaRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Amount == nil || req.Unit == nil || req.WindowDays == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "amount, unit and window_days are all required")
		return
	}
	actor := "unknown"
	if info, ok := authnport.TokenInfoFrom(r.Context()); ok {
		actor = info.PrincipalID
	}
	q, failures, err := h.svc.Set(r.Context(), r.PathValue("id"), *req.Amount, *req.Unit, *req.WindowDays, actor)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	view := quotaView(q)
	if failures == nil {
		failures = []BudgetFailure{}
	}
	view["budget_update_failures"] = failures
	writeJSON(w, http.StatusOK, view)
}

// GetAdmin handles GET /v1/admin/principals/{id}/quota: 200 with the
// quota, or 404 no_quota when the principal carries no quota row (the
// unlimited default).
func (h *Handler) GetAdmin(w http.ResponseWriter, r *http.Request) {
	q, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quotaView(q))
}

// GetSelf handles GET /v1/self/quota for the session's principal; same
// 404 no_quota shape as the admin route.
func (h *Handler) GetSelf(w http.ResponseWriter, r *http.Request) {
	q, err := h.svc.Get(r.Context(), actorFrom(r.Context()))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quotaView(q))
}

// --- helpers ---

func quotaView(q *quotadomain.Quota) map[string]any {
	return map[string]any{
		"principal_id": q.PrincipalID,
		"amount":       q.Amount,
		"unit":         string(q.Unit),
		"window_days":  q.WindowDays,
		"updated_by":   q.UpdatedBy,
		"updated_at":   q.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func actorFrom(ctx context.Context) string {
	info, ok := authnport.TokenInfoFrom(ctx)
	if !ok {
		return ""
	}
	return info.PrincipalID
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

// writeUseCaseError maps quota use-case errors onto the v1 error
// contract. Validation errors keep their safe detail; backend
// internals stay server-side (CWE-209) and are logged instead.
func writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, safe := classifyUseCaseError(err)
	message := code
	if safe {
		message = err.Error()
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "quota request failed", "err", err)
	}
	writeError(w, status, code, message)
}

func classifyUseCaseError(err error) (status int, code string, safe bool) {
	switch {
	case errors.Is(err, ErrPrincipalNotFound):
		return http.StatusNotFound, "not_found", false
	case errors.Is(err, quotadomain.ErrNoQuota):
		return http.StatusNotFound, "no_quota", false
	case errors.Is(err, quotadomain.ErrInvalidAmount),
		errors.Is(err, quotadomain.ErrInvalidUnit),
		errors.Is(err, quotadomain.ErrInvalidWindow):
		return http.StatusBadRequest, "invalid_request", true
	default:
		return http.StatusInternalServerError, "internal", false
	}
}
