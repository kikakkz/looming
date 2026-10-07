// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"os"
	"strings"
	"testing"
	"time"
)

// withFakeHome points HOME at a temp dir so the store tests never
// touch the developer's real ~/.looming.
func withFakeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows safety, no-op elsewhere
}

func TestProfileRoundTrip(t *testing.T) {
	withFakeHome(t)
	in := &Profile{Name: "prod", GatewayURL: "http://gw:8080", IdentityURL: "http://id:8081", CredentialKey: "prod"}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	got, err := Load("prod")
	if err != nil {
		t.Fatal(err)
	}
	if *got != *in {
		t.Fatalf("round trip mismatch: %+v vs %+v", got, in)
	}
	info, err := os.Stat(mustPath(t, "prod"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("profile must be 0600, got %o", perm)
	}
}

func mustPath(t *testing.T, name string) string {
	t.Helper()
	p, err := Path(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProfileRejectsBadNames(t *testing.T) {
	if _, err := Path("../escape"); err == nil {
		t.Fatal("path traversal name must fail")
	}
	if _, err := Path(""); err == nil {
		t.Fatal("empty name must fail")
	}
}

func TestCredentialsFirstRunEmpty(t *testing.T) {
	withFakeHome(t)
	c, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.LoomingKeys) != 0 {
		t.Fatalf("first run must be empty, got %v", c.LoomingKeys)
	}
}

func TestCredentialsRoundTripAndModes(t *testing.T) {
	withFakeHome(t)
	c, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	c.SetLoomingKey("default", "lk-abc")
	c.SetSession("default", Session{Token: "tok", ExpiresAt: time.Now().Add(time.Hour)})
	if saveErr := c.Save(); saveErr != nil {
		t.Fatal(saveErr)
	}
	again, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key, err := again.LoomingKey("default")
	if err != nil || key != "lk-abc" {
		t.Fatalf("key mismatch: %q %v (%v)", key, again.LoomingKeys, err)
	}
	if _, err := again.Session("default"); err != nil {
		t.Fatalf("session: %v", err)
	}
	if _, err := again.LoomingKey("missing"); err == nil {
		t.Fatal("missing key must error")
	}
}

func TestSessionExpiry(t *testing.T) {
	withFakeHome(t)
	c, _ := LoadCredentials()
	c.SetSession("old", Session{Token: "tok", ExpiresAt: time.Now().Add(-time.Hour)})
	if _, err := c.Session("old"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired session must error, got %v", err)
	}
}

func TestDefaultProfile(t *testing.T) {
	withFakeHome(t)
	if _, err := Default(); err == nil {
		t.Fatal("missing default profile must error")
	}
	if err := Save(&Profile{Name: DefaultName, GatewayURL: "g", IdentityURL: "i", CredentialKey: DefaultName}); err != nil {
		t.Fatal(err)
	}
	p, err := Default()
	if err != nil || p.Name != DefaultName {
		t.Fatalf("default: %v %+v", err, p)
	}
}
