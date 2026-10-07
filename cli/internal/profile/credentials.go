// SPDX-License-Identifier: Apache-2.0

// Credentials are the CLI's secrets: LoomingKeys and identity session
// tokens, keyed by name, in ~/.looming/credentials.yaml at mode 0600.
// This file NEVER enters a profile (profiles are shareable), never
// touches the repo, and is the only place secrets live client-side
// (cli-l1 §4).
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Session is a cached identity login (the self API's bearer).
type Session struct {
	Token     string    `yaml:"token"`
	ExpiresAt time.Time `yaml:"expires_at"`
}

// Credentials is the named secret store.
type Credentials struct {
	// LoomingKeys maps credential key -> raw LoomingKey (lk-...).
	LoomingKeys map[string]string `yaml:"looming_keys"`
	// Sessions maps credential key -> cached identity login. Quota
	// and other self-API views ride the session; gateway traffic
	// rides the LoomingKey.
	Sessions map[string]Session `yaml:"sessions,omitempty"`
}

// credentialsPath is the store file.
func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: home directory: %w", err)
	}
	return filepath.Join(home, ".looming", "credentials.yaml"), nil
}

// LoadCredentials reads the store; a missing file is an empty store,
// not an error (first run).
func LoadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	c := &Credentials{LoomingKeys: map[string]string{}, Sessions: map[string]Session{}}
	// #nosec G304 -- path is the fixed store location under ~/.looming.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cli: read credentials: %w", err)
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("cli: parse credentials: %w", err)
	}
	if c.LoomingKeys == nil {
		c.LoomingKeys = map[string]string{}
	}
	if c.Sessions == nil {
		c.Sessions = map[string]Session{}
	}
	return c, nil
}

// Save writes the store 0600, refusing to weaken existing permissions.
func (c *Credentials) Save() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return fmt.Errorf("cli: credential directory: %w", mkErr)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("cli: marshal credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("cli: write credentials: %w", err)
	}
	return nil
}

// SetLoomingKey stores one key under its name.
func (c *Credentials) SetLoomingKey(name, raw string) { c.LoomingKeys[name] = raw }

// SetSession caches one identity login under its name.
func (c *Credentials) SetSession(name string, s Session) { c.Sessions[name] = s }

// Session reads one cached login.
func (c *Credentials) Session(name string) (Session, error) {
	s, ok := c.Sessions[name]
	if !ok {
		return Session{}, fmt.Errorf("cli: no session %q (run `looming onboard`)", name)
	}
	if time.Now().After(s.ExpiresAt) {
		return Session{}, fmt.Errorf("cli: session %q expired (re-run `looming onboard`)", name)
	}
	return s, nil
}

// LoomingKey reads one key.
func (c *Credentials) LoomingKey(name string) (string, error) {
	raw, ok := c.LoomingKeys[name]
	if !ok {
		return "", fmt.Errorf("cli: no credential %q (run `looming onboard`)", name)
	}
	return raw, nil
}
