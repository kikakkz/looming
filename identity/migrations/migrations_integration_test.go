// SPDX-License-Identifier: Apache-2.0

//go:build integration

package migrations_test

import (
	"testing"

	"github.com/kikakkz/looming/identity/migrations"
	"github.com/kikakkz/looming/identity/tests/pgtest"
)

func TestUpFailurePaths(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		// Port 1 is the discarded TCP port: always refused, no network wait.
		{"unreachable database", "postgres://identity:identity@127.0.0.1:1/identity?sslmode=disable"},
		// An unregistered scheme fails instance construction, before any dial.
		{"unknown driver", "unknownscheme://nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := migrations.Up(tc.url); err == nil {
				t.Fatal("up must fail for an unusable database URL")
			}
		})
	}
}

func TestUpIsIdempotentAcrossRestarts(t *testing.T) {
	// pgtest migrates at boot; a second Up against the same database
	// must be a clean no-op so process restarts stay safe.
	_, dsn := pgtest.NewDBWithDSN(t)
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("second Up must be a no-op, got: %v", err)
	}
}
