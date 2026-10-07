// SPDX-License-Identifier: Apache-2.0

package agentcfg

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	if _, err := Lookup("nope"); err == nil {
		t.Fatal("unknown agent must error")
	}
	if names := Names(); len(names) != 1 || names[0] != "kimi-code" {
		t.Fatalf("registry: %v", names)
	}
	kimi, err := Lookup("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	block := kimi.RenderBlock("prod", "http://gw:8080", "k3", 131072, "lk-x")
	for _, want := range []string{"prod", `[providers.looming]`, "k3", `type = "openai"`, "http://gw:8080/v1", "lk-x", `[models."looming/k3"]`, "max_context_size = 131072", "kimi -m looming/k3"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path, err := kimi.ConfigPath()
	if err != nil || !strings.HasSuffix(path, filepath.Join(".kimi-code", "config.toml")) {
		t.Fatalf("config path: %v %q", err, path)
	}
	// Unfenced copies of EITHER owned table abort Apply with a
	// recoverable conflict instead of writing duplicate TOML tables.
	conflict := kimiCode{}
	if err := conflict.CheckConflict("[providers.looming]\ntype = \"openai\"\n"); err == nil {
		t.Fatalf("unfenced provider table must be a conflict")
	}
	if err := conflict.CheckConflict(`[models."looming/k3"]` + "\nprovider = \"x\"\n"); err == nil {
		t.Fatalf("unfenced model table must be a conflict")
	}
	if err := conflict.CheckConflict("# clean\n"); err != nil {
		t.Fatalf("clean config must not conflict: %v", err)
	}
	// A copy INSIDE the managed fence is the block's own business.
	fenced := fenceStart + "\n" + providerKey + "\n" + fenceEnd + "\ntail\n"
	if err := conflict.CheckConflict(fenced); err != nil {
		t.Fatalf("fenced managed content must not conflict: %v", err)
	}
	// KIMI_CODE_HOME relocates the config.
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	if path, err := kimi.ConfigPath(); err != nil || !strings.HasSuffix(path, "config.toml") {
		t.Fatalf("KIMI_CODE_HOME override: %v %q", err, path)
	}
	t.Setenv("KIMI_CODE_HOME", "")
}
