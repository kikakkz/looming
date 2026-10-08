// SPDX-License-Identifier: Apache-2.0

// Package applycmd is the admin face's apply command: converge the
// deployment from the operator's topology.yaml, or stop after the
// render with --dry-run. A thin cobra wrapper over platform/go's
// apply.Pipeline with the production StdDeps — the command owns flag
// parsing, the operator-facing summary, and nothing else (the pipeline
// owns every decision; topology-l1 §3, cli-l1 §4 admin face).
package applycmd

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kikakkz/looming/platform/go/apply"
	"github.com/kikakkz/looming/platform/go/exec"
)

// defaultConfigPath is the operator-edited desired-state file the
// apply command converges from (topology-l1 §3).
const defaultConfigPath = "/etc/looming/topology.yaml"

// DatabaseURLEnv is the operator-provided topology database URL, read
// here for the pipeline's explicit override. The database-direct
// commands (dbcmd) resolve the same variable with their --database-url
// flag; the single source of the variable NAME is this constant — keep
// the two packages' copies in sync.
const DatabaseURLEnv = "TOPOLOGY_DATABASE_URL"

// New builds the apply subcommand: converge the declared topology end
// to end (state plane → declare → render → per-host docker converge),
// or stop after the render with --dry-run. --print-invite requests the
// first-admin bootstrap invite even when the converge changed nothing.
func New(stdout io.Writer, log *slog.Logger) *cobra.Command {
	var configPath, bundleRoot string
	var dryRun, printInvite bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge the deployment from topology.yaml",
		Long: "Converge the declared topology toward reality: validate the " +
			"config, bring up the state plane on first boot, persist the " +
			"desired state, render per-host compose files, and restart only " +
			"what changed. Idempotent — a second apply with an unchanged " +
			"config is a no-op. With a bootstrap section, a changed converge " +
			"(or --print-invite) requests identityd's one-time admin invite " +
			"and prints it.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" {
				return errors.New("apply: --config must not be empty")
			}
			// Resolve the config against the operator's working
			// directory before any chdir: a relative --config must not
			// silently re-anchor to the bundle root.
			absConfig, absErr := filepath.Abs(configPath)
			if absErr != nil {
				return fmt.Errorf("apply: --config: %w", absErr)
			}
			configPath = absConfig
			if bundleRoot != "" {
				info, err := os.Stat(bundleRoot)
				if err != nil {
					return fmt.Errorf("apply: --bundle-root: %w", err)
				}
				if !info.IsDir() {
					return fmt.Errorf("apply: --bundle-root %q is not a directory", bundleRoot)
				}
				// Compose build contexts are rendered relative to the
				// bundle root; make the process CWD match (documented
				// contract: apply runs with CWD = bundle root).
				absRoot, err := filepath.Abs(bundleRoot)
				if err != nil {
					return fmt.Errorf("apply: resolve bundle root: %w", err)
				}
				if err := os.Chdir(absRoot); err != nil {
					return fmt.Errorf("apply: chdir to bundle root: %w", err)
				}
				bundleRoot = absRoot
			}

			pipeline := apply.NewPipeline(apply.StdDeps(exec.LocalRunner{}))
			result, err := pipeline.Apply(cmd.Context(), apply.Input{
				ConfigPath:  configPath,
				DryRun:      dryRun,
				PrintInvite: printInvite,
				DatabaseURL: os.Getenv(DatabaseURLEnv),
				BundleRoot:  bundleRoot,
			})
			if err != nil {
				return err
			}

			printSummary(stdout, result, dryRun)
			if dryRun {
				// Nothing converged — the revision is 0 by contract,
				// and "apply converged" would be a false log line.
				return nil
			}
			if result.Failed() {
				return errors.New("apply: one or more hosts failed to converge (see the summary above)")
			}
			log.Info("apply converged", "revision", result.Revision)
			return nil
		},
	}

	pwd, err := os.Getwd()
	if err != nil {
		pwd = "."
	}
	cmd.Flags().StringVar(&configPath, "config", defaultConfigPath,
		"path to the topology config file")
	cmd.Flags().StringVar(&bundleRoot, "bundle-root", pwd,
		"bundle root directory (compose build contexts resolve against it; defaults to the working directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"stop after the render and print the compose files; no docker, no database")
	cmd.Flags().BoolVar(&printInvite, "print-invite", false,
		"request the bootstrap invite even when the converge changed nothing (the manual recovery when the invite step warned)")
	return cmd
}

// printSummary renders the human-facing apply result: the revision,
// per-host converge outcomes, the bootstrap-invite outcome, and — in
// dry-run — the compose files themselves. Writes are best-effort by
// definition (a closed pipe must not fail the converge that already
// happened).
func printSummary(w io.Writer, result *apply.Result, dryRun bool) {
	line := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}

	if dryRun {
		line("dry-run: rendered compose files (no docker, no database writes)")
		for _, artifact := range result.Artifacts {
			line("--- %s (%s) ---", artifact.HostID, artifact.Hash)
			_, _ = fmt.Fprint(w, artifact.Compose)
		}
		line("next: re-run without --dry-run to converge")
		return
	}

	line("topology revision %d", result.Revision)
	for _, host := range result.Hosts {
		switch {
		case host.Err != nil:
			line("host %s: FAILED: %v", host.HostID, host.Err)
		case host.Changed:
			line("host %s: changed (compose converge ran)", host.HostID)
		default:
			line("host %s: skipped (unchanged)", host.HostID)
		}
	}
	printInviteOutcome(w, result.Invite)
	printGuideOutcome(w, result.Guide)
	line("next: `looming token create --role engine --ttl 24h` mints a join token for a new host")
}

// printGuideOutcome renders the post-converge guide step's outcome: a
// fresh render, an unchanged copy, or a warning that never fails the
// converge.
func printGuideOutcome(w io.Writer, g *apply.GuideOutcome) {
	if g == nil {
		return
	}
	line := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}
	switch {
	case g.Err != nil:
		line("guide: WARNING: %v (converge succeeded; re-run apply to render the guide)", g.Err)
	case g.Rendered:
		line("guide: rendered (the gateway serves it when access.public)")
	default:
		line("guide: unchanged")
	}
}

// printInviteOutcome renders the bootstrap-invite step's outcome: the
// printed invite (token + register example), a deliberate skip, or a
// warning that never fails the converge.
func printInviteOutcome(w io.Writer, inv *apply.InviteOutcome) {
	if inv == nil {
		return
	}
	line := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format+"\n", args...)
	}

	switch {
	case inv.Printed:
		line("bootstrap invite for %s (one-time — deliver it to the mailbox):", inv.AdminEmail)
		line("  token: %s", inv.Token)
		if inv.ExpiresAt != "" {
			line("  expires_at: %s", inv.ExpiresAt)
		}
		line("  register: curl -sS -X POST %s%s -H 'Content-Type: application/json' "+
			"-d '{\"username\":\"<choose>\",\"password\":\"<choose>\",\"email\":\"%s\",\"invite_token\":\"%s\"}'",
			inv.Endpoint, inv.RegisterPath, inv.AdminEmail, inv.Token)
	case inv.Skipped:
		line("bootstrap invite: skipped (%s)", inv.Reason)
	case inv.Warning:
		line("bootstrap invite: WARNING: %s (converge succeeded; fix and re-run with --print-invite)", inv.Reason)
	}
}
