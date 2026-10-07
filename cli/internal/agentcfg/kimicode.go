// SPDX-License-Identifier: Apache-2.0

// The kimi-code adapter: configures the agent against the looming
// gateway by writing an [model_provider.looming] section inside the
// managed block of ~/.kimi/config.toml. The user's other config is
// untouched (CLI-2 adds codex/claude adapters in this shape).
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() { Register(kimiCode{}) }

type kimiCode struct{}

func (kimiCode) Name() string { return "kimi-code" }

// ConfigPath is ~/.kimi/config.toml.
func (kimiCode) ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: home directory: %w", err)
	}
	return filepath.Join(home, ".kimi", "config.toml"), nil
}

// RenderBlock is the TOML the managed region carries. The api_key
// travels only into this file (0600); the profile keeps no secret.
func (kimiCode) RenderBlock(profileName, gatewayURL, loomKey string) string {
	return fmt.Sprintf(`# managed by looming (profile %q) — edit via: looming configure
[model_provider.looming]
name = "Looming Gateway (%s)"
base_url = "%s/v1"
api_key = "%s"
model = "default"`, profileName, profileName, gatewayURL, loomKey)
}
