// SPDX-License-Identifier: Apache-2.0

// Package joincmd is the admin face's join command: the pulling host's
// self-registration through the first host's topologyd
// (topology-l1 §3 adding-a-host journey). On success the persistent
// credential lands at --cred-file (0600) and is shown exactly once on
// this terminal.
package joincmd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	joinadapter "github.com/kikakkz/looming/platform/go/joinadapter"
)

// defaultCredFile is where the joining host's persistent credential
// lands (topology-l1 §5 Host row): host id + credential, mode 0600.
//
//nolint:gosec // G101 false positive: this is a file path, not a credential value.
const defaultCredFile = "/etc/looming/host.cred"

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

// New builds `join <first-host-url> --token <t>`.
func New(stdout io.Writer) *cobra.Command {
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

			// Machine facts ride the join payload (advisor-l1 §8 slice
			// 1.3): collected locally, degrading per-fact, announced on
			// stdout so the operator sees exactly what registers.
			caps := collectFacts(cmd.Context())
			printFactsSummary(stdout, caps)

			res, err := joinadapter.NewClient(server).Join(cmd.Context(), joinadapter.JoinRequest{
				Token:        token,
				HostID:       hostID,
				Address:      addr,
				Labels:       labels,
				Capabilities: caps,
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
