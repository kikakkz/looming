// SPDX-License-Identifier: Apache-2.0

// Package advisor is the admin face's advise command (advisor-l1 §3-4,
// AD-38): load the operator's topology facts and the shipped component
// profiles, run the platform evaluator, render the full (component ×
// host) feasibility matrix, and append the session record. Slice 1.1
// is table mode only — the matrix itself is the answer, so an
// infeasible pair never fails the command; only load and validation
// errors do. The interaction lives here, the domain logic in
// platform/go/advisor — the dependency never reverses (AD-38
// decision 7).
package advisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"

	"github.com/kikakkz/looming/cli/internal/profile"
)

// defaultTopologyPath is the operator-edited desired-state file the
// advise command reads facts from — the same file apply converges
// (topology-l1 §3). Keep in sync with applycmd's and dbcmd's copies.
const defaultTopologyPath = "/etc/looming/topology.yaml"

// decisionTableViewed is the slice-1.1 session decision: the operator
// viewed the deterministic table; the reason/decide interaction
// arrives with slice 1.2 and extends this vocabulary.
const decisionTableViewed = "table-viewed"

// New builds `looming advise`: the deterministic placement-feasibility
// table for the declared topology (slice 1.1), and with --reason the
// model-assisted proposal flow (slice 1.2, advisor-l1 §3): the genesis
// channel proposes ranked placements with reasons and risks, the
// evaluator guardrails every proposal, and the operator confirms,
// edits, regenerates, or aborts before anything reaches disk.
func New(stdout, stderr io.Writer) *cobra.Command {
	var filePath string
	var reason bool
	var preference string
	cmd := &cobra.Command{
		Use:   "advise",
		Short: "Show which components can run on which hosts (deterministic feasibility)",
		Long: "Read the topology file's declared host facts, match them against the " +
			"shipped component profiles, and print the full (component × host) " +
			"feasibility matrix — one row per pair, no pagination. FEASIBLE rows " +
			"show remaining memory/CPU; INFEASIBLE rows name the violated hard " +
			"rules and the missing facts. The matrix is the answer: infeasible " +
			"pairs still exit 0; only load or validation failures exit non-zero. " +
			"With --reason, the model channel (genesis, #143) proposes ranked " +
			"placements with reasons and risks; every proposal re-enters the " +
			"evaluator, and the unified diff lands on disk only on your confirm — " +
			"never auto-applied.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if filePath == "" {
				return errors.New("advise: --file must not be empty")
			}
			if reason && strings.TrimSpace(preference) == "" && stdinIsTerminal() {
				answered, err := stdinAsker(stdout)("preference (optional, this session only): ")
				if err != nil && !errors.Is(err, io.EOF) {
					return fmt.Errorf("advise: preference: %w", err)
				}
				preference = answered
			}
			if !reason {
				return run(stdout, stderr, filePath)
			}
			return runReasonedCommand(stdout, stderr, filePath, preference, cmd.Context())
		},
	}
	cmd.Flags().StringVar(&filePath, "file", defaultTopologyPath,
		"path to the topology config file (declared hosts + capabilities facts)")
	cmd.Flags().BoolVar(&reason, "reason", false,
		"propose ranked placements with the model channel (guardrailed, human-confirmed)")
	cmd.Flags().StringVar(&preference, "preference", "",
		"operator free-text preference for this session only (repeatable effect via regenerate)")
	return cmd
}

// runReasonedCommand is the --reason entry: load, advance the genesis
// lifecycle (the channel resolution depends on it; sync notes are
// informational), resolve the model channel, and run the reasoned
// interaction. A sync failure warns — the channel resolution below is
// the hard gate.
func runReasonedCommand(stdout, stderr io.Writer, filePath, preference string, ctx context.Context) error {
	cfg, err := config.Load(filePath)
	if err != nil {
		return err
	}
	profiles, err := padvisor.Parse(padvisor.DefaultProfilesYAML())
	if err != nil {
		return fmt.Errorf("advise: parse embedded profiles: %w", err)
	}

	report, err := syncGenesis(ctx, cfg, time.Now())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "advise: warning: genesis sync: %v\n", err)
	}
	for _, note := range reportNotesWithoutStage(report, err) {
		_, _ = fmt.Fprintf(stderr, "genesis: %s\n", note)
	}

	client, err := modelChannel(cfg)
	if err != nil {
		return err
	}
	ioDeps := reasonIO{
		client: client,
		ask:    stdinAsker(stdout),
		edit:   editorRunner(),
		redact: credentialRedaction(),
		now:    time.Now(),
	}
	return runReason(stdout, stderr, filePath, cfg, profiles, preference, ioDeps)
}

