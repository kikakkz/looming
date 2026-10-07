// SPDX-License-Identifier: Apache-2.0

// Package agentcfg reconciles a looming profile INTO a local agent
// CLI's config (cli-l1 §5's AgentAdapter contract): idempotent
// managed-block merge, original backed up before the first mutation,
// --undo restores the pre-managed content of the managed region only.
// Adding an agent means adding an adapter here — no core changes.
package agentcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Adapter knows one agent CLI's config file and how to render the
// managed block for a profile.
type Adapter interface {
	// Name is the adapter registry key (kimi-code, codex, claude).
	Name() string
	// ConfigPath is the agent's config file (~ expansion included).
	ConfigPath() (string, error)
	// RenderBlock returns the managed-block lines (without the fence
	// markers) for one profile and model id (the gateway catalog's
	// model this configuration selects) plus the context window the
	// agent requires on every model entry.
	RenderBlock(profileName, gatewayURL, model string, contextSize int, loomKey string) string
	// CheckConflict reports a recoverable conflict between the managed
	// block and the existing config OUTSIDE the managed fence (for
	// example an unfenced provider table the block would redefine).
	// A conflict aborts Apply before any write.
	CheckConflict(existing string) error
}

// adapters is the registry. CLI-2 adds codex and claude.
var adapters = map[string]Adapter{}

// Register adds an adapter (package init of each adapter file).
func Register(a Adapter) { adapters[a.Name()] = a }

// Lookup finds an adapter by name.
func Lookup(name string) (Adapter, error) {
	a, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("cli: unknown agent %q (have: %s)", name, strings.Join(Names(), ", "))
	}
	return a, nil
}

// Names lists registered adapters, sorted.
func Names() []string {
	out := make([]string, 0, len(adapters))
	for name := range adapters {
		out = append(out, name)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

const (
	fenceStart = "# looming:managed:start"
	fenceEnd   = "# looming:managed:end"
)

// Apply merges the managed block into the agent's config: replaces an
// existing block in place, appends one when absent. Byte-idempotent:
// same input -> same output. Backs the config up before the FIRST
// mutation (backup kept until Undo). An existing config file is
// chmodded 0600 BEFORE the key lands in it (WriteFile never tightens
// an existing file's mode — CWE-732, CodeRabbit review on PR #142).
func Apply(a Adapter, profileName, gatewayURL, model string, contextSize int, loomKey string) (backupPath string, changed bool, err error) {
	path, err := a.ConfigPath()
	if err != nil {
		return "", false, err
	}
	// #nosec G304 -- path is the adapter's fixed config location.
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("cli: read %s config: %w", a.Name(), err)
	}
	if err == nil { // the file exists (possibly empty) — tighten before the key lands
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return "", false, fmt.Errorf("cli: tighten %s config permissions: %w", a.Name(), chmodErr)
		}
	}

	block := fenceStart + "\n" + a.RenderBlock(profileName, gatewayURL, model, contextSize, loomKey) + "\n" + fenceEnd
	if conflictErr := a.CheckConflict(string(original)); conflictErr != nil {
		return "", false, conflictErr
	}
	updated, hadBlock, mergeErr := mergeManaged(string(original), block)
	if mergeErr != nil {
		return "", false, mergeErr
	}
	if hadBlock && updated == string(original) {
		return "", false, nil // byte-idempotent: nothing to do
	}

	// Back up the pre-managed config on the FIRST mutation; later
	// applies leave the original backup untouched (it holds the
	// pre-managed state Undo restores).
	backup := ""
	if len(original) > 0 {
		backup = path + ".looming-backup"
		if _, statErr := os.Stat(backup); errors.Is(statErr, os.ErrNotExist) {
			if err := writeFile(backup, original, 0o600); err != nil {
				return "", false, fmt.Errorf("cli: backup %s config: %w", a.Name(), err)
			}
		}
	}
	_ = hadBlock
	if err := writeFileAtomic(path, []byte(updated)); err != nil {
		return "", false, fmt.Errorf("cli: write %s config: %w", a.Name(), err)
	}
	return backup, true, nil
}

// writeFileAtomic replaces the config via a same-directory temp file
// and rename: a mid-write error must never leave a truncated config
// behind (CodeRabbit review on PR #142). The temp file inherits the
// 0600 mode; rename over the target preserves it. A symlinked config
// path (dotfiles-managed setups) is followed first — renaming over
// the link itself would sever the link and strand the real config.
func writeFileAtomic(path string, data []byte) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".looming-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// Undo removes the managed block. Out-of-region edits made after
// configure survive — the removal excises only the fenced region, so
// the pre-managed content of that region (nothing, when configure
// appended the block) is restored by construction (cli-l1 §5's
// scoping). The first-mutation backup stays on disk for manual
// recovery; the operator deletes it when confident.
func Undo(a Adapter) (bool, error) {
	path, err := a.ConfigPath()
	if err != nil {
		return false, err
	}
	// #nosec G304 -- path is the adapter's fixed config location.
	original, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cli: read %s config: %w", a.Name(), err)
	}
	updated, hadBlock, err := mergeManaged(string(original), "")
	if err != nil {
		return false, err
	}
	if !hadBlock {
		return false, nil
	}
	if err := writeFileAtomic(path, []byte(updated)); err != nil {
		return false, fmt.Errorf("cli: write %s config: %w", a.Name(), err)
	}
	return true, nil
}

// mergeManaged replaces the fenced block with replacement (empty
// removes). An UNTERMINATED fence is rejected without touching the
// file: settings below the opening fence are out-of-region content
// the removal contract must preserve, and guessing where the block
// ends would gamble them away (CodeRabbit review on PR #142).
func mergeManaged(content, replacement string) (string, bool, error) {
	start := strings.Index(content, fenceStart)
	if start < 0 {
		if replacement == "" {
			return content, false, nil
		}
		sep := "\n"
		if content != "" && !strings.HasSuffix(content, "\n") {
			sep = "\n\n"
		}
		return content + sep + replacement + "\n", false, nil
	}
	end := strings.Index(content[start:], fenceEnd)
	if end < 0 {
		return "", true, fmt.Errorf("cli: %s config has an unterminated %q fence — fix the file manually; refusing to change it", "agent", fenceStart)
	}
	endAbs := start + end + len(fenceEnd)
	head, tail := content[:start], content[endAbs:]
	if replacement == "" {
		return head + strings.TrimLeft(tail, "\n"), true, nil
	}
	return head + replacement + tail, true, nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
