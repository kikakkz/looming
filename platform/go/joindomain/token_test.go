// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/kikakkz/looming/platform/go/joindomain"
)

var testNow = time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

// scriptedRNG serves deterministic randomness and records how many bytes
// were drawn.
type scriptedRNG struct {
	buf []byte
	off int
}

func (s *scriptedRNG) Read(p []byte) (int, error) {
	if s.off+len(p) > len(s.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, s.buf[s.off:])
	s.off += n
	return n, nil
}

func newRNG() *scriptedRNG {
	return newRNGFrom(0)
}

func newRNGFrom(seed byte) *scriptedRNG {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = byte(i+1) + seed
	}
	return &scriptedRNG{buf: buf}
}

func TestGenerateTokenBuildsHashStoredToken(t *testing.T) {
	rng := newRNG()
	raw, tok, err := domain.GenerateToken(domain.RoleEngine, "looming", 24*time.Hour, rng, testNow)
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.Equal(t, domain.RoleEngine, tok.Role)
	assert.Equal(t, "looming", tok.CreatedBy)
	assert.Equal(t, testNow, tok.CreatedAt)
	assert.Equal(t, testNow.Add(24*time.Hour), tok.ExpiresAt)
	assert.Nil(t, tok.UsedAt)

	want := sha256.Sum256([]byte(raw))
	assert.Equal(t, want[:], tok.TokenHash, "only the SHA-256 hash is stored")
}

func TestGenerateTokenRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		role string
		ttl  time.Duration
		rng  io.Reader
	}{
		{"unknown role", "superuser", time.Hour, newRNG()},
		{"empty role", "", time.Hour, newRNG()},
		{"zero ttl", domain.RoleWorker, 0, newRNG()},
		{"negative ttl", domain.RoleWorker, -time.Hour, newRNG()},
		{"rng exhaustion", domain.RoleWorker, time.Hour, &scriptedRNG{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, tok, err := domain.GenerateToken(tc.role, "looming", tc.ttl, tc.rng, testNow)
			require.Error(t, err)
			assert.Empty(t, raw)
			assert.Nil(t, tok)
		})
	}
}

func TestRoleVocabulary(t *testing.T) {
	assert.True(t, domain.ValidRole(domain.RoleEngine))
	assert.True(t, domain.ValidRole(domain.RoleWorker))
	assert.False(t, domain.ValidRole("admin"))
	assert.False(t, domain.ValidRole(""))
}

func TestConsumeHappyPathMarksUsed(t *testing.T) {
	_, tok, err := domain.GenerateToken(domain.RoleEngine, "op", time.Hour, newRNG(), testNow)
	require.NoError(t, err)
	require.NoError(t, tok.Consume(testNow.Add(time.Minute)))
	require.NotNil(t, tok.UsedAt)
	assert.Equal(t, testNow.Add(time.Minute), *tok.UsedAt)
}

func TestConsumeRejectsReuse(t *testing.T) {
	_, tok, err := domain.GenerateToken(domain.RoleEngine, "op", time.Hour, newRNG(), testNow)
	require.NoError(t, err)
	require.NoError(t, tok.Consume(testNow.Add(time.Minute)))
	err = tok.Consume(testNow.Add(2 * time.Minute))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenUsed))
}

func TestConsumeRejectsExpired(t *testing.T) {
	_, tok, err := domain.GenerateToken(domain.RoleWorker, "op", time.Hour, newRNG(), testNow)
	require.NoError(t, err)
	err = tok.Consume(testNow.Add(time.Hour))
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTokenExpired))
}

func TestConsumeStillValidJustBeforeExpiry(t *testing.T) {
	_, tok, err := domain.GenerateToken(domain.RoleWorker, "op", time.Hour, newRNG(), testNow)
	require.NoError(t, err)
	assert.NoError(t, tok.Consume(testNow.Add(time.Hour).Add(-time.Nanosecond)))
}

func TestHashTokenRoundTrip(t *testing.T) {
	sum := domain.HashToken("raw-token")
	require.Len(t, sum, sha256.Size)
	assert.Equal(t, hex.EncodeToString(sum), domain.HashTokenHex("raw-token"))
}

func TestGenerateHostIDShape(t *testing.T) {
	rng := newRNG()
	id, err := domain.GenerateHostID(rng)
	require.NoError(t, err)
	assert.Regexp(t, `^host-[0-9a-f]{8}$`, id)

	// Distinct streams give distinct ids.
	other, err := domain.GenerateHostID(newRNGFrom(0x40))
	require.NoError(t, err)
	assert.NotEqual(t, id, other)
}

func TestGenerateHostIDPropagatesRNGFailure(t *testing.T) {
	id, err := domain.GenerateHostID(&scriptedRNG{})
	require.Error(t, err)
	assert.Empty(t, id)
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF))
}

func TestTokenPrefixIsStableHexSlice(t *testing.T) {
	_, tok, err := domain.GenerateToken(domain.RoleEngine, "op", time.Hour, newRNG(), testNow)
	require.NoError(t, err)
	prefix := tok.Prefix()
	assert.Equal(t, hex.EncodeToString(tok.TokenHash[:domain.PrefixByteLen]), prefix)
	assert.Len(t, prefix, domain.PrefixByteLen*2)

	// A degenerate short hash degrades instead of panicking.
	tok.TokenHash = tok.TokenHash[:2]
	assert.Equal(t, hex.EncodeToString(tok.TokenHash[:2]), tok.Prefix())
}
