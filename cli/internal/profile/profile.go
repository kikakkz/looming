// SPDX-License-Identifier: Apache-2.0

// Profile is the CLI-local connection profile (cli-l1 §4): the
// cluster's endpoints plus a credential reference. Profiles live in
// ~/.looming/profiles/*.yaml and are safe to show and share — the
// credential itself never lands here.
package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile is one named cluster connection.
type Profile struct {
	Name        string `yaml:"name"`
	GatewayURL  string `yaml:"gateway_url"`
	IdentityURL string `yaml:"identity_url"`
	// CredentialKey names the credential set in credentials.yaml.
	CredentialKey string `yaml:"credential_key"`
}

// Dir returns the profiles directory (~/.looming/profiles), creating
// it on first use.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: home directory: %w", err)
	}
	dir := filepath.Join(home, ".looming", "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cli: profile directory: %w", err)
	}
	return dir, nil
}

// Path is the profile file for name.
func Path(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("cli: invalid profile name %q", name)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// Load reads one profile.
func Load(name string) (*Profile, error) {
	path, err := Path(name)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- path is built from the validated profile name.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cli: read profile %q: %w", name, err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("cli: parse profile %q: %w", name, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	return &p, nil
}

// Save writes one profile (0600: endpoints plus the credential key
// name reveal deployment shape).
func Save(p *Profile) error {
	path, err := Path(p.Name)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("cli: marshal profile: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("cli: write profile: %w", err)
	}
	return nil
}

// DefaultName is the profile onboard writes and every command reads
// unless --profile says otherwise.
const DefaultName = "default"

// Default loads the default profile.
func Default() (*Profile, error) { return Load(DefaultName) }
