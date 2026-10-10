// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
	topologydomain "github.com/kikakkz/looming/platform/go/topologydomain"

	"github.com/kikakkz/looming/cli/internal/identityclient"
	"github.com/kikakkz/looming/cli/internal/profile"
)

// Well-known names of the genesis lifecycle's local artifacts. The
// service principal is a fixed identity (#143 stage 2's "service
// identity LoomingKey"); the LoomingKey lands in the credentials store
// under serviceCredentialName.
const (
	genesisStateFileName     = "genesis.json"
	servicePrincipalUsername = "looming-advisor"
	serviceCredentialName    = "advisor"
	serviceKeyName           = "genesis-channel"
)

// genesisStatePath resolves the lifecycle state file next to the
// session log: ~/.looming/advisor/genesis.json, mode 0600 under the
// 0700 advisor directory (AD-37 §5 discipline).
func genesisStatePath() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("genesis: resolve home directory: %w", err)
	}
	return filepath.Join(home, advisorDirName, genesisStateFileName), nil
}

// loadGenesisState reads the lifecycle state; a missing file is the
// zero state (stage direct, no evidence) — first run. A fresh state's
// Stage normalizes to direct so downstream code never special-cases "".
func loadGenesisState() (padvisor.GenesisState, error) {
	path, err := genesisStatePath()
	if err != nil {
		return padvisor.GenesisState{}, err
	}
	// #nosec G304 -- the path is the fixed advisor state location.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return padvisor.GenesisState{Stage: padvisor.StageDirect}, nil
	}
	if err != nil {
		return padvisor.GenesisState{}, fmt.Errorf("genesis: read state: %w", err)
	}
	var state padvisor.GenesisState
	if err := json.Unmarshal(data, &state); err != nil {
		return padvisor.GenesisState{}, fmt.Errorf("genesis: parse state: %w", err)
	}
	if state.Stage == "" {
		state.Stage = padvisor.StageDirect
	}
	return state, nil
}

// saveGenesisState persists the state 0600; the advisor directory is
// created/widened to 0700 like the session-log home.
func saveGenesisState(state padvisor.GenesisState) error {
	path, err := genesisStatePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("genesis: create advisor directory: %w", mkErr)
	}
	// #nosec G302 -- directory mode, not a file: 0700 is the contract.
	if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
		return fmt.Errorf("genesis: secure advisor directory: %w", chmodErr)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("genesis: encode state: %w", err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		return fmt.Errorf("genesis: write state: %w", writeErr)
	}
	return nil
}

// NotifyApplyConverged records a successful apply converge — the
// gateway-ready signal that moves the genesis lifecycle toward stage 2
// (#143). Wired into applycmd as its post-converge hook from
// cmd/looming (the cli matrix keeps the capabilities independent); a
// failure warns in apply's log but never fails the converge.
func NotifyApplyConverged(_ context.Context) error {
	return notifyApplyConvergedAt(time.Now())
}

// notifyApplyConvergedAt is the clock-injected body: tests drive the
// ordering gates with a fake clock.
func notifyApplyConvergedAt(now time.Time) error {
	state, err := loadGenesisState()
	if err != nil {
		return err
	}
	return saveGenesisState(state.RecordConvergence(now))
}

// GenesisReport is one sync's operator-facing outcome: the stage after
// the sync and redacted notes explaining where the lifecycle stands.
type GenesisReport struct {
	Stage padvisor.Stage
	Notes []string
}

// syncGenesis advances the genesis lifecycle one step (#143's
// three-stage shape) and reports where it stands. It is safe to re-run:
// every transition is evidence-gated in the pure state machine, and
// completed steps short-circuit. The stage branches live in their own
// functions — the complexity budget (AD-24) counts this dispatcher
// only.
func syncGenesis(ctx context.Context, cfg *config.Config, now time.Time) (*GenesisReport, error) {
	state, err := loadGenesisState()
	if err != nil {
		return nil, err
	}
	creds, err := profile.LoadCredentials()
	if err != nil {
		return nil, err
	}

	report := &GenesisReport{Stage: state.Stage}
	if state.Stage == padvisor.StageGateway {
		return syncGatewayStage(state, creds, now, report)
	}

	report.Notes = append(report.Notes, "channel: direct genesis endpoint")
	if cfg == nil || cfg.Genesis == nil {
		report.Notes = append(report.Notes, "no genesis section in the topology file — nothing to advance")
		return redactReport(report, creds), nil
	}
	if state.ConvergedAt == "" {
		report.Notes = append(report.Notes, "stage 1: no apply converge recorded yet — the gateway-ready signal arrives with the first successful converge")
		return redactReport(report, creds), nil
	}
	envFile := gatewayEnvFile(cfg)
	if envFile == "" {
		report.Notes = append(report.Notes, "stage 2 blocked: apply converged, but the gateway-front placement declares no env_file — the genesis credential cannot become the gateway's upstream engine credential via existing surfaces; the channel stays direct (declare env_file on the gateway-front placement and re-run)")
		return redactReport(report, creds), nil
	}
	return advanceStageTwo(ctx, state, cfg, creds, envFile, now, report)
}

