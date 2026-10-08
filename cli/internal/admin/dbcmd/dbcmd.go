// SPDX-License-Identifier: Apache-2.0

// Package dbcmd is the admin face's database-direct command group: the
// join-token lifecycle (token create|list|revoke) and `guide show` —
// admin-side operations that talk to the topology database directly,
// the kubeadm shape (topology-l1 §7). Each command validates its inputs
// before touching the database; the store wiring goes through
// platform/go's services and adapters.
package dbcmd

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver for the store connections

	"github.com/kikakkz/looming/platform/go/config"
	guideadapter "github.com/kikakkz/looming/platform/go/guideadapter"
	guideapp "github.com/kikakkz/looming/platform/go/guideapp"
	hostadapter "github.com/kikakkz/looming/platform/go/hostadapter"
	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
	joinapp "github.com/kikakkz/looming/platform/go/joinapp"
	joindomain "github.com/kikakkz/looming/platform/go/joindomain"
	"github.com/kikakkz/looming/platform/go/migrations"
	topologyadapter "github.com/kikakkz/looming/platform/go/topologyadapter"
)

// defaultConfigPath is the operator-edited desired-state file whose
// presentation facts (cluster name, CLI download URL) feed the guide
// render — the same file applycmd converges. Keep in sync with
// applycmd's copy.
const defaultConfigPath = "/etc/looming/topology.yaml"

// databaseURLEnv is the operator-provided topology database URL. It
// must equal applycmd.DatabaseURLEnv — the two packages stay
// independent, so the shared value is pinned in both places.
const databaseURLEnv = "TOPOLOGY_DATABASE_URL"

// NewToken builds the token subcommand tree: mint, list, and revoke
// one-time join tokens (admin side, talks to the topology database
// directly — the kubeadm shape, topology-l1 §7).
func NewToken(stdout io.Writer) *cobra.Command {
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

// NewGuide builds `guide show`: print the public onboarding guide the
// gateway serves. The persisted copy is read as-is; when none exists
// yet (or the revision moved), it renders on demand from the current
// topology plus the config's presentation facts — which is why the
// command takes --config like apply.
func NewGuide(stdout io.Writer) *cobra.Command {
	var configPath, databaseURL string
	cmd := &cobra.Command{
		Use:   "guide",
		Short: "Show the public onboarding guide",
	}
	cmd.PersistentFlags().StringVar(&databaseURL, "database-url", "",
		"topology database URL (default: $"+databaseURLEnv+")")
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the guide (rendering it on demand when stale)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			db, err := openTopologyStore(resolveDatabaseURL(databaseURL))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			svc := guideapp.NewService(
				guideadapter.NewStore(db),
				topologyadapter.NewStore(db),
				hostadapter.NewRegistry(db),
				time.Now,
			)
			guide, rendered, err := svc.EnsureRendered(cmd.Context(), guideapp.Facts{
				ClusterName:    cfg.ClusterName,
				CLIDownloadURL: cfg.CLIDownloadURL,
			})
			if err != nil {
				return fmt.Errorf("guide: %w", err)
			}
			raw, err := json.MarshalIndent(guide.Snapshot, "", "  ")
			if err != nil {
				return fmt.Errorf("guide: encode: %w", err)
			}
			state := "unchanged (already current)"
			if rendered {
				state = "rendered"
			}
			_, _ = fmt.Fprintf(stdout, "guide %s at revision %d:\n%s\n", state, guide.RenderedRev, raw)
			return nil
		},
	})
	cmd.PersistentFlags().StringVar(&configPath, "config", defaultConfigPath,
		"path to the topology config file (cluster name + CLI URL facts)")
	return cmd
}

// resolveDatabaseURL applies the flag-over-env precedence.
func resolveDatabaseURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(databaseURLEnv)
}

// openTopologyStore connects to the topology database for the
// database-direct commands, migrating the schema like apply and
// topologyd do (idempotent no-op on an up-to-date database).
func openTopologyStore(databaseURL string) (*sql.DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("topology: %s is unset (or pass --database-url)", databaseURLEnv)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("topology: open topology store: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("topology: ping topology store: %w", err)
	}
	if err := migrations.Up(databaseURL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// newJoinService wires the join capability's service over a raw
// topology database handle — the token commands' construction point.
func newJoinService(db *sql.DB) *joinapp.Service {
	return joinapp.NewService(
		joinadapter.NewTokenStore(db),
		hostadapter.NewRegistry(db),
		topologyadapter.NewStore(db),
		rand.Reader,
		time.Now,
	)
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
			if !joindomain.ValidRole(role) {
				return fmt.Errorf("token: --role must be %q or %q, got %q", joindomain.RoleEngine, joindomain.RoleWorker, role)
			}
			lifetime, err := time.ParseDuration(ttl)
			if err != nil || lifetime <= 0 {
				return fmt.Errorf("token: --ttl must be a positive duration like 24h, got %q", ttl)
			}
			db, err := openTopologyStore(resolveDatabaseURL(*databaseURL))
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
	cmd.Flags().StringVar(&createdBy, "created-by", "looming", "operator identity recorded on the token")
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
			db, err := openTopologyStore(resolveDatabaseURL(*databaseURL))
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
			db, err := openTopologyStore(resolveDatabaseURL(*databaseURL))
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
