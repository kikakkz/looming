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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
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
// table for the declared topology.
func New(stdout, stderr io.Writer) *cobra.Command {
	var filePath string
	cmd := &cobra.Command{
		Use:   "advise",
		Short: "Show which components can run on which hosts (deterministic feasibility)",
		Long: "Read the topology file's declared host facts, match them against the " +
			"shipped component profiles, and print the full (component × host) " +
			"feasibility matrix — one row per pair, no pagination. FEASIBLE rows " +
			"show remaining memory/CPU; INFEASIBLE rows name the violated hard " +
			"rules and the missing facts. The matrix is the answer: infeasible " +
			"pairs still exit 0; only load or validation failures exit non-zero.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if filePath == "" {
				return errors.New("advise: --file must not be empty")
			}
			return run(stdout, stderr, filePath)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", defaultTopologyPath,
		"path to the topology config file (declared hosts + capabilities facts)")
	return cmd
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
	_, _ = fmt.Fprint(stdout, renderTable(verdicts))

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
