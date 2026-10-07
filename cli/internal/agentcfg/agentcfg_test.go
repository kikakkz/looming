// SPDX-License-Identifier: Apache-2.0

package agentcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testAdapter struct {
	path     string
	conflict error
}

func (t testAdapter) Name() string { return "test" }
func (t testAdapter) ConfigPath() (string, error) {
	return t.path, nil
}
func (t testAdapter) RenderBlock(profile, gateway, model string, contextSize int, key string) string {
	return fmt.Sprintf("profile=%s gateway=%s model=%s ctx=%d key=%s", profile, gateway, model, contextSize, key)
}

func (t testAdapter) CheckConflict(string) error { return t.conflict }

func tempAdapter(t *testing.T, existing string) testAdapter {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if existing != "" {
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return testAdapter{path: path}
}

func readConfig(t *testing.T, a testAdapter) string {
	t.Helper()
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyTightensExistingConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// #nosec G306 -- the world-readable file is the test subject.
	if err := os.WriteFile(path, []byte("# world-readable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := testAdapter{path: path}
	if _, _, err := Apply(a, "d", "g", "k3", 131072, "lk"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("existing config must be tightened to 0600 before the key lands, got %o", perm)
	}
}

func TestApplyRefusesUnfencedProviderConflict(t *testing.T) {
	a := tempAdapter(t, "[other]\nx = 1\n")
	a.conflict = errors.New("unfenced [providers.looming]")
	if _, _, err := Apply(a, "d", "g", "k3", 131072, "lk"); err == nil {
		t.Fatalf("conflict must abort Apply")
	}
	got := readConfig(t, a)
	if !strings.Contains(got, "[other]") {
		t.Fatalf("conflict must not modify the file:\n%s", got)
	}
}

func TestApplyFirstRunAppendsBlock(t *testing.T) {
	a := tempAdapter(t, "# my config\n")
	_, changed, err := Apply(a, "default", "http://gw:8080", "k3", 131072, "lk-secret")
	if err != nil || !changed {
		t.Fatalf("apply: %v changed=%v", err, changed)
	}
	got := readConfig(t, a)
	if !strings.Contains(got, "# my config") || !strings.Contains(got, fenceStart) ||
		!strings.Contains(got, "profile=default") || !strings.Contains(got, "key=lk-secret") {
		t.Fatalf("merged config missing pieces:\n%s", got)
	}
	// Backup captured the pre-managed content.
	backup, err := os.ReadFile(a.path + ".looming-backup")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != "# my config\n" {
		t.Fatalf("backup content: %q", backup)
	}
}

func TestApplyIsByteIdempotent(t *testing.T) {
	a := tempAdapter(t, "")
	if _, _, err := Apply(a, "default", "http://gw:8080", "k3", 131072, "lk-secret"); err != nil {
		t.Fatal(err)
	}
	first := readConfig(t, a)
	_, changed, err := Apply(a, "default", "http://gw:8080", "k3", 131072, "lk-secret")
	if err != nil || changed {
		t.Fatalf("second apply must be a no-op: %v changed=%v", err, changed)
	}
	if readConfig(t, a) != first {
		t.Fatalf("bytes moved on a no-op apply")
	}
}

func TestApplyReplacesExistingBlockOnly(t *testing.T) {
	a := tempAdapter(t, "head\n"+fenceStart+"\nold\n"+fenceEnd+"\ntail\n")
	if _, _, err := Apply(a, "prod", "http://gw2:8080", "k3", 131072, "lk-new"); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, a)
	if !strings.Contains(got, "head") || !strings.Contains(got, "tail") || !strings.Contains(got, "profile=prod") {
		t.Fatalf("surrounding content or new block missing:\n%s", got)
	}
	if strings.Contains(got, "old") {
		t.Fatalf("stale block content survived:\n%s", got)
	}
}

func TestUndoRestoresManagedRegionOnly(t *testing.T) {
	a := tempAdapter(t, "original\n")
	if _, _, err := Apply(a, "default", "http://gw:8080", "k3", 131072, "lk-secret"); err != nil {
		t.Fatal(err)
	}
	// The user edits outside the managed region after configure.
	if err := os.WriteFile(a.path, []byte(readConfig(t, a)+"# user note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Undo(a)
	if err != nil || !changed {
		t.Fatalf("undo: %v changed=%v", err, changed)
	}
	got := readConfig(t, a)
	if !strings.Contains(got, "original") || !strings.Contains(got, "# user note") {
		t.Fatalf("pre-managed region or user edit lost:\n%s", got)
	}
	if strings.Contains(got, fenceStart) {
		t.Fatalf("managed block survived undo:\n%s", got)
	}
}

func TestUndoWithoutBlockIsNoOp(t *testing.T) {
	a := tempAdapter(t, "untouched\n")
	changed, err := Undo(a)
	if err != nil || changed {
		t.Fatalf("undo without a block must be a no-op: %v", err)
	}
}

func TestApplyWriteFailure(t *testing.T) {
	a := testAdapter{path: filepath.Join(t.TempDir(), "missing", "config.toml")}
	// ReadFile fails (not-exist is fine), MkdirAll succeeds, Write
	// succeeds here — instead force a non-directory config path.
	bad := testAdapter{path: a.path}
	if err := os.WriteFile(filepath.Dir(a.path)+".block", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = bad
	// A simpler failure: config path inside a file-as-directory.
	a2 := testAdapter{path: filepath.Join(t.TempDir(), "asfile", "config.toml")}
	if err := os.WriteFile(filepath.Dir(a2.path), []byte("x"), 0o600); err != nil {
		// mkdir asfile will fail because a file named "asfile" exists.
		t.Fatal(err)
	}
	_, _, err := Apply(a2, "p", "g", "k3", 131072, "k")
	if err == nil {
		t.Fatal("write into a file-as-directory must fail")
	}
}

func TestUndoReadFailure(t *testing.T) {
	a := testAdapter{path: filepath.Join(t.TempDir(), "asfile")}
	if err := os.WriteFile(a.path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ConfigPath points at an existing FILE; ReadFile succeeds, no
	// block -> no-op. The read-failure branch needs a disappearing
	// file: remove between read attempts is racy, so exercise the
	// not-exists branch instead (already no-op). This case pins the
	// file-as-config no-op.
	changed, err := Undo(a)
	if err != nil || changed {
		t.Fatalf("file-without-block undo must be a no-op: %v changed=%v", err, changed)
	}
}

func TestMergeManagedUnterminatedFenceRejected(t *testing.T) {
	if _, _, err := mergeManaged("head\n"+fenceStart+"\norphan\n", "newblock"); err == nil {
		t.Fatalf("unterminated fence must be rejected, not rewritten")
	}
	if _, _, err := mergeManaged("head\n"+fenceStart+"\norphan\n", ""); err == nil {
		t.Fatalf("unterminated fence removal must be rejected too")
	}
}

func TestWriteFileAtomicReplacesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// #nosec G306 -- the world-readable starting state is the test subject.
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new content")); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	// #nosec G304 -- path comes from t.TempDir().
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new content" {
		t.Fatalf("content: %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("atomic write must land 0600, got %o", perm)
	}
	// No temp files linger.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".looming-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileAtomicMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file standing where the parent directory must be created.
	if err := writeFileAtomic(filepath.Join(blocker, "config.toml"), []byte("y")); err == nil {
		t.Fatalf("write through a file-as-directory must fail")
	}
}

func TestUndoWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := fenceStart + "\nmanaged\n" + fenceEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Remove write permission on the directory so the atomic replace fails.
	// #nosec G302 -- the read-only directory is the test subject.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- restoring the temp directory for the harness.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	a := testAdapter{path: path}
	if _, err := Undo(a); err == nil {
		t.Fatalf("undo with an unwritable directory must fail")
	}
}
