// SPDX-License-Identifier: Apache-2.0

// Command looming-ctl is the bundle's admin CLI: the operator's handle
// on the topology plane (topology-l1 §7). T1 landed `apply` — the
// idempotent converge from /etc/looming/topology.yaml; T2 lands the
// join surface: `token create|list|revoke` (admin side) and `join`
// (the pulling host's self-registration). The CLI binary's final
// component placement is #108's design surface; this cmd's location
// under topology/ is a T1 temporary (decision recorded on the issue),
// not a layout precedent.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the token store

	"github.com/kikakkz/looming/topology/internal/apply"
	"github.com/kikakkz/looming/topology/internal/exec"
	hostadapter "github.com/kikakkz/looming/topology/internal/host/adapter"
	joinadapter "github.com/kikakkz/looming/topology/internal/join/adapter"
	"github.com/kikakkz/looming/topology/internal/join/app"
	"github.com/kikakkz/looming/topology/internal/join/domain"
	topologyadapter "github.com/kikakkz/looming/topology/internal/topology/adapter"
	"github.com/kikakkz/looming/topology/migrations"
)

// defaultConfigPath is the operator-edited desired-state file the
// apply command converges from (topology-l1 §3).
const defaultConfigPath = "/etc/looming/topology.yaml"

// defaultCredFile is where the joining host's persistent credential
// lands (topology-l1 §5 Host row): host id + credential, mode 0600.
//
//nolint:gosec // G101 false positive: this is a file path, not a credential value.
const defaultCredFile = "/etc/looming/host.cred"

// databaseURLEnv is the operator-provided topology database URL, used
// directly when the config carries no state section (or as an explicit
// override when it does), and by the admin-side token commands.
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

// newRoot builds the cobra tree: apply (T1), token + join (T2);
// status/render/guide arrive with T3 (topology-l1 §7).
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
	root.AddCommand(newToken(stdout))
	root.AddCommand(newJoin(stdout))
	return root
}

