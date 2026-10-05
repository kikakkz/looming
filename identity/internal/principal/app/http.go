// SPDX-License-Identifier: Apache-2.0
package app

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
	"github.com/kikakkz/looming/identity/internal/principal/domain"
)

// Handler exposes the principal use cases over HTTP. Gateway
// precedent: HTTP handling lives in the app layer; cmd wires the mux
// and the auth middleware. Methods are mounted per route by cmd.
type Handler struct {
	svc *Service
}

// NewHandler wires the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxBodyBytes = 1 << 20

// --- request/response shapes (v1 API) ---

type registerRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	InviteToken string `json:"invite_token"`
	// Email is matched against a bootstrap invite's binding.
	Email string `json:"email"`
}

type provisionRequest struct {
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	DisplayName string   `json:"display_name"`
	Kind        string   `json:"kind"`
	Roles       []string `json:"roles"`
}

type statusRequest struct {
	Status string `json:"status"`
}

type inviteRequest struct {
	TTL string `json:"ttl"`
}

type bootstrapInviteRequest struct {
	Email string `json:"email"`
}

// RegisterSelf handles POST /v1/self/register.
func (h *Handler) RegisterSelf(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.svc.Register(r.Context(), RegisterInput(req))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":     p.ID,
		"status": string(p.Status),
	})
}

// Me handles GET /v1/self/me; the auth middleware has already validated
// the session and stored its claims in the request context.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	info, ok := authnport.TokenInfoFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing session")
		return
	}
	p, err := h.svc.Get(r.Context(), info.PrincipalID)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writePrincipal(w, http.StatusOK, p)
}

// ProvisionAdmin handles POST /v1/admin/principals.
func (h *Handler) ProvisionAdmin(w http.ResponseWriter, r *http.Request) {
	var req provisionRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.svc.Provision(r.Context(), ProvisionInput{
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Kind:        domain.Kind(req.Kind),
		Roles:       req.Roles,
	})
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writePrincipal(w, http.StatusCreated, p)
}

// ListAdmin handles GET /v1/admin/principals with limit/offset paging.
func (h *Handler) ListAdmin(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := h.svc.List(r.Context(), limit, offset)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, p := range items {
		out = append(out, principalView(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// GetAdmin handles GET /v1/admin/principals/{id}.
func (h *Handler) GetAdmin(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writePrincipal(w, http.StatusOK, p)
}

// ApproveAdmin handles POST /v1/admin/principals/{id}/approve.
func (h *Handler) ApproveAdmin(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Approve(r.Context(), r.PathValue("id"))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writePrincipal(w, http.StatusOK, p)
}

// SetStatusAdmin handles POST /v1/admin/principals/{id}/status.
func (h *Handler) SetStatusAdmin(w http.ResponseWriter, r *http.Request) {
	var req statusRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.svc.SetStatus(r.Context(), r.PathValue("id"), domain.Status(req.Status))
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writePrincipal(w, http.StatusOK, p)
}

// CreateInviteAdmin handles POST /v1/admin/invites. The raw token is
// returned exactly once; the response is the only place it ever
// appears in plaintext.
func (h *Handler) CreateInviteAdmin(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes)).Decode(&req)
	if errors.Is(err, io.EOF) {
		err = nil // empty body means default ttl
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	ttl := time.Duration(0)
	if req.TTL != "" {
		parsed, parseErr := time.ParseDuration(req.TTL)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "ttl must be a Go duration like 24h")
			return
		}
		ttl = parsed
	}
	info, ok := authnport.TokenInfoFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing session")
		return
	}
	raw, tok, err := h.svc.CreateInvite(r.Context(), info.PrincipalID, ttl)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      raw,
		"expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// bootstrapRegisterPath is the contract value the first-admin flow
// prints: the invitee POSTs the voucher (plus their credentials and
// the bound email) to this registration endpoint.
const bootstrapRegisterPath = "/v1/self/register"

// CreateBootstrapInvite handles POST /v1/bootstrap/invite (topology-l1
// §4 first-admin mechanism). The route is guarded by the Bootstrap-key
// middleware in cmd (IDENTITY_BOOTSTRAP_KEY); the one-shot window is a
// domain rule, closed with 409 once an admin exists or a bootstrap
// invite was minted. The raw token is returned exactly once —
// looming-ctl apply prints it and the operator carries it to the
// mailbox.
func (h *Handler) CreateBootstrapInvite(w http.ResponseWriter, r *http.Request) {
	var req bootstrapInviteRequest
	if !decode(w, r, &req) {
		return
	}
	raw, tok, err := h.svc.CreateBootstrapInvite(r.Context(), req.Email, 0)
	if err != nil {
		writeUseCaseError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":           raw,
		"expires_at":      tok.ExpiresAt.UTC().Format(time.RFC3339),
		"invite_url_path": bootstrapRegisterPath,
	})
}

// --- helpers ---

func pageParams(r *http.Request) (limit, offset int) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset, err = strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}
	return limit, offset
}

// principalView is the northbound shape; PasswordHash never leaves the
// service (credential hygiene).
func principalView(p *domain.Principal) map[string]any {
	return map[string]any{
		"id":           p.ID,
		"username":     p.Username,
		"kind":         string(p.Kind),
		"display_name": p.DisplayName,
		"status":       string(p.Status),
		"roles":        p.Roles,
		"version":      p.Version,
		"created_at":   p.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":   p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func writePrincipal(w http.ResponseWriter, status int, p *domain.Principal) {
	writeJSON(w, status, principalView(p))
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
// contract. Safe validation errors keep their detail (the client needs
// it to correct the input); everything else returns the code only —
// backend and invite internals stay server-side (CWE-209) and are
// logged instead.
func writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, safe := classifyUseCaseError(err)
	message := code
	if safe {
		message = err.Error()
	}
	if status == http.StatusInternalServerError || errors.Is(err, domain.ErrInvalidInvite) {
		slog.ErrorContext(r.Context(), "principal request failed", "err", err)
	}
	writeError(w, status, code, message)
}

func classifyUseCaseError(err error) (status int, code string, safe bool) {
	switch {
	case errors.Is(err, ErrSelfRegistrationForbidden):
		return http.StatusForbidden, "registration_forbidden", false
	case errors.Is(err, ErrPasswordTooShort),
		errors.Is(err, ErrInvalidEmail),
		errors.Is(err, domain.ErrInvalidUsername),
		errors.Is(err, domain.ErrDisplayNameTooLong),
		errors.Is(err, domain.ErrInvalidKind),
		errors.Is(err, domain.ErrInviteEmailMismatch):
		return http.StatusBadRequest, "invalid_request", true
	case errors.Is(err, domain.ErrInvalidStatus):
		return http.StatusBadRequest, "invalid_status", true
	case errors.Is(err, domain.ErrInvalidInvite):
		return http.StatusBadRequest, "invalid_invite", false
	case errors.Is(err, domain.ErrBootstrapClosed):
		return http.StatusConflict, "bootstrap_closed", false
	case errors.Is(err, domain.ErrUsernameTaken):
		return http.StatusConflict, "username_taken", false
	case errors.Is(err, domain.ErrInvalidTransition):
		return http.StatusConflict, "invalid_transition", false
	case errors.Is(err, domain.ErrLastAdmin):
		return http.StatusConflict, "last_admin", false
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict, "conflict", false
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", false
	default:
		return http.StatusInternalServerError, "internal", false
	}
}