// credentialRedaction builds the scrub the reasoned flow runs over
// everything the model echoes: every credential the local store holds
// (the genesis key while it lives there, the service LoomingKey after
// the switch) never reaches the terminal or the session record — the
// output-safety half of #143's redaction line.
func credentialRedaction() func(string) string {
	secrets := []string{}
	if creds, err := profile.LoadCredentials(); err == nil {
		if key := genesisKeyOrEmpty(creds); key != "" {
			secrets = append(secrets, key)
		}
		if key, err := creds.LoomingKey(serviceCredentialName); err == nil {
			secrets = append(secrets, key)
		}
	}
	return func(s string) string {
		for _, secret := range secrets {
			s = padvisor.Redact(s, secret)
		}
		return s
	}
}

// reportNotesWithoutStage renders the sync notes when the sync itself
// failed: nothing to render, the caller already warned.
func reportNotesWithoutStage(report *GenesisReport, syncErr error) []string {
	if syncErr != nil || report == nil {
		return nil
	}
	return report.Notes
}

// stdinIsTerminal reports whether stdin is a character device — the
// precondition for interactive preference/decide prompts. Piped or
// redirected stdin (CI, scripts) gets no prompts; flags carry the input.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// stdinAsker builds the interactive ask over stdin, one buffered
// reader for the whole session (recreating it per question would eat
// buffered input). EOF with no text is the abort signal.
func stdinAsker(stdout io.Writer) func(question string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	return func(question string) (string, error) {
		_, _ = fmt.Fprint(stdout, question)
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
}

// editorRunner wires the edit decision to $EDITOR; unset means the
// edit option explains itself when chosen.
func editorRunner() func(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return nil
	}
	return func(path string) error {
		// #nosec G204 -- the operator's own $EDITOR on their own staged
		// candidate file is the documented edit mechanism.
		cmd := exec.Command(editor, path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}

// run is the command body: load, evaluate, render, record. The record
// step is deliberately non-fatal — the table already answered, and
// the exit-code contract reserves non-zero for load/validation
// failures — a session-log error warns loudly instead.
func run(stdout, stderr io.Writer, filePath string) error {
	cfg, err := config.Load(filePath)
	if err != nil {
		return err
	}
	profiles, err := padvisor.Parse(padvisor.DefaultProfilesYAML())
	if err != nil {
		return fmt.Errorf("advise: parse embedded profiles: %w", err)
	}

	verdicts := padvisor.Evaluate(cfg.Hosts, profiles)
	if _, err := fmt.Fprint(stdout, renderTable(verdicts)); err != nil {
		// The table never reached the operator — recording a
		// "table-viewed" decision would be a false record.
		_, _ = fmt.Fprintf(stderr, "advise: warning: table not written: %v\n", err)
		return nil
	}

	rec := buildSessionRecord(filePath, cfg.Hosts, verdicts, profileSHA256(padvisor.DefaultProfilesYAML()), time.Now())
	if err := appendSessionRecord(rec); err != nil {
		_, _ = fmt.Fprintf(stderr, "advise: warning: session record not written: %v\n", err)
	}
	return nil
}

// buildSessionRecord assembles the append-only session record
// (advisor-l1 §6): facts snapshot, profile-set hash, matrix summary,
// decision. The timestamp is injected so tests stay deterministic.
func buildSessionRecord(filePath string, hosts []config.Host, verdicts []padvisor.Verdict, profileHash string, now time.Time) sessionRecord {
	summary := matrixSummary{Pairs: len(verdicts)}
	for _, v := range verdicts {
		if v.Feasible {
			summary.Feasible++
		} else {
			summary.Infeasible++
		}
	}
	return sessionRecord{
		Timestamp:     now.UTC().Format(time.RFC3339),
		File:          filePath,
		ProfileSHA256: profileHash,
		FactsSnapshot: hosts,
		Matrix:        summary,
		Decision:      decisionTableViewed,
	}
}

// profileSHA256 fingerprints the profile-set document — the
// profile-version hash the session record quotes (advisor-l1 §6).
func profileSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
