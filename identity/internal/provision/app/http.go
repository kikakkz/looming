// SPDX-License-Identifier: Apache-2.0
package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kikakkz/looming/identity/internal/provision/domain"
)

// Handler exposes the IdentityMap inspect surface over HTTP. cmd mounts
// it behind the admin middleware (identity-l1 §6: IdentityMap inspect).
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const (
	defaultInspectLimit = 100
	maxInspectLimit     = 500
)

// InspectAdmin handles GET /v1/admin/identitymap?principal={id}. The
// response is reference-only by construction — the aggregate never
// holds the credential value, and the view names its columns
// explicitly, so a sealed blob can never leak northbound.
func (h *Handler) InspectAdmin(w http.ResponseWriter, r *http.Request) {
	principalID := r.URL.Query().Get("principal")
	if principalID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "principal query parameter is required")
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items, total, err := h.svc.Inspect(r.Context(), principalID, limit, offset)
	if err != nil {
		slog.ErrorContext(r.Context(), "identity map inspect failed", "principal_id", principalID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	views := make([]any, 0, len(items))
	for _, m := range items {
		views = append(views, mapView(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views, "total": total})
}

// --- helpers ---

// mapView renders the reference-only inspect shape. CredentialEnc is
// deliberately absent: the sealed blob stays service-internal.
func mapView(m *domain.IdentityMap) map[string]any {
	return map[string]any{
		"key_id":         m.KeyID,
		"engine":         m.Engine,
		"credential_ref": m.CredentialRef,
		"status":         string(m.Status),
		"created_at":     m.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":     m.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func parsePage(r *http.Request) (limit, offset int, err error) {
	limit = defaultInspectLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 || n > maxInspectLimit {
			return 0, 0, errors.New("limit must be an integer in (0, 500]")
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
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
