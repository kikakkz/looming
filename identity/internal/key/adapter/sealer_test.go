// SPDX-License-Identifier: Apache-2.0
package adapter

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func testKey() []byte {
	key := make([]byte, KeyByteLen)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestSealerRoundTrip(t *testing.T) {
	sealer, err := NewSealer(testKey(), rand.Reader)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	secret := []byte("lk-testsecret")
	sealed, err := sealer.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("sealed blob must not contain the plaintext")
	}
	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Fatalf("roundtrip mismatch: %q", opened)
	}
}

func TestSealerOpenRejectsTampering(t *testing.T) {
	sealer, _ := NewSealer(testKey(), rand.Reader)
	sealed, _ := sealer.Seal([]byte("lk-tamperme"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := sealer.Open(sealed); err == nil {
		t.Fatal("a tampered blob must fail open")
	}
}

func TestSealerOpenRejectsForeignKey(t *testing.T) {
	sealer, _ := NewSealer(testKey(), rand.Reader)
	other, _ := NewSealer(bytes.Repeat([]byte{0xab}, KeyByteLen), rand.Reader)
	sealed, _ := sealer.Seal([]byte("lk-foreign"))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a blob sealed under another master key must fail open")
	}
}

func TestSealerOpenRejectsShortBlob(t *testing.T) {
	sealer, _ := NewSealer(testKey(), rand.Reader)
	if _, err := sealer.Open([]byte{1, 2, 3}); err == nil {
		t.Fatal("a blob shorter than the nonce must fail open")
	}
}

func TestSealerRejectsBadKeyLength(t *testing.T) {
	if _, err := NewSealer([]byte("short"), rand.Reader); err == nil {
		t.Fatal("a non-32-byte master key must fail fast")
	}
}

func TestSealUsesFreshNonces(t *testing.T) {
	sealer, _ := NewSealer(testKey(), rand.Reader)
	a, _ := sealer.Seal([]byte("lk-same"))
	b, _ := sealer.Seal([]byte("lk-same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext must differ (random nonce)")
	}
}
