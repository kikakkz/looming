// SPDX-License-Identifier: Apache-2.0

// Package adapter holds the key capability's driven implementations:
// the AES-GCM sealer and the postgres repository.
package adapter

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	"github.com/kikakkz/looming/identity/internal/key/port"
)

// KeyByteLen is the required master-key size (AES-256).
const KeyByteLen = 32

// NonceByteLen is the GCM nonce width prepended to every sealed blob.
const NonceByteLen = 12

// errSealedShape marks a blob too short to hold nonce plus GCM tag.
var errSealedShape = errors.New("identity: sealed blob malformed")

// Sealer protects raw key secrets for the reveal path (dual-track
// storage, identity-l1 §2): AES-GCM with a fresh random nonce per seal,
// stored as nonce‖ciphertext. Opening a tampered or foreign blob fails
// closed.
type Sealer struct {
	aead cipher.AEAD
	rng  io.Reader
}

var _ port.Sealer = (*Sealer)(nil)

// NewSealer wires the sealer; key must be KeyByteLen bytes. rng must be
// a CSPRNG in production (AD-25: injected).
func NewSealer(key []byte, rng io.Reader) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("identity: key sealer init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("identity: key sealer gcm: %w", err)
	}
	return &Sealer{aead: aead, rng: rng}, nil
}

// Seal encrypts plaintext as nonce‖ciphertext.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, NonceByteLen)
	if _, err := io.ReadFull(s.rng, nonce); err != nil {
		return nil, fmt.Errorf("identity: key sealer nonce: %w", err)
	}
	out := make([]byte, 0, NonceByteLen+len(plaintext)+s.aead.Overhead())
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Open decrypts a nonce‖ciphertext blob.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < NonceByteLen {
		return nil, errSealedShape
	}
	plaintext, err := s.aead.Open(nil, sealed[:NonceByteLen], sealed[NonceByteLen:], nil)
	if err != nil {
		return nil, fmt.Errorf("identity: key sealer open: %w", err)
	}
	return plaintext, nil
}
