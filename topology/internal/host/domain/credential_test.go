// SPDX-License-Identifier: Apache-2.0

package domain_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kikakkz/looming/topology/internal/host/domain"
)

var testNow = time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

type scriptedRNG struct{ buf []byte }

func (s scriptedRNG) Read(p []byte) (int, error) {
	if len(s.buf) < len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(p, s.buf[:len(p)]), nil
}

func TestGenerateCredentialMintsRawPlusHash(t *testing.T) {
	rng := scriptedRNG{buf: make([]byte, 64)}
	raw, hash, err := domain.GenerateCredential(rng)
	require.NoError(t, err)
	require.Len(t, raw, 43, "base64url of 32 bytes without padding")
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	require.NoError(t, err)
	require.Len(t, decoded, domain.CredentialByteLen)

	want := sha256.Sum256([]byte(raw))
	assert.Equal(t, want[:], hash, "the stored hash is SHA-256 of the raw credential")
}

func TestGenerateCredentialPropagatesRNGFailure(t *testing.T) {
	raw, hash, err := domain.GenerateCredential(scriptedRNG{})
	require.Error(t, err)
	assert.Empty(t, raw)
	assert.Nil(t, hash)
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF))
}

func TestCredentialMatchesConstantTimeShape(t *testing.T) {
	raw, hash, err := domain.GenerateCredential(scriptedRNG{buf: make([]byte, 64)})
	require.NoError(t, err)

	h, err := domain.NewHost("host-1", "10.0.0.9", nil, testNow)
	require.NoError(t, err)
	h.CredentialHash = hash

	assert.True(t, h.CredentialMatches(raw), "the minted credential must verify")
	assert.False(t, h.CredentialMatches(raw+"x"), "a tampered credential must not")
	assert.False(t, h.CredentialMatches(""), "an empty credential must not")
}

func TestCredentialMatchesRejectsUnsetSlot(t *testing.T) {
	h, err := domain.NewHost("host-1", "10.0.0.9", nil, testNow)
	require.NoError(t, err)
	assert.False(t, h.CredentialMatches("anything"), "a host that never joined has no credential")
}