// syncGatewayStage finishes a lifecycle whose stage already flipped: a
// previous run may have died mid-erasure — retry; terminal means the
// local copy is gone.
func syncGatewayStage(state padvisor.GenesisState, creds *profile.Credentials, now time.Time, report *GenesisReport) (*GenesisReport, error) {
	report.Notes = append(report.Notes, "channel: gateway (service identity LoomingKey)")
	if state.ErasedAt != "" {
		return redactReport(report, creds), nil
	}
	if err := eraseGenesisKey(creds); err != nil {
		return nil, err
	}
	if err := saveGenesisState(state.MarkErased(now)); err != nil {
		return nil, err
	}
	report.Notes = append(report.Notes, "genesis api key erased from the local store")
	return redactReport(report, creds), nil
}

// advanceStageTwo walks the direct-stage tail once the converge signal
// arrived and the gateway env file is declared: prepare the env file
// when it does not carry the credential yet, wait when a CLI-written
// file still owes a later converge, and complete the switch when the
// evidence gates pass.
func advanceStageTwo(ctx context.Context, state padvisor.GenesisState, cfg *config.Config, creds *profile.Credentials, envFile string, now time.Time, report *GenesisReport) (*GenesisReport, error) {
	key := genesisKeyOrEmpty(creds)
	// #nosec G304 -- the path is the operator-declared env file.
	content, readErr := os.ReadFile(envFile)
	matches := key != "" && readErr == nil && padvisor.GatewayEnvMatches(string(content), cfg.Genesis.Endpoint, key)
	if !matches {
		if key == "" {
			report.Notes = append(report.Notes, "stage 2 blocked: the genesis api key is not in the local store (run `looming genesis set`); the channel stays direct")
			return redactReport(report, creds), nil
		}
		if err := writeGatewayEnvFile(envFile, cfg.Genesis.Endpoint, key); err != nil {
			return nil, err
		}
		if err := saveGenesisState(state.MarkEnvWritten(now)); err != nil {
			return nil, err
		}
		report.Notes = append(report.Notes, "stage 2 prepared: the gateway env file now carries the genesis credential — re-run `looming apply`, then `looming genesis sync` to complete the switch")
		return redactReport(report, creds), nil
	}

	if !state.SwitchReady(true) {
		report.Notes = append(report.Notes, "stage 2 pending: the env file carries the genesis credential, and a converge after the write is still awaited (re-run `looming apply`)")
		return redactReport(report, creds), nil
	}

	if err := ensureServiceKey(ctx, creds); err != nil {
		return nil, err
	}
	if err := eraseGenesisKey(creds); err != nil {
		return nil, err
	}
	if err := saveGenesisState(state.MarkSwitched(now).MarkErased(now)); err != nil {
		return nil, err
	}
	report.Stage = padvisor.StageGateway
	report.Notes = append(report.Notes, "stage 2 complete: the channel rides the gateway with the service identity LoomingKey, and the local genesis api key was erased")
	return redactReport(report, creds), nil
}

// genesisKeyOrEmpty reads the stored genesis api key, mapping the
// missing-entry case to "" so callers can branch on preparation state
// without an error in hand.
func genesisKeyOrEmpty(creds *profile.Credentials) string {
	key, err := creds.GenesisKey()
	if err != nil {
		return ""
	}
	return key
}

// redactReport scrubs every note with whatever secrets the store
// currently holds — belt and braces over the by-construction guarantee
// that notes never quote key material (#143's testable redaction line).
func redactReport(report *GenesisReport, creds *profile.Credentials) *GenesisReport {
	secrets := []string{genesisKeyOrEmpty(creds)}
	if key, err := creds.LoomingKey(serviceCredentialName); err == nil {
		secrets = append(secrets, key)
	}
	for i, note := range report.Notes {
		for _, secret := range secrets {
			note = padvisor.Redact(note, secret)
		}
		report.Notes[i] = note
	}
	return report
}