// newApply builds the apply subcommand: converge the declared
// topology end to end (state plane → declare → render → per-host
// docker converge), or stop after the render with --dry-run. T2 adds
// --print-invite: request the first-admin bootstrap invite even when
// the converge changed nothing.
func newApply(stdout io.Writer, log *slog.Logger) *cobra.Command {
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
				// contract: the ctl runs with CWD = bundle root).
				if err := os.Chdir(bundleRoot); err != nil {
					return fmt.Errorf("apply: chdir to bundle root: %w", err)
				}
			}

			pipeline := apply.NewPipeline(apply.StdDeps(exec.LocalRunner{}))
			result, err := pipeline.Apply(cmd.Context(), apply.Input{
				ConfigPath:  configPath,
				DryRun:      dryRun,
				PrintInvite: printInvite,
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
	cmd.Flags().BoolVar(&printInvite, "print-invite", false,
		"request the bootstrap invite even when the converge changed nothing")
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
	line("next: `looming-ctl token create --role engine --ttl 24h` mints a join token for a new host")
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

// openTokenStore connects to the topology database for the admin-side
// token commands, migrating the schema like apply and topologyd do
// (idempotent no-op on an up-to-date database).
func openTokenStore(databaseURL string) (*sql.DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("token: %s is unset (or pass --database-url)", databaseURLEnv)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("token: open topology store: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("token: ping topology store: %w", err)
	}
	if err := migrations.Up(databaseURL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// newJoinService wires the join capability's service over a raw
// topology database handle — the token commands' construction point.
func newJoinService(db *sql.DB) *app.Service {
	return app.NewService(
		joinadapter.NewTokenStore(db),
		hostadapter.NewRegistry(db),
		topologyadapter.NewStore(db),
		rand.Reader,
		time.Now,
	)
}

// newToken builds the token subcommand tree: mint, list, and revoke
// one-time join tokens (admin side, talks to the topology database
// directly — the kubeadm shape, topology-l1 §7).
func newToken(stdout io.Writer) *cobra.Command {
	var databaseURL string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage one-time join tokens",
	}
	cmd.PersistentFlags().StringVar(&databaseURL, "database-url", "",
		"topology database URL (default: $"+databaseURLEnv+")")
	cmd.AddCommand(newTokenCreate(stdout, &databaseURL))
	cmd.AddCommand(newTokenList(stdout, &databaseURL))
	cmd.AddCommand(newTokenRevoke(stdout, &databaseURL))
	return cmd
}

// resolveDatabaseURL applies the flag-over-env precedence.
func resolveDatabaseURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(databaseURLEnv)
}

// newTokenCreate builds `token create --role <engine|worker> --ttl 24h`:
// mint a token and print it exactly once — the operator carries the
// raw token to the joining host's operator; only its hash is stored.
func newTokenCreate(stdout io.Writer, databaseURL *string) *cobra.Command {
	var role, ttl, createdBy string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a one-time join token (printed once)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !domain.ValidRole(role) {
				return fmt.Errorf("token: --role must be %q or %q, got %q", domain.RoleEngine, domain.RoleWorker, role)
			}
			lifetime, err := time.ParseDuration(ttl)
			if err != nil || lifetime <= 0 {
				return fmt.Errorf("token: --ttl must be a positive duration like 24h, got %q", ttl)
			}
			db, err := openTokenStore(resolveDatabaseURL(*databaseURL))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			raw, tok, err := newJoinService(db).Mint(cmd.Context(), role, createdBy, lifetime)
			if err != nil {
				return fmt.Errorf("token: mint: %w", err)
			}
			_, _ = fmt.Fprintf(stdout, "join token (printed once — deliver it to the joining host's operator):\n"+
				"  token:      %s\n"+
				"  role:       %s\n"+
				"  expires_at: %s\n",
				raw, tok.Role, tok.ExpiresAt.UTC().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "join role: engine or worker (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "token lifetime (Go duration)")
	cmd.Flags().StringVar(&createdBy, "created-by", "looming-ctl", "operator identity recorded on the token")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

// newTokenList builds `token list`: every token's hash prefix, role,
// expiry, and used state — the vocabulary `token revoke` consumes.
func newTokenList(stdout io.Writer, databaseURL *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List join tokens (hash prefix, role, expiry, used state)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openTokenStore(resolveDatabaseURL(*databaseURL))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			tokens, err := newJoinService(db).List(cmd.Context())
			if err != nil {
				return fmt.Errorf("token: list: %w", err)
			}
			if len(tokens) == 0 {
				_, _ = fmt.Fprintln(stdout, "no join tokens")
				return nil
			}
			_, _ = fmt.Fprintln(stdout, "PREFIX        ROLE    EXPIRES_AT            STATE")
			for _, tok := range tokens {
				state := "unused"
				if tok.UsedAt != nil {
					state = "used"
				}
				_, _ = fmt.Fprintf(stdout, "%-13s %-7s %-21s %s\n",
					tok.Prefix(), tok.Role, tok.ExpiresAt.UTC().Format(time.RFC3339), state)
			}
			return nil
		},
	}
}

// newTokenRevoke builds `token revoke <prefix>`: consume a token
// without joining, by the hash prefix `token list` shows.
func newTokenRevoke(stdout io.Writer, databaseURL *string) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <prefix>",
		Short: "Revoke a join token by its hash prefix",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openTokenStore(resolveDatabaseURL(*databaseURL))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			tok, err := newJoinService(db).RevokeByPrefix(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("token: revoke: %w", err)
			}
			_, _ = fmt.Fprintf(stdout, "revoked join token %s (role %s, expired %s)\n",
				tok.Prefix(), tok.Role, tok.ExpiresAt.UTC().Format(time.RFC3339))
			return nil
		},
	}
}

// dialLocalAddr detects the joining host's address as seen from the
// first host: dial the server and read the local end. A package
// variable so tests can script it (AD-25: no real network in unit
// tests).
var dialLocalAddr = func(server string) (string, error) {
	host := server
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		host = u.Host
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("join: cannot auto-detect this host's address (dial %s: %w); pass --address", host, err)
	}
	defer func() { _ = conn.Close() }()
	addr, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		return "", errors.New("join: cannot auto-detect this host's address; pass --address")
	}
	return addr.IP.String(), nil
}

