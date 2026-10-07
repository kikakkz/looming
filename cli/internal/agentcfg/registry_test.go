// SPDX-License-Identifier: Apache-2.0

package agentcfg

import (
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
	block := kimi.RenderBlock("prod", "http://gw:8080", "lk-x")
	for _, want := range []string{"prod", "http://gw:8080/v1", "lk-x"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path, err := kimi.ConfigPath()
	if err != nil || !strings.HasSuffix(path, ".kimi/config.toml") {
		t.Fatalf("config path: %v %q", err, path)
	}
}