// gatewayEnvFile returns the gateway-front placement's declared env
// file path, "" when no gateway-front placement carries one (either
// absent entirely or the gap case: present without the phase-1 secret
// channel declared).
func gatewayEnvFile(cfg *config.Config) string {
	for _, p := range cfg.Placements {
		if p.Component == topologydomain.ComponentGatewayFront {
			return p.EnvFile
		}
	}
	return ""
}

// writeGatewayEnvFile renders the genesis credential into the
// operator-declared env file — the gateway's upstream engine credential
// on the phase-1 secret channel. 0600: it is secret material at rest.
func writeGatewayEnvFile(path, endpoint, apiKey string) error {
	if err := os.WriteFile(path, []byte(padvisor.RenderGatewayEnv(endpoint, apiKey)), 0o600); err != nil {
		return fmt.Errorf("genesis: write gateway env file %q: %w", path, err)
	}
	return nil
}

// eraseGenesisKey removes the genesis api key from the local store —
// #143's terminal minimization (no emergency fallback; recovery = the
// admin re-provides into the gateway's env file).
func eraseGenesisKey(creds *profile.Credentials) error {
	creds.SetGenesis("")
	if err := creds.Save(); err != nil {
		return fmt.Errorf("genesis: erase local api key: %w", err)
	}
	return nil
}

// ensureServiceKey provisions the stage-2 service identity through
// identity's existing surface (#143): admin-provision a service-kind
// principal, authenticate as it, issue its LoomingKey on the self
// route, store the key under the well-known credential name. An
// existing local key short-circuits — re-runs are idempotent.
func ensureServiceKey(ctx context.Context, creds *profile.Credentials) error {
	if _, err := creds.LoomingKey(serviceCredentialName); err == nil {
		return nil
	}
	p, err := profile.Default()
	if err != nil {
		return fmt.Errorf("genesis: service identity needs the cluster profile (run `looming onboard`): %w", err)
	}
	sess, err := creds.Session(p.CredentialKey)
	if err != nil {
		return fmt.Errorf("genesis: service identity needs a live admin session (re-run `looming onboard`): %w", err)
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	client := identityclient.New(p.IdentityURL)
	if _, provisionErr := client.ProvisionServicePrincipal(ctx, sess.Token, servicePrincipalUsername, password, "Looming management agent"); provisionErr != nil {
		if identityclient.IsCode(provisionErr, "username_taken") {
			return errors.New("genesis: service principal \"looming-advisor\" exists but its key is not in the local store — recovery: delete the principal as an identity admin, then re-run `looming genesis sync`")
		}
		return fmt.Errorf("genesis: provision service principal: %w", provisionErr)
	}
	svc, err := client.Login(ctx, servicePrincipalUsername, password)
	if err != nil {
		return fmt.Errorf("genesis: service principal login: %w", err)
	}
	issued, err := client.IssueKey(ctx, svc, serviceKeyName)
	if err != nil {
		return fmt.Errorf("genesis: issue service LoomingKey: %w", err)
	}
	creds.SetLoomingKey(serviceCredentialName, issued.Key)
	if saveErr := creds.Save(); saveErr != nil {
		return fmt.Errorf("genesis: store service LoomingKey: %w", saveErr)
	}
	return nil
}

// randomPassword mints the one-shot bootstrap password the service
// principal registers with; the LoomingKey outlives it and the password
// is never stored.
func randomPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("genesis: generate service password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// modelChannel resolves the LLMClient the current lifecycle stage
// calls through — stage 2's gateway client, or stage 1's direct genesis
// client. advise's --reason path (slice 1.2) consumes this after
// running the sync.
func modelChannel(cfg *config.Config) (padvisor.LLMClient, error) {
	state, err := loadGenesisState()
	if err != nil {
		return nil, err
	}
	creds, err := profile.LoadCredentials()
	if err != nil {
		return nil, err
	}
	if state.Stage == padvisor.StageGateway {
		key, keyErr := creds.LoomingKey(serviceCredentialName)
		if keyErr != nil {
			return nil, fmt.Errorf("advise: gateway channel without the service LoomingKey (%v)", keyErr)
		}
		p, profileErr := profile.Default()
		if profileErr != nil {
			return nil, fmt.Errorf("advise: gateway channel needs the cluster profile: %w", profileErr)
		}
		return padvisor.NewGatewayClient(strings.TrimSuffix(p.GatewayURL, "/")+"/v1", key, nil)
	}
	if cfg == nil || cfg.Genesis == nil {
		return nil, errors.New("advise: model calls need the genesis channel — declare genesis: {endpoint: ...} in the topology and run `looming genesis set`, or complete the switch to the gateway (see `looming genesis status`)")
	}
	key, err := creds.GenesisKey()
	if err != nil {
		return nil, fmt.Errorf("advise: %w", err)
	}
	return padvisor.NewGenesisClient(cfg.Genesis.Endpoint, key, nil)
}

// NewGenesis builds `looming genesis`: the bootstrap model-channel
// lifecycle (#143) — set stores the admin-provided api key in the
// client-side secret channel, sync advances the lifecycle, status
// reports it. No subcommand ever prints key material.
func NewGenesis(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "genesis",
		Short: "Manage the genesis model-channel lifecycle (bootstrap → direct → gateway → erased)",
	}
	cmd.AddCommand(newGenesisSet(stdout), newGenesisSync(stdout), newGenesisStatus(stdout))
	return cmd
}

// newGenesisSet builds `genesis set --api-key KEY`: store the admin's
// genesis credential in ~/.looming/credentials.yaml (0600 — the
// cli-l1 §4 channel), keyed to the topology file's genesis.endpoint.
func newGenesisSet(stdout io.Writer) *cobra.Command {
	var apiKey, configPath string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Store the genesis api key in the local secret store",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(apiKey) == "" {
				return errors.New("genesis: --api-key must not be empty")
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if cfg.Genesis == nil {
				return fmt.Errorf("genesis: %s declares no genesis section — add genesis: {endpoint: ...} to the topology file first", configPath)
			}
			state, err := loadGenesisState()
			if err != nil {
				return err
			}
			if state.Stage == padvisor.StageGateway {
				return errors.New("genesis: the channel already rides the gateway and the local copy was erased — re-providing belongs in the gateway's env file now (recovery: re-create the gateway-front env file and re-run `looming apply`)")
			}
			creds, err := profile.LoadCredentials()
			if err != nil {
				return err
			}
			creds.SetGenesis(apiKey)
			if err := creds.Save(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "genesis api key stored for endpoint %s (stage 1: direct channel).\n", cfg.Genesis.Endpoint)
			return nil
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "the genesis api key (never echoed or printed)")
	cmd.Flags().StringVar(&configPath, "config", defaultTopologyPath, "path to the topology config file")
	return cmd
}