// newJoin builds `join <first-host-url> --token <t>`: the pulling
// host's self-registration (topology-l1 §3 adding-a-host journey). On
// success the persistent credential lands at --cred-file (0600) and is
// shown exactly once on this terminal.
func newJoin(stdout io.Writer) *cobra.Command {
	var token, address, hostID, credFile string
	var labels []string
	var rotate bool

	cmd := &cobra.Command{
		Use:   "join <first-host-url>",
		Short: "Register this host with the cluster (pull join)",
		Args:  cobra.ExactArgs(1),
		Long: "Join the cluster through its topologyd: present a one-time join token, " +
			"register this host, and store the persistent re-join credential at the cred file.",
		RunE: func(cmd *cobra.Command, args []string) error {
			server := args[0]
			if token == "" {
				return errors.New("join: --token is required")
			}
			if rotate {
				return errors.New("join: --rotate is not supported yet: server-side credential rotation is out of scope; " +
					"recovery is re-running join on the original host (its credential file is intact) or admin SQL on the topology database")
			}

			// Reserve the destination before any network call:
			// overwrite and parent-path failures must surface before
			// the one-time token is spent on the server. A failed join
			// removes the reservation again.
			credentialFile, err := reserveCredentialFile(credFile)
			if err != nil {
				return err
			}
			keepCredential := false
			defer func() {
				_ = credentialFile.Close()
				if !keepCredential {
					_ = os.Remove(credFile)
				}
			}()

			addr := address
			if addr == "" {
				detected, detectErr := dialLocalAddr(server)
				if detectErr != nil {
					return detectErr
				}
				addr = detected
			}

			res, err := joinadapter.NewClient(server).Join(cmd.Context(), joinadapter.JoinRequest{
				Token:   token,
				HostID:  hostID,
				Address: addr,
				Labels:  labels,
			})
			if err != nil {
				return err
			}
			if err := writeCredential(credentialFile, credFile, res.HostID+":"+res.Credential); err != nil {
				return err
			}
			keepCredential = true

			_, _ = fmt.Fprintf(stdout, "joined as %s (address %s)\n", res.HostID, addr)
			if res.Access != "" {
				_, _ = fmt.Fprintf(stdout, "cluster access: %s\n", res.Access)
			}
			_, _ = fmt.Fprintf(stdout, "credential stored at %s (mode 0600) — re-join presents it automatically\n", credFile)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "one-time join token (required)")
	cmd.Flags().StringVar(&address, "address", "", "this host's reachable address (default: auto-detect through the first host)")
	cmd.Flags().StringVar(&hostID, "host-id", "", "operator-chosen host id (default: server-generated host-<uuid8>)")
	cmd.Flags().StringVar(&credFile, "cred-file", defaultCredFile, "where the persistent credential is written")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "host label (repeatable)")
	cmd.Flags().BoolVar(&rotate, "rotate", false, "request a new credential for an already-joined host (not supported yet)")
	return cmd
}

// reserveCredentialFile exclusively creates the credential destination
// (0600) before any network call: an existing file or an unwritable
// parent fails here, not after the server consumed the one-time token.
func reserveCredentialFile(path string) (*os.File, error) {
	//nolint:gosec // the path is the operator-supplied --cred-file; O_EXCL plus 0600 pins the contract.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("join: %s already exists — refusing to overwrite it; re-join on the original host or remove the file", path)
		}
		return nil, fmt.Errorf("join: create credential file %q: %w", path, err)
	}
	return f, nil
}

// writeCredential persists the joined host's persistent credential
// through the reserved file, then verifies the mode survived (the file
// is a cluster key).
func writeCredential(file *os.File, path, content string) error {
	if _, err := file.Write([]byte(content + "\n")); err != nil {
		return fmt.Errorf("join: write credential file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("join: stat credential file: %w", err)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("join: credential file %s has mode %o, want 600", path, info.Mode().Perm())
	}
	return nil
}
