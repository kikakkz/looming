// SPDX-License-Identifier: Apache-2.0

// Package factscmd is the admin face's topology-facts command group:
// `looming topology facts pull` merges the machine facts topologyd
// observed at join time (advisor-l1 §8 slice 1.3) into the topology
// file's declared capabilities. The file stays the source of truth —
// the operator reviews the diff like any other edit — and the merge
// protects what observation cannot know: the cloud/lan zone and the
// host labels stay operator-declared; observed values only ever
// replace hardware figures, egress, and latencies.
package factscmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kikakkz/looming/platform/go/config"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
	"github.com/kikakkz/looming/platform/go/render"
	topologydomain "github.com/kikakkz/looming/platform/go/topologydomain"
)

// defaultTopologyPath is the operator-edited desired-state file the
// pull merges into — the same file apply converges (topology-l1 §3).
const defaultTopologyPath = "/etc/looming/topology.yaml"

// serviceTokenEnv is where the operator's topologyd service token
// comes from when --token is not given: the same env var topologyd's
// env_file carries.
const serviceTokenEnv = "TOPOLOGY_SERVICE_TOKEN"

// hostsClient is the read seam the pull talks to — *joinadapter.Client
// in production, fakes in tests.
type hostsClient interface {
	Hosts(ctx context.Context, serviceToken string) ([]joinadapter.ObservedHost, error)
}

// New builds the `topology facts` subtree: the observed-facts
// commands the admin face's topology group carries. The `topology`
// parent itself lives with the advise command (advisor.New) — cmd
// composes the two, keeping the capabilities independent.
func New(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Work with the machine facts the cluster observed",
	}
	cmd.AddCommand(newPull(stdout))
	return cmd
}

// newPull builds `topology facts pull`: read the hosts topologyd knows
// (with their join-time observed capabilities), merge the observations
// into the file's declared capabilities, and write the file back —
// validated through the real config loader before anything reaches
// disk. Nothing applies: the operator reviews the diff and runs
// `looming apply` when satisfied.
func newPull(stdout io.Writer) *cobra.Command {
	var filePath, server, token string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Merge observed host facts into the topology file",
		Long: "Read every host topologyd has registered (with the machine facts each " +
			"host observed at join time) and merge them into the topology file's " +
			"hosts[].capabilities: observed hardware figures, egress, and latencies " +
			"replace declared ones; the zone and labels stay operator-declared. The " +
			"merged file must validate through the same loader apply uses before it " +
			"is written — and it is never applied for you.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(filePath)
			if err != nil {
				return err
			}
			if server == "" {
				server = deriveTopologydURL(cfg)
				if server == "" {
					return errors.New("facts: cannot derive topologyd's URL from the file (no topologyd placement with an http port) — pass --server")
				}
			}
			if token == "" {
				token = os.Getenv(serviceTokenEnv)
				if token == "" {
					return fmt.Errorf("facts: the topology service token is required (--token or $%s)", serviceTokenEnv)
				}
			}
			return runPull(cmd.Context(), stdout, cfg, filePath, joinadapter.NewClient(server), token)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", defaultTopologyPath,
		"path to the topology config file (declared hosts + capabilities facts)")
	cmd.Flags().StringVar(&server, "server", "",
		"topologyd base URL (default: derived from the file's topologyd placement)")
	cmd.Flags().StringVar(&token, "token", "",
		"topology service token (default: $"+serviceTokenEnv+")")
	return cmd
}

