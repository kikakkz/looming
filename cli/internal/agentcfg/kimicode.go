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

type kimiCode struct{}

func (kimiCode) Name() string { return "kimi-code" }

// ConfigPath resolves the config kimi code actually reads: the
// KIMI_CODE_HOME override wins (kimi code's documented data-directory
// relocation), then ~/.kimi-code/config.toml.
func (kimiCode) ConfigPath() (string, error) {
	if override := os.Getenv("KIMI_CODE_HOME"); override != "" {
		return filepath.Join(override, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: home directory: %w", err)
	}
	return filepath.Join(home, ".kimi-code", "config.toml"), nil
}

// RenderBlock is the TOML the managed region carries: an
// OpenAI-compatible provider against the gateway plus a selectable
// model binding. max_context_size is a REQUIRED field of the models
// table (the config-files documentation); it comes from --context-size
// because the gateway catalog does not advertise per-model windows.
func (kimiCode) RenderBlock(profileName, gatewayURL, model string, contextSize int, loomKey string) string {
	alias := "looming/" + model
	baseURL := strings.TrimRight(gatewayURL, "/") + "/v1"
	return fmt.Sprintf(`# managed by looming (profile %q, model %q) — edit via: looming configure
# select the model in kimi code: kimi -m %s
[providers.looming]
type = "openai"
base_url = %q
api_key = %q

[models.%q]
provider = "looming"
model = %q
max_context_size = %d`,
		profileName, model, alias, baseURL, loomKey, alias, model, contextSize)
}

// providerKey/modelPrefix are the tables the managed block owns.
const (
	providerKey = "[providers.looming]"
	modelPrefix = "[models.\"looming/"
)

// CheckConflict refuses to write the managed block over an unfenced
// copy of either table it owns — the result would define the same
// TOML table twice and be invalid. Table headers are PARSED (a line
// whose first non-space character opens the header) so a mention
// inside a comment or string value cannot false-positive (CodeRabbit
// review on PR #142). The managed region itself is excluded.
func (kimiCode) CheckConflict(existing string) error {
	outside := existing
	if start := strings.Index(existing, fenceStart); start >= 0 {
		if end := strings.Index(existing[start:], fenceEnd); end >= 0 {
			outside = existing[:start] + existing[start+end+len(fenceEnd):]
		}
	}
	for _, line := range strings.Split(outside, "\n") {
		header := strings.TrimSpace(line)
		if !strings.HasPrefix(header, "[") {
			continue // comments, strings, scalars cannot define tables
		}
		switch {
		case strings.HasPrefix(header, providerKey):
			return fmt.Errorf("cli: kimi-code config has %s outside the managed block — remove or rename it, then re-run configure", providerKey)
		case strings.HasPrefix(header, modelPrefix):
			return fmt.Errorf("cli: kimi-code config has a %s table outside the managed block — remove or rename it, then re-run configure", modelPrefix)
		}
	}
	return nil
}
