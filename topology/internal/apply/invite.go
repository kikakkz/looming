// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kikakkz/looming/topology/internal/config"
	"github.com/kikakkz/looming/topology/internal/render"
	"github.com/kikakkz/looming/topology/internal/topology/domain"
)

// Bootstrap-invite step (topology-l1 §3 admin journey, §4 first-admin
// mechanism): after a successful converge, apply closes the admin
// journey by requesting identityd's one-shot bootstrap invite for the
// declared admin email and printing it. Every failure mode short of
// identityd itself is a warn-and-continue — the converge already
// succeeded, and the operator can re-run with --print-invite.

// bootstrapKeyEnvName is the identityd env var apply reads from the
// placement's env_file — the phase-1 secret channel (T1); the key
// never enters the rendered compose content.
const bootstrapKeyEnvName = "IDENTITY_BOOTSTRAP_KEY"

// defaultRegisterPath is the contract value identityd's 201 response
// carries; the fallback covers older/simpler fakes.
const defaultRegisterPath = "/v1/self/register"

// InviteOutcome is the operator-facing result of the invite step. At
// most one of Printed/Skipped/Warning is set; Reason carries the
// human message in the two negative cases.
type InviteOutcome struct {
	// Attempted marks that an invite request reached identityd.
	Attempted bool
	// Printed marks the 201 path: the invite is below.
	Printed bool
	// Skipped marks a deliberate no-op with Reason.
	Skipped bool
	// Warning marks a failed step that did NOT fail the converge.
	Warning bool
	// Reason is the operator-facing message for Skipped/Warning.
	Reason string
	// Token is the one-time invite voucher (only when Printed).
	Token string
	// ExpiresAt is the invite's RFC3339 expiry (only when Printed).
	ExpiresAt string
	// Endpoint is the identityd base URL the invite was requested
	// against (Attempted/Printed).
	Endpoint string
	// RegisterPath is the invite_url_path contract value identityd
	// returned (only when Printed).
	RegisterPath string
	// AdminEmail is the declared first-admin address.
	AdminEmail string
}

// maybeInvite runs the invite step when the config declares one and
// either the converge changed something or the operator forced it with
// --print-invite. It never returns an error: the outcome lands on the
// result and the summary.
func (p *Pipeline) maybeInvite(ctx context.Context, cfg *config.Config, result *Result, force bool) {
	if cfg.Bootstrap == nil || cfg.Bootstrap.AdminEmail == "" {
		return
	}

	inv := &InviteOutcome{AdminEmail: cfg.Bootstrap.AdminEmail}
	result.Invite = inv

	changed := false
	for _, h := range result.Hosts {
		if h.Changed {
			changed = true
			break
		}
	}
	if !changed && !force {
		inv.Skipped = true
		inv.Reason = "converge changed nothing (re-run with --print-invite to force the invite request)"
		return
	}

	endpoint, envFile, err := identitydEndpoint(cfg)
	if err != nil {
		inv.Warning = true
		inv.Reason = err.Error()
		return
	}
	inv.Endpoint = endpoint

	if envFile == "" {
		inv.Warning = true
		inv.Reason = "the identityd placement declares no env_file — cannot read " + bootstrapKeyEnvName + "; skipping the bootstrap invite"
		return
	}
	key, err := readBootstrapKey(p.deps.ReadFile, envFile)
	if err != nil {
		inv.Warning = true
		inv.Reason = err.Error()
		return
	}
	if p.deps.InvitePoster == nil {
		inv.Warning = true
		inv.Reason = "no invite poster configured — cannot request the bootstrap invite"
		return
	}

	inv.Attempted = true
	status, body, err := p.deps.InvitePoster(ctx, endpoint, key, cfg.Bootstrap.AdminEmail)
	if err != nil {
		inv.Warning = true
		inv.Reason = fmt.Sprintf("bootstrap invite request failed: %v", err)
		return
	}
	switch status {
	case http.StatusCreated:
		inv.consumeInviteResponse(body)
	case http.StatusConflict:
		inv.Skipped = true
		inv.Reason = "bootstrap invite already created or an admin exists — skipping"
	default:
		inv.Warning = true
		inv.Reason = fmt.Sprintf("identityd answered %d — skipping the bootstrap invite", status)
	}
}

// consumeInviteResponse parses the 201 body; a malformed answer is a
// warning, not a panic — identityd may be a different version.
func (inv *InviteOutcome) consumeInviteResponse(body []byte) {
	var resp struct {
		Token         string `json:"token"`
		ExpiresAt     string `json:"expires_at"`
		InviteURLPath string `json:"invite_url_path"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Token == "" {
		inv.Warning = true
		inv.Reason = "identityd answered 201 without a usable invite body — check the identity deployment"
		return
	}
	inv.Printed = true
	inv.Token = resp.Token
	inv.ExpiresAt = resp.ExpiresAt
	inv.RegisterPath = resp.InviteURLPath
	if inv.RegisterPath == "" {
		inv.RegisterPath = defaultRegisterPath
	}
}

// identitydEndpoint resolves the identityd placement's reachable URL
// and its env_file (the bootstrap key's location). Errors name the
// operator fix: declare identityd, or fix its placement.
func identitydEndpoint(cfg *config.Config) (endpoint, envFile string, err error) {
	contract, _ := render.Lookup(domain.ComponentIdentityd) // allowlist membership is config-validated
	for _, pl := range cfg.Placements {
		if pl.Component != domain.ComponentIdentityd {
			continue
		}
		var address string
		for _, h := range cfg.Hosts {
			if h.ID == pl.Host {
				address = h.Address
				break
			}
		}
		if address == "" {
			return "", "", fmt.Errorf("the identityd placement references host %q, which is not declared — cannot resolve the invite endpoint", pl.Host)
		}
		port, ok := pl.Ports[contract.ListenPort]
		if !ok {
			return "", "", fmt.Errorf("the identityd placement on %q declares no %q port — cannot resolve the invite endpoint", pl.Host, contract.ListenPort)
		}
		return fmt.Sprintf("http://%s:%d", address, port), pl.EnvFile, nil
	}
	return "", "", errors.New("no identityd placement is declared — add one (with an env_file carrying " + bootstrapKeyEnvName + ") to print the bootstrap invite")
}

// readBootstrapKey extracts IDENTITY_BOOTSTRAP_KEY from the
// operator-prepared env_file. Same KEY=VALUE parsing posture as the
// state plane's env file: apply never generates credentials, and a
// missing key is an operator-facing error message, not a silent skip.
func readBootstrapKey(readFile func(string) ([]byte, error), path string) (string, error) {
	data, err := readFile(path)
	if err != nil {
		return "", fmt.Errorf("bootstrap invite: read identityd env_file %q: %w", path, err)
	}
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("bootstrap invite: env_file %q line %d is not KEY=VALUE: %q", path, lineNo+1, line)
		}
		if strings.TrimSpace(key) != bootstrapKeyEnvName {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "" {
			return "", fmt.Errorf("bootstrap invite: env_file %q sets an empty %s", path, bootstrapKeyEnvName)
		}
		return value, nil
	}
	return "", fmt.Errorf("bootstrap invite: env_file %q carries no %s", path, bootstrapKeyEnvName)
}