// runPull is the command body behind the seam: match observed hosts to
// file hosts, merge, report, and — only when something changed —
// splice, validate, and atomically rewrite the file.
func runPull(ctx context.Context, stdout io.Writer, cfg *config.Config, filePath string, client hostsClient, token string) error {
	observed, err := client.Hosts(ctx, token)
	if err != nil {
		return fmt.Errorf("facts: read hosts from topologyd: %w", err)
	}

	matched, unmatched := matchHosts(cfg.Hosts, observed)
	merged := map[string]*config.Capabilities{}
	updated := 0
	for _, m := range matched {
		mergedCaps, changes := mergeFacts(m.declared.Capabilities, m.observed.Capabilities)
		switch {
		case m.observed.Capabilities == nil:
			_, _ = fmt.Fprintf(stdout, "%s: no observed facts on the server; unchanged\n", m.declared.ID)
		case len(changes) == 0:
			_, _ = fmt.Fprintf(stdout, "%s: unchanged\n", m.declared.ID)
		default:
			merged[m.declared.ID] = mergedCaps
			updated++
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", m.declared.ID, describeChanges(changes))
		}
	}
	for _, obs := range unmatched {
		_, _ = fmt.Fprintf(stdout, "server host %s (%s): no matching host in the file; skipped\n", obs.ID, obs.Address)
	}

	if len(merged) == 0 {
		_, _ = fmt.Fprintf(stdout, "nothing to update — %s untouched\n", filePath)
		return nil
	}

	// #nosec G304 -- the path is the operator-supplied --file, the same
	// file every admin-face command reads.
	document, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("facts: read topology file: %w", err)
	}
	spliced, err := spliceCapabilities(document, cfg.Hosts, merged)
	if err != nil {
		return err
	}
	if err := validateTopologyFile(spliced); err != nil {
		return fmt.Errorf("facts: merged file fails validation: %w", err)
	}
	if err := writeTopologyFile(filePath, spliced); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s — %d host(s) updated, %d unchanged\n",
		filePath, updated, len(matched)-updated)
	return nil
}

// describeChanges renders one host's merge summary for the terminal.
func describeChanges(changes []fieldChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s %s→%s", c.field, c.from, c.to))
	}
	return "capabilities updated (" + strings.Join(parts, ", ") + ")"
}

// deriveTopologydURL locates topologyd through the file's own
// declaration: the topologyd placement's host address plus its http
// port — the phase-1 http://<address>:<port> shape. Any gap degrades
// to "": the caller asks for --server. Unlike the renderer's
// derivation the CLI runs host-side, so a loopback address is usable
// as-is.
func deriveTopologydURL(cfg *config.Config) string {
	contract, ok := render.Lookup(topologydomain.ComponentTopologyd)
	if !ok || contract.ListenPort == "" {
		return ""
	}
	for _, p := range cfg.Placements {
		if p.Component != topologydomain.ComponentTopologyd {
			continue
		}
		port, ok := p.Ports[contract.ListenPort]
		if !ok {
			return ""
		}
		for _, h := range cfg.Hosts {
			if h.ID == p.Host {
				return fmt.Sprintf("http://%s:%d", h.Address, port)
			}
		}
		return ""
	}
	return ""
}

// validateTopologyFile proves the spliced document loads through the
// real config validator — the same fail-closed checks apply runs — via
// a staged temp file, so a merge that would corrupt the file never
// reaches disk.
func validateTopologyFile(content []byte) error {
	tmp, err := os.CreateTemp("", "looming-facts-check-*.yaml")
	if err != nil {
		return fmt.Errorf("facts: stage validation: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeAndClose(tmp, content); err != nil {
		return fmt.Errorf("facts: stage validation: %w", err)
	}
	// #nosec G304 -- the path is this function's own staged temp file.
	if _, err := config.Load(tmp.Name()); err != nil {
		return err
	}
	return nil
}

// writeTopologyFile lands the merged content atomically, preserving
// the file's mode (the operator's editor/umask decision stays theirs).
func writeTopologyFile(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("facts: stat topology file: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".looming-facts-*.yaml")
	if err != nil {
		return fmt.Errorf("facts: stage write: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("facts: stage write: %w", err)
	}
	if err := writeAndClose(tmp, content); err != nil {
		return fmt.Errorf("facts: stage write: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("facts: write topology file: %w", err)
	}
	return nil
}

func writeAndClose(f *os.File, content []byte) error {
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