// newGenesisSync builds `genesis sync`: advance the lifecycle — write
// the gateway env file when the converge signal arrived, complete the
// switch when the evidence gates pass, erase the local key.
func newGenesisSync(stdout io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Advance the genesis lifecycle toward the gateway stage",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			report, err := syncGenesis(cmd.Context(), cfg, time.Now())
			if err != nil {
				return err
			}
			for _, note := range report.Notes {
				_, _ = fmt.Fprintf(stdout, "genesis: %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultTopologyPath, "path to the topology config file")
	return cmd
}

// newGenesisStatus builds `genesis status`: the lifecycle stage and
// evidence timestamps, plus which local artifacts exist — never their
// values.
func newGenesisStatus(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the genesis lifecycle stage (no secrets)",
		RunE: func(_ *cobra.Command, _ []string) error {
			state, err := loadGenesisState()
			if err != nil {
				return err
			}
			creds, err := profile.LoadCredentials()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "stage: %s\n", state.Stage)
			_, _ = fmt.Fprintf(stdout, "last converge: %s\n", orNever(state.ConvergedAt))
			_, _ = fmt.Fprintf(stdout, "env file written: %s\n", orNever(state.EnvWrittenAt))
			_, _ = fmt.Fprintf(stdout, "switched: %s\n", orNever(state.SwitchedAt))
			_, _ = fmt.Fprintf(stdout, "local key erased: %s\n", orNever(state.ErasedAt))
			_, _ = fmt.Fprintf(stdout, "genesis api key in store: %t\n", genesisKeyOrEmpty(creds) != "")
			_, err = creds.LoomingKey(serviceCredentialName)
			_, _ = fmt.Fprintf(stdout, "service LoomingKey in store: %t\n", err == nil)
			return nil
		},
	}
	return cmd
}

// orNever renders an absent RFC 3339 timestamp as the operator-facing
// "never".
func orNever(ts string) string {
	if ts == "" {
		return "never"
	}
	return ts
}
