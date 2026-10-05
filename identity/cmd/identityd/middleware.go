// SPDX-License-Identifier: Apache-2.0
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
	authnport "github.com/kikakkz/looming/identity/internal/authn/port"
)

// requireAuth guards a route with a validated Bearer session. With
// admin=true the session's roles must include admin; every other role
// gets 403 on admin routes. The validated claims ride the request
// context for the handlers (authnport.TokenInfoFrom).
func requireAuth(provider authnport.Provider, admin bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strict scheme: a missing "Bearer " prefix (Basic auth, raw
		// token, a bare "Bearer") never reaches Validate.
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		info, err := provider.Validate(r.Context(), raw)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrTokenExpired):
				writeAuthError(w, http.StatusUnauthorized, "token_expired")
			case errors.Is(err, domain.ErrTokenRevoked):
				writeAuthError(w, http.StatusUnauthorized, "token_revoked")
			default:
				// Unknown token and every other failure: unauthenticated.
				// The distinction stays server-side (credential hygiene).
				writeAuthError(w, http.StatusUnauthorized, "unauthenticated")
			}
			return
		}
		if admin && !hasRole(info.Roles, "admin") {
			writeAuthError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r.WithContext(authnport.WithTokenInfo(r.Context(), info)))
	})
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func writeAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": code},
	})
}

// requireBootstrapKey guards the one-shot first-admin endpoint with the
// IDENTITY_BOOTSTRAP_KEY shared secret: the operator-side
// `looming-ctl apply` is the only legitimate caller (topology-l1 §4).
// Scheme is strict `Authorization: Bootstrap <key>` and the comparison
// is constant-time (CWE-208). When the key is unconfigured the route
// stays mounted but reports bootstrap_disabled (503) — deployments
// that already have an admin never need the key, so its absence is not
// a boot-time misconfiguration.
func requireBootstrapKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key == "" {
			writeAuthError(w, http.StatusServiceUnavailable, "bootstrap_disabled")
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bootstrap ")
		if !ok || subtle.ConstantTimeCompare([]byte(raw), []byte(key)) != 1 {
			writeAuthError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// argonLimit bounds the expensive argon2id work that unauthenticated
// routes can trigger: login comparisons (including the unknown-user
// dummy comparison, shaped to cost the same) and registration password
// hashing. Each permit stands for ~64 MiB and ~half a second of CPU, so
// the limit bounds total memory and CPU under a credential-stuffing
// flood (CWE-770). Requests beyond the limit are rejected, not queued.
type argonLimit struct {
	permits chan struct{}
}

func newArgonLimit(n int) *argonLimit {
	if n <= 0 {
		n = defaultArgonConcurrency
	}
	return &argonLimit{permits: make(chan struct{}, n)}
}

// wrap rejects with 503 busy when every permit is taken.
func (a *argonLimit) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case a.permits <- struct{}{}:
			defer func() { <-a.permits }()
			next.ServeHTTP(w, r)
		default:
			writeAuthError(w, http.StatusServiceUnavailable, "busy")
		}
	})
}
