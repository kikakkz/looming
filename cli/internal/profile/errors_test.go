// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingProfile(t *testing.T) {
	withFakeHome(t)
	if _, err := Load("ghost"); err == nil {
		t.Fatal("missing profile must error")
	}
}

func TestSaveMarshalAndWriteErrors(t *testing.T) {
	withFakeHome(t)
	// Unmarshalable is impossible via yaml; write failure needs a
	// read-only file — use a profile name whose parent path is a file.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	blocker := filepath.Join(home, ".looming", "profiles")
	if err := os.MkdirAll(filepath.Join(home, ".looming"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Save(&Profile{Name: "x", GatewayURL: "g", IdentityURL: "i", CredentialKey: "x"})
	if err == nil {
		t.Fatal("write under a non-directory must fail")
	}
}

func TestCredentialsCorruptFile(t *testing.T) {
	withFakeHome(t)
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(":\n:: bad yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(); err == nil {
		t.Fatal("corrupt credentials must error")
	}
}

func TestSessionMissingAndBoundary(t *testing.T) {
	withFakeHome(t)
	c, _ := LoadCredentials()
	if _, err := c.Session("nope"); err == nil {
		t.Fatal("missing session must error")
	}
	c.SetSession("edge", Session{Token: "t", ExpiresAt: time.Now().Add(time.Second)})
	if _, err := c.Session("edge"); err != nil {
		t.Fatalf("live session must resolve: %v", err)
	}
}

func TestCredentialsSaveMkdirFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// ~/.looming as a FILE makes MkdirAll(profile dir) fail inside
	// Save's own MkdirAll of the parent.
	if err := os.WriteFile(filepath.Join(home, ".looming"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Credentials{LoomingKeys: map[string]string{"k": "v"}}
	if err := c.Save(); err == nil {
		t.Fatal("save with a file in place of ~/.looming must fail")
	}
}
