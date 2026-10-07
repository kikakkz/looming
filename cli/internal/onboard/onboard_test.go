// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kikakkz/looming/cli/internal/profile"
)

// fakeIdentity is the minimal identityd surface onboard uses.
type fakeIdentity struct {
	srv        *httptest.Server
	registered map[string]string
	keys       int
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	f := &fakeIdentity{registered: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/self/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			InviteToken string `json:"invite_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.InviteToken == "blocked" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"registration_forbidden"}}`))
			return
		}
		if body.Username == "taken" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"username_taken"}}`))
			return
		}
		f.registered[body.Username] = body.Password
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/v1/self/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.registered[body.Username] != body.Password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"session-tok","expires_at":"2027-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/v1/self/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.keys++
		_, _ = w.Write([]byte(`{"id":"k-1","key":"lk-raw-value"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func scripted(answers map[string]string) func(string, bool) (string, error) {
	return func(question string, _ bool) (string, error) {
		for key, answer := range answers {
			if containsStr(question, key) {
				return answer, nil
			}
		}
		return "", errors.New("unexpected prompt: " + question)
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return len(needle) == 0
}

func TestOnboardRegisterThenConfigureFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		Register:    true,
		KeyName:     "laptop",
	}
	answers := map[string]string{
		"username": "ker",
		"password": "correct horse battery staple",
		"Invite":   "",
		"Username": "ker",
		"Password": "correct horse battery staple",
	}
	name, err := Run(t.Context(), opts, scripted(answers))
	if err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if name != profile.DefaultName {
		t.Fatalf("want default profile, got %q", name)
	}
	p, err := profile.Default()
	if err != nil {
		t.Fatal(err)
	}
	if p.GatewayURL != "http://gw:8080" || p.CredentialKey != profile.DefaultName {
		t.Fatalf("profile fields: %+v", p)
	}
	creds, err := profile.LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key, err := creds.LoomingKey(profile.DefaultName)
	if err != nil || key != "lk-raw-value" {
		t.Fatalf("stored key: %q %v", key, err)
	}
	if _, err := creds.Session(profile.DefaultName); err != nil {
		t.Fatalf("session cache: %v", err)
	}
	if fake.keys != 1 {
		t.Fatalf("want one issued key, got %d", fake.keys)
	}
}

func TestOnboardLoginOnlyFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	fake.registered["existing"] = "pw"

	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		KeyName:     "k",
	}
	answers := map[string]string{
		"Username": "existing",
		"Password": "pw",
	}
	if _, err := Run(t.Context(), opts, scripted(answers)); err != nil {
		t.Fatalf("login-only onboard: %v", err)
	}
}

func TestOnboardLoginFailureHintsRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
	}
	answers := map[string]string{
		"Username": "ghost",
		"Password": "nope",
	}
	_, err := Run(t.Context(), opts, scripted(answers))
	if err == nil {
		t.Fatal("unknown login must fail")
	}
	if !containsStr(err.Error(), "--register") {
		t.Fatalf("failure must hint --register, got %v", err)
	}
}

func TestOnboardExternalTokenFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		IDToken:     "id-tok",
		KeyName:     "k",
	}
	// The fake's login ignores id_token but accepts any login — the
	// point here is that no prompts fire when every option is set.
	if _, err := Run(t.Context(), opts, scripted(map[string]string{})); err != nil {
		t.Fatalf("external flow: %v", err)
	}
}

func TestOnboardMissingEndpointsPrompt(t *testing.T) {
	opts := Options{}
	_, err := Run(t.Context(), opts, func(question string, _ bool) (string, error) {
		return "", nil // empty answers everywhere
	})
	if err == nil {
		t.Fatal("empty identity endpoint must fail")
	}
}

func TestOnboardRegisterForbiddenHintsAdmin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		Register:    true,
		InviteToken: "blocked",
	}
	answers := map[string]string{
		"username": "blocked",
		"password": "whatever12345",
	}
	_, err := Run(t.Context(), opts, scripted(answers))
	if err == nil || !containsStr(err.Error(), "ask an admin") {
		t.Fatalf("forbidden register must hint admin, got %v", err)
	}
}

func TestOnboardPromptErrorPropagates(t *testing.T) {
	opts := Options{IdentityURL: "http://x", GatewayURL: "http://y"}
	promptErr := errors.New("input closed")
	_, err := Run(t.Context(), opts, func(string, bool) (string, error) {
		return "", promptErr
	})
	if err == nil {
		t.Fatal("prompt error must propagate")
	}
}

func TestOnboardIssueKeyFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fake := newFakeIdentity(t)
	fake.srv.Close() // every request now fails
	opts := Options{
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		Register:    true,
	}
	answers := map[string]string{
		"username": "ker", "password": "correct horse battery",
		"Invite": "", "Username": "ker", "Password": "correct horse battery",
		"Key name": "k",
	}
	if _, err := Run(t.Context(), opts, scripted(answers)); err == nil {
		t.Fatal("unreachable identity must fail onboarding")
	}
}

func TestLinePrompterReadsAnswers(t *testing.T) {
	prompt := LinePrompter(bufio.NewReader(strings.NewReader("hello world\n")))
	answer, err := prompt("anything", false)
	if err != nil || answer != "hello world" {
		t.Fatalf("line prompter: %q %v", answer, err)
	}
}

func TestOnboardEndpointPromptErrors(t *testing.T) {
	// Identity prompt fails.
	_, err := Run(t.Context(), Options{}, func(string, bool) (string, error) {
		return "", errors.New("no input")
	})
	if err == nil {
		t.Fatal("identity prompt error must propagate")
	}
	// Gateway prompt fails.
	_, err = Run(t.Context(), Options{IdentityURL: "http://id"}, func(question string, _ bool) (string, error) {
		if strings.Contains(question, "Gateway") {
			return "", errors.New("no input")
		}
		return "http://id", nil
	})
	if err == nil {
		t.Fatal("gateway prompt error must propagate")
	}
}

func TestOnboardKeyNamePromptAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	fake := newFakeIdentity(t)
	fake.registered["e"] = "p"
	opts := Options{IdentityURL: fake.srv.URL, GatewayURL: "http://gw"}
	answers := map[string]string{
		"Username": "e", "Password": "p", "Key name": "",
	}
	name, err := Run(t.Context(), opts, scripted(answers))
	if err != nil || name != profile.DefaultName {
		t.Fatalf("default key name flow: %v %q", err, name)
	}
	creds, _ := profile.LoadCredentials()
	if _, err := creds.LoomingKey(profile.DefaultName); err != nil {
		t.Fatalf("key stored under default profile: %v", err)
	}
}

func TestOnboardRejectsBadProfileNameBeforeKeyIssuance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	fake := newFakeIdentity(t)
	opts := Options{
		ProfileName: "../escape",
		IdentityURL: fake.srv.URL,
		GatewayURL:  "http://gw:8080",
		Register:    true,
	}
	if _, err := Run(t.Context(), opts, scripted(map[string]string{})); err == nil {
		t.Fatal("invalid profile name must fail before any key issuance")
	}
	if fake.keys != 0 {
		t.Fatalf("no key may be issued for a bad profile name, got %d", fake.keys)
	}
}

func TestOnboardRejectsEmptyGateway(t *testing.T) {
	opts := Options{IdentityURL: "http://id"}
	_, err := Run(t.Context(), opts, func(question string, _ bool) (string, error) {
		return "", nil // gateway answer empty
	})
	if err == nil || !containsStr(err.Error(), "gateway") {
		t.Fatalf("empty gateway must fail: %v", err)
	}
}
