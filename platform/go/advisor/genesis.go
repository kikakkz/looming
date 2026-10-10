// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"strings"
	"time"
)

// Stage is one lifecycle stage of the genesis model channel (#143's
// three-stage shape): StageDirect is stage 1 — the advisor calls the
// genesis endpoint directly, an empty cluster is fine; StageGateway is
// stage 2 — calls ride the cluster's gateway with a service-identity
// LoomingKey while the genesis credential lives only as the gateway's
// upstream engine credential. There is deliberately no stage 3: after
// the switch the local genesis copy is erased and the lifecycle is
// terminal (recovery = the admin re-provides, #143).
type Stage string

const (
	StageDirect  Stage = "direct"
	StageGateway Stage = "gateway"
)

// GenesisState is the persisted genesis lifecycle: which stage the
// channel is in and the timestamped evidence behind it. It carries NO
// secret material — the api_key never enters the state file (#143's
// minimization is testable: the serialized state must be quotable
// anywhere). Serialization and I/O stay with the caller; the
// transitions are pure.
type GenesisState struct {
	Stage Stage `json:"stage"`
	// ConvergedAt is the last apply converge success (RFC 3339) — the
	// gateway-ready signal (#143 stage 1 → 2). Refreshes on every
	// successful converge: an unchanged converge still recreates a
	// container whose env file changed, so a later timestamp is honest
	// evidence the gateway now holds the credential.
	ConvergedAt string `json:"converged_at,omitempty"`
	// EnvWrittenAt is when the CLI last wrote the gateway's env file
	// from the stored genesis credential (RFC 3339). Empty means the
	// operator prepared the file by hand — the converge that signalled
	// readiness then already ran with the credential in place.
	EnvWrittenAt string `json:"env_written_at,omitempty"`
	// SwitchedAt is the stage-1 → stage-2 completion (RFC 3339).
	SwitchedAt string `json:"switched_at,omitempty"`
	// ErasedAt is the local genesis credential's erasure (RFC 3339).
	ErasedAt string `json:"erased_at,omitempty"`
}

// RecordConvergence stamps a successful apply converge — the
// gateway-ready signal. Idempotent in effect; the timestamp refreshes
// so env-file writes can be ordered against a later converge.
func (s GenesisState) RecordConvergence(now time.Time) GenesisState {
	s.ConvergedAt = now.UTC().Format(time.RFC3339)
	return s
}

// MarkEnvWritten stamps the CLI writing the gateway env file.
func (s GenesisState) MarkEnvWritten(now time.Time) GenesisState {
	s.EnvWrittenAt = now.UTC().Format(time.RFC3339)
	return s
}

// MarkSwitched completes the transition to the gateway stage.
func (s GenesisState) MarkSwitched(now time.Time) GenesisState {
	s.Stage = StageGateway
	s.SwitchedAt = now.UTC().Format(time.RFC3339)
	return s
}

// MarkErased stamps the local genesis credential's erasure.
func (s GenesisState) MarkErased(now time.Time) GenesisState {
	s.ErasedAt = now.UTC().Format(time.RFC3339)
	return s
}

// SwitchReady reports whether stage 2 may complete now: a converge was
// signalled, the gateway env file carries the genesis credential, and
// — when the CLI wrote that file — a converge came AFTER the write (a
// converge is what recreates the gateway container, so one predating
// the write proves nothing about the running gateway). An operator-
// prepared file (no CLI write on record) needs no ordering proof: the
// signalling converge already ran with it in place.
func (s GenesisState) SwitchReady(envMatches bool) bool {
	if s.Stage != StageDirect || s.ConvergedAt == "" || !envMatches {
		return false
	}
	return s.EnvWrittenAt == "" || s.ConvergedAt > s.EnvWrittenAt
}

// The gateway's upstream engine credential rides the phase-1 secret
// channel — the gateway-front placement's operator-declared env file
// (config.Placement.EnvFile → compose env_file). The names are the
// gateway binary's contract (gateway cmd's loadUpstream).
const (
	GatewayUpstreamEnv     = "GATEWAY_UPSTREAM"
	GatewayUpstreamAuthEnv = "GATEWAY_UPSTREAM_AUTH"
)

// RenderGatewayEnv renders the gateway-front env file content carrying
// the genesis credential as the gateway's upstream engine credential —
// docker env-file format (KEY=value lines).
func RenderGatewayEnv(endpoint, apiKey string) string {
	return GatewayUpstreamEnv + "=" + endpoint + "\n" +
		GatewayUpstreamAuthEnv + "=" + apiKey + "\n"
}

// GatewayEnvMatches reports whether content (a gateway env file) maps
// both upstream variables to exactly the genesis endpoint and key.
// Parse, don't string-compare: operators hand-edit these files, and
// irrelevant lines or ordering must not matter.
func GatewayEnvMatches(content, endpoint, apiKey string) bool {
	values := parseEnvFile(content)
	return values[GatewayUpstreamEnv] == endpoint && values[GatewayUpstreamAuthEnv] == apiKey
}

// parseEnvFile reads docker env-file lines (KEY=value, blank lines and
// # comments skipped) into a map. One '=' splits key from value;
// values are used verbatim.
func parseEnvFile(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = value
	}
	return out
}
