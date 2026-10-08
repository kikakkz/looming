// SPDX-License-Identifier: Apache-2.0

// Command looming is the user-facing CLI (cli-l1): one binary, two
// faces (AD-37). The user face (onboard/configure/usage) is a pure
// HTTP client; the admin face (apply/token/guide/join — local-root
// operations per cli-l1 §4) arrived with CLI-1 (#108), retiring the
// temporary topology admin binary and consuming the platform/go kit.
package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/kikakkz/looming/cli/internal/admin/applycmd"
	"github.com/kikakkz/looming/cli/internal/admin/dbcmd"
	"github.com/kikakkz/looming/cli/internal/admin/joincmd"
	"github.com/kikakkz/looming/cli/internal/agentcfg"
	"github.com/kikakkz/looming/cli/internal/identityclient"
	"github.com/kikakkz/looming/cli/internal/onboard"
	"github.com/kikakkz/looming/cli/internal/profile"
)

func main() {
	if err := root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	root := &cobra.Command{
		Use:   "looming",
		Short: "Looming CLI — connect local agents to your cluster",
	}
	root.AddCommand(onboardCmd(), configureCmd(), usageCmd())
	root.AddCommand(adminCmd(os.Stdout, os.Stderr)...)
	return root
}

// adminCmd assembles the admin face (cli-l1 §4): apply converges the
// deployment, token/guide talk to the topology database directly, and
// join is the pulling host's self-registration. Every command is a
// thin wrapper over the platform/go kit (#108's extraction).
func adminCmd(stdout, stderr io.Writer) []*cobra.Command {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	return []*cobra.Command{
		applycmd.New(stdout, log),
		dbcmd.NewToken(stdout),
		dbcmd.NewGuide(stdout),
		joincmd.New(stdout),
	}
}

func onboardCmd() *cobra.Command {
	var opts onboard.Options
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Register (or login) and store your cluster connection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, err := onboard.Run(cmd.Context(), opts, onboard.LinePrompter(bufio.NewReader(os.Stdin)))
			if err != nil {
				return err
			}
			fmt.Printf("Profile %q ready. Next: looming configure --agent kimi-code\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.ProfileName, "profile", "", "profile name (default \"default\")")
	cmd.Flags().StringVar(&opts.IdentityURL, "identity", "", "identity endpoint URL")
	cmd.Flags().StringVar(&opts.GatewayURL, "gateway", "", "gateway endpoint URL")
	cmd.Flags().StringVar(&opts.Username, "username", "", "account username")
	cmd.Flags().StringVar(&opts.Password, "password", "", "account password")
	cmd.Flags().StringVar(&opts.IDToken, "id-token", "", "OIDC ID token (OIDC deployments)")
	cmd.Flags().StringVar(&opts.InviteToken, "invite", "", "invite token (invite-mode registration)")
	cmd.Flags().StringVar(&opts.KeyName, "key-name", "", "LoomingKey label")
	cmd.Flags().BoolVar(&opts.Register, "register", false, "self-register before login (per deployment policy)")
	return cmd
}

func configureCmd() *cobra.Command {
	var agentName, profileName, model string
	var contextSize int
	var undo bool
	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Reconcile a profile into a local agent CLI's config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			adapter, err := agentcfg.Lookup(agentName)
			if err != nil {
				return err
			}
			if undo {
				changed, undoErr := agentcfg.Undo(adapter)
				if undoErr != nil {
					return undoErr
				}
				if changed {
					fmt.Printf("Removed the looming managed block from %s's config.\n", agentName)
				} else {
					fmt.Printf("%s's config has no looming managed block.\n", agentName)
				}
				return nil
			}
			p, err := loadProfile(profileName)
			if err != nil {
				return err
			}
			creds, err := profile.LoadCredentials()
			if err != nil {
				return err
			}
			loomKey, err := creds.LoomingKey(p.CredentialKey)
			if err != nil {
				return err
			}
			_, changed, err := agentcfg.Apply(adapter, p.Name, p.GatewayURL, model, contextSize, loomKey)
			if err != nil {
				return err
			}
			if changed {
				fmt.Printf("%s's config now points at profile %q (%s, model %q).\n", agentName, p.Name, p.GatewayURL, model)
				fmt.Printf("Select it in kimi code: kimi -m looming/%s (or set default_model = \"looming/%s\").\n", model, model)
			} else {
				fmt.Printf("%s's config already matches profile %q — nothing to change.\n", agentName, p.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent CLI to configure ("+joinNames()+")")
	cmd.Flags().StringVar(&profileName, "profile", "", "profile to reconcile (default \"default\")")
	cmd.Flags().StringVar(&model, "model", "default", "gateway catalog model this configuration selects")
	cmd.Flags().IntVar(&contextSize, "context-size", 131072, "model context window in tokens (kimi code requires it on every model entry)")
	cmd.Flags().BoolVar(&undo, "undo", false, "remove the looming managed block (content outside the block is preserved; the first-mutation backup stays on disk for manual recovery)")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func usageCmd() *cobra.Command {
	var profileName string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show your quota (identity slice C's self view)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadProfile(profileName)
			if err != nil {
				return err
			}
			creds, err := profile.LoadCredentials()
			if err != nil {
				return err
			}
			session, err := creds.Session(p.CredentialKey)
			if err != nil {
				return err
			}
			client := identityclient.New(p.IdentityURL)
			quota, err := client.SelfQuota(cmd.Context(), &identityclient.Session{
				Token: session.Token, ExpiresAt: session.ExpiresAt,
			})
			if err != nil {
				return err
			}
			if !quota.Exists {
				fmt.Println("No quota set for your account — usage is unlimited until an admin sets one.")
				return nil
			}
			fmt.Printf("Quota: %d %s per %d days (identity-side policy; engine budgets are its projection).\n",
				quota.Quota.Amount, quota.Quota.Unit, quota.Quota.WindowDays)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile to read (default \"default\")")
	return cmd
}

func loadProfile(name string) (*profile.Profile, error) {
	if name == "" {
		return profile.Default()
	}
	return profile.Load(name)
}

func joinNames() string {
	names := agentcfg.Names()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
