// SPDX-License-Identifier: Apache-2.0

package app

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	domain "github.com/kikakkz/looming/platform/go/hostdomain"
)

// Handler exposes the host capability's read path over HTTP —
// topology-l1 §7's internal hosts surface (GET /v1/internal/hosts),
// served by topologyd for the admin CLI's observed-facts pull
// (advisor-l1 §8 slice 1.3). The guard mirrors the guide endpoint's:
// the same TOPOLOGY_SERVICE_TOKEN as an "Authorization: Bearer" value.
// The response carries no credential material — the persistent host
// credential never leaves the store.
type Handler struct {
	svc   *Service
	token string
}

// NewHandler wires the handler. serviceToken is the expected
// "Authorization: Bearer" value; empty means the deployment never
// configured TOPOLOGY_SERVICE_TOKEN and the endpoint answers 503 —
// observed facts are not published without their guard.
func NewHandler(svc *Service, serviceToken string) *Handler {
	return &Handler{svc: svc, token: serviceToken}
}

// hostOut is the wire shape of one host: identity, labels, and the
// observed facts (null when the host joined without any). Credential
// material has no field here by construction.
type hostOut struct {
	ID           string               `json:"id"`
	Address      string               `json:"address"`
	Labels       []string             `json:"labels"`
	Capabilities *domain.Capabilities `json:"capabilities"`
}

// Hosts handles GET /v1/internal/hosts: service-token guard, then every
// registered host in id order. The house v1 error envelope answers
// every non-200 (identity precedent).
func (h *Handler) Hosts(w http.ResponseWriter, r *http.Request) {
	if h.token == "" {
		writeHostsError(w, http.StatusServiceUnavailable, "hosts_unavailable", "TOPOLOGY_SERVICE_TOKEN is not configured")
		return
	}
	if !h.authorized(r.Header.Get("Authorization")) {
		writeHostsError(w, http.StatusUnauthorized, "unauthenticated", "unauthenticated")
		return
	}

	hosts, err := h.svc.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "hosts read failed", "err", err)
		writeHostsError(w, http.StatusInternalServerError, "internal", "internal")
		return
	}
	out := make([]hostOut, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, hostOut{
			ID:           host.ID,
			Address:      host.Address,
			Labels:       host.RoleLabels,
			Capabilities: host.Capabilities,
		})
	}
	writeHostsJSON(w, http.StatusOK, out)
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

func writeHostsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHostsError(w http.ResponseWriter, status int, code, message string) {
	writeHostsJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
