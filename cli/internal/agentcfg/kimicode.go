// SPDX-License-Identifier: Apache-2.0

// The kimi-code adapter: configures the agent against the looming
// gateway in ~/.kimi-code/config.toml — the configuration root kimi
// code actually reads (CodeRabbit review on PR #142). The managed
// block carries the real schema: a [providers.looming] OpenAI-compatible
// provider plus a [models."looming/<model>"] binding, per the Kimi
// Code config-files documentation. `default_model` stays operator
// choice: select the Looming model with `kimi -m looming/<model>`
// or by setting default_model yourself (the CLI prints the hint).
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() { Register(kimiCode{}) }

// providerKey is the TOML table the managed block owns.
const providerKey = "[providers.looming]"

type kimiCode struct{}

func (kimiCode) Name() string { return "kimi-code" }

// ConfigPath is ~/.kimi-code/config.toml (kimi code's real root).
func (kimiCode) ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: home directory: %w", err)
	}
	return filepath.Join(home, ".kimi-code", "config.toml"), nil
}

// RenderBlock is the TOML the managed region carries: an
// OpenAI-compatible provider against the gateway plus a selectable
// model binding (the wire format kimi code documents).
func (kimiCode) RenderBlock(profileName, gatewayURL, model, loomKey string) string {
	alias := "looming/" + model
	return fmt.Sprintf(`# managed by looming (profile %q, model %q) — edit via: looming configure
[providers.looming]
type = "openai"
base_url = "%s/v1"
api_key = "%s"

[models.%q]
provider = "looming"
model = "%s"`,
		profileName, model, gatewayURL, loomKey, alias, model)
}

// CheckConflict refuses to append the managed block over an unfenced
// [providers.looming] table: the result would define the same TOML
// table twice and be invalid (CodeRabbit review on PR #142).
func (kimiCode) CheckConflict(existing string) error {
	if strings.Contains(existing, fenceStart) {
		return nil // the managed region owns itself
	}
	if strings.Contains(existing, providerKey) {
		return fmt.Errorf("cli: %s config already has an unfenced %s — remove or rename it, then re-run configure", "kimi-code", providerKey)
	}
	return nil
}
