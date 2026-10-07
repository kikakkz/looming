// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the authn capability's driven implementations:
// the builtin local provider over postgres (credential check plus
// session-token store) and the argon2id password hasher the
// registration flow shares.
package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/lib/pq"

	"github.com/kikakkz/looming/identity/internal/authn/domain"
	"github.com/kikakkz/looming/identity/internal/authn/port"
)

// Argon2idHasher is the PasswordHasher implementation for the local
// credential scheme (OWASP-preferred argon2id, mature wrapper over
// x/crypto/argon2).
type Argon2idHasher struct{}

// Hash returns the encoded argon2id hash for storage.
func (Argon2idHasher) Hash(password string) (string, error) {
	return argon2id.CreateHash(password, argon2id.DefaultParams)
}

// LocalProvider is the builtin AuthNProvider (identity-l1 §5): local
// credentials verified against the principals table, session tokens in
// authn_tokens. Both are SQL read/written here; the principals access
// is the local provider's private read model, not a port crossing.
type LocalProvider struct {
	db    *sql.DB
	ttl   time.Duration
	rng   io.Reader
	clock func() time.Time
}

// NewLocalProvider wires the provider. rng must be a CSPRNG in
// production; clock is injected (AD-25).
func NewLocalProvider(db *sql.DB, ttl time.Duration, rng io.Reader, clock func() time.Time) *LocalProvider {
	return &LocalProvider{db: db, ttl: ttl, rng: rng, clock: clock}
}

// dummyHash equalizes VerifyPassword timing for unknown usernames: the
// comparison still runs against a real argon2id encoding. It is not a
// credential — the salt is public by design and the password is
// irrelevant; only the parsing and comparison cost matters.
const dummyHash = "$argon2id$v=19$m=65536,t=1,p=8$pzs7w1WHHU3qdyYurUmHSg$APlpcySyWnodhr2nMVU4iaiClTDDmmkAGhImnJzqYqA"

var _ port.Provider = (*LocalProvider)(nil)

// VerifyPassword checks username + password and returns the principal
// ID. Unknown username, wrong password, and non-active principal all
// fail with domain.ErrInvalidCredential — same error, same work, so the
// response shape cannot discriminate (credential hygiene).
func (l *LocalProvider) VerifyPassword(ctx context.Context, username, password string) (string, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(password_hash, ''), status FROM principals WHERE username = $1`,
		username)
	var (
		id        string
		hash      string
		status    string
		knownUser = true
	)
	err := row.Scan(&id, &hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown user: run the same comparison cost against a dummy
		// encoding so timing does not reveal account existence.
		knownUser = false
		id, hash, status = "", dummyHash, ""
	} else if err != nil {
		return "", fmt.Errorf("identity: verify password lookup: %w", err)
	}
	if !knownUser || status != "active" || hash == "" {
		// Deliberately compare anyway where possible to keep the work
		// shape uniform; the result is discarded.
		_, _ = argon2id.ComparePasswordAndHash(password, hash)
		return "", domain.ErrInvalidCredential
	}
	ok, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return "", fmt.Errorf("identity: password hash comparison: %w", err)
	}
	if !ok {
		return "", domain.ErrInvalidCredential
	}
	return id, nil
}

// VerifyExternalToken is not implemented by the builtin-local
// provider: builtin deployments authenticate local credentials only
// (identity-l1 §5). The port stays total; OIDC-mode deployments wire
// the OIDC adapter instead.
func (l *LocalProvider) VerifyExternalToken(_ context.Context, _ string) (string, error) {
	return "", domain.ErrExternalAuthnNotSupported
}

// Issue mints and stores a session token.
func (l *LocalProvider) Issue(ctx context.Context, principalID string) (string, error) {
	raw, tok, err := domain.GenerateToken(principalID, l.ttl, l.rng, l.clock())
	if err != nil {
		return "", err
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT INTO authn_tokens (token_hash, principal_id, issued_at, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		tok.TokenHash, tok.PrincipalID, tok.IssuedAt, tok.ExpiresAt)
	if err != nil {
		return "", fmt.Errorf("identity: token issue: %w", err)
	}
	return raw, nil
}

// Validate resolves a raw token to its claims. Unknown tokens fail with
// domain.ErrInvalidCredential; expired and revoked tokens surface their
// specific errors; a principal that is no longer active cannot use the
// token either (fail closed).
func (l *LocalProvider) Validate(ctx context.Context, rawToken string) (port.TokenInfo, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT t.expires_at, t.revoked_at, p.id, p.roles, p.status
		   FROM authn_tokens t
		   JOIN principals p ON p.id = t.principal_id
		  WHERE t.token_hash = $1`,
		domain.HashRawToken(rawToken))
	var (
		info      port.TokenInfo
		expiresAt time.Time
		revokedAt sql.NullTime
		status    string
	)
	if err := row.Scan(&expiresAt, &revokedAt, &info.PrincipalID, pq.Array(&info.Roles), &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return port.TokenInfo{}, domain.ErrInvalidCredential
		}
		return port.TokenInfo{}, fmt.Errorf("identity: token validate lookup: %w", err)
	}
	tok := &domain.Token{
		PrincipalID: info.PrincipalID,
		IssuedAt:    time.Time{},
		ExpiresAt:   expiresAt,
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		tok.RevokedAt = &t
	}
	if err := tok.ValidateAt(l.clock()); err != nil {
		return port.TokenInfo{}, err
	}
	if status != "active" {
		return port.TokenInfo{}, domain.ErrInvalidCredential
	}
	info.ExpiresAt = expiresAt
	return info, nil
}

// Revoke invalidates a raw token. Unknown tokens fail with
// domain.ErrInvalidCredential; an already-revoked token is a no-op
// (one-way lifecycle, idempotent administration).
func (l *LocalProvider) Revoke(ctx context.Context, rawToken string) error {
	hash := domain.HashRawToken(rawToken)
	row := l.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM authn_tokens WHERE token_hash = $1`, hash)
	var revokedAt sql.NullTime
	if err := row.Scan(&revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrInvalidCredential
		}
		return fmt.Errorf("identity: token revoke lookup: %w", err)
	}
	if revokedAt.Valid {
		return nil
	}
	_, err := l.db.ExecContext(ctx,
		`UPDATE authn_tokens SET revoked_at = $1 WHERE token_hash = $2 AND revoked_at IS NULL`,
		l.clock(), hash)
	if err != nil {
		return fmt.Errorf("identity: token revoke: %w", err)
	}
	return nil
}
