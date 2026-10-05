// SPDX-License-Identifier: Apache-2.0

// Command looming-ctl is the bundle's admin CLI: the operator's handle
// on the topology plane (topology-l1 §7). T1 lands `apply` — the
// idempotent converge from /etc/looming/topology.yaml. The CLI binary's
// final component placement is #108's design surface; this cmd's
// location under topology/ is a T1 temporary (decision recorded on the
// issue), not a layout precedent.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/kikakkz/looming/topology/internal/apply"
	"github.com/kikakkz/looming/topology/internal/exec"
)

// defaultConfigPath is the operator-edited desired-state file the
// apply command converges from (topology-l1 §3).
const defaultConfigPath = "/etc/looming/topology.yaml"

// databaseURLEnv is the operator-provided topology database URL, used
// directly when the config carries no state section (or as an explicit
// override when it does).
const databaseURLEnv = "TOPOLOGY_DATABASE_URL"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, log); err != nil {
		log.Error("looming-ctl exited", "err", err)
		os.Exit(1)
	}
}

// run is the testable entry: it wires the real dependencies, builds
// the command tree, and executes it over the given streams.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, log *slog.Logger) error {
	root := newRoot(stdout, log)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

// newRoot builds the cobra tree: just `apply` in T1; token/join/status
// arrive with T2/T3 (topology-l1 §7).
func newRoot(stdout io.Writer, log *slog.Logger) *cobra.Command {
	root := &cobra.Command{
		Use:   "looming-ctl",
		Short: "Looming bundle admin CLI",
		Long: "Looming bundle admin CLI: converge the deployment from " +
			"/etc/looming/topology.yaml and operate the topology plane.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newApply(stdout, log))
	return root
}

// newApply builds the apply subcommand: converge the declared
// topology end to end (state plane → declare → render → per-host
// docker converge), or stop after the render with --dry-run.
func newApply(stdout io.Writer, log *slog.Logger) *cobra.Command {
	var configPath, bundleRoot string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge the deployment from topology.yaml",
		Long: "Converge the declared topology toward reality: validate the " +
			"config, bring up the state plane on first boot, persist the " +
			"desired state, render per-host compose files, and restart only " +
			"what changed. Idempotent — a second apply with an unchanged " +
			"config is a no-op.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" {
				return errors.New("apply: --config must not be empty")
			}
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
				// contract: the ctl runs with CWD = bundle root).
				if err := os.Chdir(bundleRoot); err != nil {
					return fmt.Errorf("apply: chdir to bundle root: %w", err)
				}
			}

			pipeline := apply.NewPipeline(apply.StdDeps(exec.LocalRunner{}))
			result, err := pipeline.Apply(cmd.Context(), apply.Input{
				ConfigPath:  configPath,
				DryRun:      dryRun,
				DatabaseURL: os.Getenv(databaseURLEnv),
			})
			if err != nil {
				return err
			}

			printSummary(stdout, result, dryRun)
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
	return cmd
}

// printSummary renders the human-facing apply result: the revision,
// per-host converge outcomes, and — in dry-run — the compose files
// themselves. Writes are best-effort by definition (a closed pipe must
// not fail the converge that already happened).
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
		line("next: re-run without --dry-run to converge; T2 adds `token create` + pull-join and the initial-admin invite flow")
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
	// T1 deliberately stops here: the initial-admin invite printing
	// lands with T2 (it needs the identity interaction channel), and
	// pull-join tokens are T2's surface as well.
	line("next: T2 adds `token create` + pull-join and the initial-admin invite flow")
}
