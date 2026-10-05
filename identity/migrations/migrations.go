// SPDX-License-Identifier: Apache-2.0

// Package migrations embeds the identity schema and applies it at
// process start (up only on boot; down exists for tests and dev).
package migrations

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // driver registration
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var schema embed.FS

// Up applies all migrations to the database at the given postgres URL.
// Running against an already-migrated database is a no-op, so restarts
// are safe.
func Up(databaseURL string) error {
	src, err := iofs.New(schema, ".")
	if err != nil {
		return fmt.Errorf("identity: migrations source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return fmt.Errorf("identity: migrations init: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			return nil
		}
		return fmt.Errorf("identity: migrations up: %w", err)
	}
	return nil
}
