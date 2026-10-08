// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	hostdomain "github.com/kikakkz/looming/platform/go/hostdomain"
	domain "github.com/kikakkz/looming/platform/go/joindomain"
)

// Handler exposes the join use cases over HTTP — topology-l1 §7's
// join/rejoin surface, served by topologyd. Methods are mounted per
// route by cmd; the error envelope is the house v1 shape
// {"error":{"code","message"}} (identity precedent).
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxBodyBytes = 1 << 20

// hostAuthScheme is the re-join authorization scheme: the host's
// persistent credential presented as "Authorization: Host <id>:<raw>".
const hostAuthScheme = "Host "

type joinRequest struct {
	Token string `json:"token"`
	Host  struct {
		ID      string   `json:"id"`
		Address string   `json:"address"`
		Labels  []string `json:"labels"`
	} `json:"host"`
}

type rejoinRequest struct {
	Address *string  `json:"address"`
	Labels  []string `json:"labels"`
}

// Join handles POST /v1/join: consume a one-time token, register the
// host, mint its persistent credential, answer with the cluster hint.
func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := h.svc.Consume(r.Context(), req.Token, HostInput{
		ID:      req.Host.ID,
		Address: req.Host.Address,
		Labels:  req.Host.Labels,
	})
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"host_id":    res.Host.ID,
		"credential": res.Credential,
		"cluster":    map[string]any{"access": res.Endpoint},
	})
}

// Rejoin handles POST /v1/join/rejoin: authenticate the host by its
// persistent credential and refresh its address/labels.
func (h *Handler) Rejoin(w http.ResponseWriter, r *http.Request) {
	hostID, credential, ok := parseHostAuth(w, r)
	if !ok {
		return
	}
	var req rejoinRequest
	if !decode(w, r, &req) {
		return
	}
	host, err := h.svc.Rejoin(r.Context(), hostID, credential, req.Address, req.Labels)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host_id": host.ID,
		"address": host.Address,
		"labels":  host.RoleLabels,
	})
}

// parseHostAuth extracts and validates the "Host <id>:<credential>"
// authorization value. Malformed headers fail as unauthenticated
// (401) — they never reach the service.
func parseHostAuth(w http.ResponseWriter, r *http.Request) (hostID, credential string, ok bool) {
	raw, found := strings.CutPrefix(r.Header.Get("Authorization"), hostAuthScheme)
	if !found {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		return "", "", false
	}
	hostID, credential, found = strings.Cut(raw, ":")
	if !found || hostID == "" || credential == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		return "", "", false
	}
	return hostID, credential, true
}

// --- envelope helpers (house shape) ---

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

// writeUseCaseError maps service-layer errors onto the v1 contract.
// Client-correctable detail (expiry, conflicts with their recovery
// hints) reaches the operator; credential internals never do.
func writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, safe := classifyUseCaseError(err)
	message := code
	if safe {
		message = err.Error()
	}
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "join request failed", "err", err)
	}
	writeError(w, status, code, message)
}

func classifyUseCaseError(err error) (status int, code string, safe bool) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized, "unauthenticated", false
	case errors.Is(err, domain.ErrTokenNotFound):
		return http.StatusForbidden, "token_invalid", false
	case errors.Is(err, domain.ErrTokenExpired):
		return http.StatusForbidden, "token_expired", true
	case errors.Is(err, domain.ErrTokenUsed):
		return http.StatusConflict, "token_used", false
	case errors.Is(err, hostdomain.ErrAddressTaken):
		return http.StatusConflict, "address_taken", true
	case errors.Is(err, ErrHostConflict):
		return http.StatusConflict, "host_conflict", true
	case errors.Is(err, hostdomain.ErrInvalidAddress):
		return http.StatusBadRequest, "invalid_request", true
	default:
		return http.StatusInternalServerError, "internal", false
	}
}
