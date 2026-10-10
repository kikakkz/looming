// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kikakkz/looming/platform/go/config"
)

// advisorDirName is the advisor state directory under the operator's
// home; the genesis lifecycle state file lives directly in it, and the
// append-only session log at advisorDirName/sessions/<UTC RFC3339>.jsonl
// (advisor-l1 §6). The directory is mode 0700 — the same discipline as
// credentials.yaml (AD-37 §5): the record may quote operator free text
// from slice 1.2 onward, so it sits in the credentials class from the
// start.
const advisorDirName = ".looming/advisor"

// sessionsDirName is the append-only session log's home.
const sessionsDirName = advisorDirName + "/sessions"

// sessionRecord is one appended JSONL line: the facts snapshot, the
// profile-set hash, the matrix summary, and the human decision. Slice
// 1.2 extends it with the raw model output; the append-only shape does
// not change.
type sessionRecord struct {
	Timestamp     string        `json:"ts"`
	File          string        `json:"file"`
	ProfileSHA256 string        `json:"profile_sha256"`
	FactsSnapshot []config.Host `json:"facts_snapshot"`
	Matrix        matrixSummary `json:"matrix"`
	Decision      string        `json:"decision"`
}

// matrixSummary is the session record's feasibility-matrix digest:
// pair count split into feasible and infeasible.
type matrixSummary struct {
	Pairs      int `json:"pairs"`
	Feasible   int `json:"feasible"`
	Infeasible int `json:"infeasible"`
}

// userHomeDir resolves the operator's home directory; it is a variable
// so tests can exercise the resolution-failure path (os.UserHomeDir
// itself cannot be made to fail on a unix test host).
var userHomeDir = os.UserHomeDir

// appendSessionRecord appends one session record to the log file
// named by the record's timestamp, creating the 0700 directory and
// 0600 file on first use. Append-only: an existing file is opened for
// append, never rewritten.
func appendSessionRecord(rec sessionRecord) error {
	home, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("advise: resolve home directory: %w", err)
	}
	return appendSessionRecordTo(filepath.Join(home, sessionsDirName), rec)
}

// appendSessionRecordTo is the path-injected body: tests aim it at a
// temp home. It returns the path-independent errors only — the caller
// owns the warning policy.
func appendSessionRecordTo(dir string, rec sessionRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("advise: create session directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode untouched — widen a
	// previously-created or hand-edged directory back to the
	// credentials-class contract (AD-37 §5) before the append.
	// #nosec G302 -- directory mode, not a file: 0700 is the contract.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("advise: secure session directory: %w", err)
	}
	// #nosec G304 -- the path is the operator's own advisor state directory.
	f, err := os.OpenFile(filepath.Join(dir, rec.Timestamp+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("advise: open session log: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Encoder over Marshal+Write: one error path covers both the
	// encode and the append.
	if err := json.NewEncoder(f).Encode(rec); err != nil {
		return fmt.Errorf("advise: append session record: %w", err)
	}
	return nil
}
