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
	"os"
	"strings"

	"golang.org/x/term"

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
func Run(ctx context.Context, opts Options, prompt func(string, bool) (string, error)) (string, error) {
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
func collectEndpoints(opts Options, prompt func(string, bool) (string, error)) (identityURL, gatewayURL string, err error) {
	identityURL = opts.IdentityURL
	if identityURL == "" {
		answer, askErr := prompt("Identity endpoint (from your cluster's guide page)", false)
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
		answer, askErr := prompt("Gateway endpoint (from the guide page)", false)
		if askErr != nil {
			return "", "", askErr
		}
		gatewayURL = strings.TrimSpace(answer)
	}
	return identityURL, gatewayURL, nil
}

// authenticate resolves a session by credential shape: an OIDC token,
// a register-then-login flow, or a plain login.
func authenticate(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string, bool) (string, error)) (*identityclient.Session, error) {
	switch {
	case opts.IDToken != "":
		return client.LoginExternal(ctx, opts.IDToken)
	case opts.Register:
		username, password, err := registerFlow(ctx, client, opts, prompt)
		if err != nil {
			return nil, err
		}
		// The registration credentials authenticate the follow-up
		// login — interactive users must not type them twice
		// (CodeRabbit review on PR #142).
		loginOpts := opts
		loginOpts.Username, loginOpts.Password = username, password
		return loginPrompt(ctx, client, loginOpts, prompt)
	default:
		return loginPrompt(ctx, client, opts, prompt)
	}
}

func keyName(opts Options, prompt func(string, bool) (string, error)) (string, error) {
	if opts.KeyName != "" {
		return opts.KeyName, nil
	}
	answer, err := prompt("Key name (label for this machine's LoomingKey)", false)
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

func registerFlow(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string, bool) (string, error)) (username, password string, err error) {
	username = opts.Username
	if username == "" {
		answer, askErr := prompt("Choose a username", false)
		if askErr != nil {
			return "", "", askErr
		}
		username = strings.TrimSpace(answer)
	}
	password = opts.Password
	if password == "" {
		answer, askErr := prompt("Choose a password (min 12 chars)", true)
		if askErr != nil {
			return "", "", askErr
		}
		password = strings.TrimSpace(answer)
	}
	invite := opts.InviteToken
	if invite == "" {
		answer, askErr := prompt("Invite token (leave empty if the deployment allows open registration)", false)
		if askErr != nil {
			return "", "", askErr
		}
		invite = strings.TrimSpace(answer)
	}
	if regErr := client.Register(ctx, username, password, invite); regErr != nil {
		if identityclient.IsCode(regErr, "registration_forbidden") {
			return "", "", errors.New("this deployment does not allow self-registration — ask an admin for an account, then re-run without --register")
		}
		return "", "", fmt.Errorf("onboard: register: %w", regErr)
	}
	return username, password, nil
}

func loginPrompt(ctx context.Context, client *identityclient.Client, opts Options, prompt func(string, bool) (string, error)) (*identityclient.Session, error) {
	username := opts.Username
	if username == "" {
		answer, err := prompt("Username", false)
		if err != nil {
			return nil, err
		}
		username = strings.TrimSpace(answer)
	}
	password := opts.Password
	if password == "" {
		answer, err := prompt("Password", true)
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

// LinePrompter reads one line from stdin; secret questions (passwords)
// are read with terminal echo off (golang.org/x/term ReadPassword,
// CodeRabbit security review on PR #142 — CWE-549).
func LinePrompter(in *bufio.Reader) func(string, bool) (string, error) {
	return func(question string, secret bool) (string, error) {
		fmt.Printf("%s: ", question)
		if secret {
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return "", err
			}
			return strings.TrimRight(string(raw), "\r\n"), nil
		}
		line, err := in.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}
