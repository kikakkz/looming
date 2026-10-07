// SPDX-License-Identifier: Apache-2.0

// Package onboard is the guide-driven onboarding journey (cli-l1 §3):
// endpoints -> register-or-login (per policy) -> issue key -> write
// the default profile and credential. Every prompt has a flag escape
// hatch so CI and scripting can run non-interactively.
package onboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kikakkz/looming/cli/internal/identityclient"
	"github.com/kikakkz/looming/cli/internal/profile"
)

// Options carries every prompt's answer; empty fields mean "ask".
type Options struct {
	ProfileName string
	IdentityURL string
	GatewayURL  string
	Username    string
	Password    string
	IDToken     string
	InviteToken string
	KeyName     string
	// Register, when true, skips the "register or login" question and
	// self-registers (the deployment's policy decides if that works).
	Register bool
}

// Run executes the journey and returns the written profile name.
func Run(ctx context.Context, opts Options, prompt func(string) (string, error)) (string, error) {
	identityURL, gatewayURL, err := collectEndpoints(opts, prompt)
	if err != nil {
		return "", err
	}
	session, err := authenticate(ctx, identityclient.New(identityURL), opts, prompt)
	if err != nil {
		return "", fmt.Errorf("onboard: login: %w", err)
	}
	keyName, err := keyName(opts, prompt)
	if err != nil {
		return "", err
	}
	return storeProfile(ctx, identityclient.New(identityURL), session, identityURL, gatewayURL, keyName, opts)
}

// collectEndpoints asks for whatever the options did not carry.
func collectEndpoints(opts Options, prompt func(string) (string, error)) (identityURL, gatewayURL string, err error) {
	identityURL = opts.IdentityURL
	if identityURL == "" {
		answer, askErr := prompt("Identity endpoint (from your cluster's guide page)")
		if askErr != nil {
			return "", "", askErr
		}
		identityURL = strings.TrimSpace(answer)
	}
	if identityURL == "" {
		return "", "", errors.New("onboard: identity endpoint is required")
	}
	gatewayURL = opts.GatewayURL
	if gatewayURL == "" {
		answer, askErr := prompt("Gateway endpoint (from the guide page)")
		if askErr != nil {
			return "", "", askErr
		}
		gatewayURL = strings.TrimSpace(answer)
	}
	return identityURL, gatewayURL, nil
}

// authenticate resolves a session by credential shape: an OIDC token,
// a register-then-login flow, or a plain login.
func authenticate(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string) (string, error)) (*identityclient.Session, error) {
	switch {
	case opts.IDToken != "":
		return client.LoginExternal(ctx, opts.IDToken)
	case opts.Register:
		if err := registerFlow(ctx, client, opts, prompt); err != nil {
			return nil, err
		}
		return loginPrompt(ctx, client, opts, prompt)
	default:
		return loginPrompt(ctx, client, opts, prompt)
	}
}

func keyName(opts Options, prompt func(string) (string, error)) (string, error) {
	if opts.KeyName != "" {
		return opts.KeyName, nil
	}
	answer, err := prompt("Key name (label for this machine's LoomingKey)")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(answer)
	if name == "" {
		name = "default"
	}
	return name, nil
}

// storeProfile issues the LoomingKey and persists profile + secrets.
func storeProfile(ctx context.Context, client *identityclient.Client, session *identityclient.Session, identityURL, gatewayURL, keyName string, opts Options) (string, error) {
	issued, err := client.IssueKey(ctx, session, keyName)
	if err != nil {
		return "", fmt.Errorf("onboard: issue key: %w", err)
	}
	name := opts.ProfileName
	if name == "" {
		name = profile.DefaultName
	}
	creds, err := profile.LoadCredentials()
	if err != nil {
		return "", err
	}
	creds.SetLoomingKey(name, issued.Key)
	creds.SetSession(name, profile.Session{Token: session.Token, ExpiresAt: session.ExpiresAt})
	if err := creds.Save(); err != nil {
		return "", err
	}
	if err := profile.Save(&profile.Profile{
		Name:          name,
		IdentityURL:   identityURL,
		GatewayURL:    gatewayURL,
		CredentialKey: name,
	}); err != nil {
		return "", err
	}
	return name, nil
}

func registerFlow(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string) (string, error)) error {
	username := opts.Username
	if username == "" {
		answer, err := prompt("Choose a username")
		if err != nil {
			return err
		}
		username = strings.TrimSpace(answer)
	}
	password := opts.Password
	if password == "" {
		answer, err := prompt("Choose a password (min 12 chars)")
		if err != nil {
			return err
		}
		password = strings.TrimSpace(answer)
	}
	invite := opts.InviteToken
	if invite == "" {
		answer, err := prompt("Invite token (leave empty if the deployment allows open registration)")
		if err != nil {
			return err
		}
		invite = strings.TrimSpace(answer)
	}
	if err := client.Register(ctx, username, password, invite); err != nil {
		if identityclient.IsCode(err, "registration_forbidden") {
			return errors.New("this deployment does not allow self-registration — ask an admin for an account, then re-run without --register")
		}
		return fmt.Errorf("onboard: register: %w", err)
	}
	return nil
}

func loginPrompt(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string) (string, error)) (*identityclient.Session, error) {
	username := opts.Username
	if username == "" {
		answer, err := prompt("Username")
		if err != nil {
			return nil, err
		}
		username = strings.TrimSpace(answer)
	}
	password := opts.Password
	if password == "" {
		answer, err := prompt("Password")
		if err != nil {
			return nil, err
		}
		password = strings.TrimSpace(answer)
	}
	session, err := client.Login(ctx, username, password)
	if err != nil {
		if identityclient.IsCode(err, "invalid_credentials") {
			return nil, errors.New("invalid credentials — if you have no account yet, re-run with --register (or ask an admin)")
		}
		return nil, err
	}
	return session, nil
}

// LinePrompter reads one line from stdin (bufio), for interactive use.
func LinePrompter(in *bufio.Reader) func(string) (string, error) {
	return func(question string) (string, error) {
		fmt.Printf("%s: ", question)
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}
