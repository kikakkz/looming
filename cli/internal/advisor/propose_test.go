// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
)

// proposeFixture is the reasoned flow's stage: gw-1 (cloud, egress,
// big), app-1 (lan, egress, mid), app-2 (lan, NO egress, mid) — and
// gateway-front already placed on gw-1. identityd needs egress so
// app-2 is a guardrail trap; topologyd fits anywhere.
const proposeFixture = `version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
hosts:
  - id: gw-1
    address: 10.0.0.11
    capabilities:
      hardware: {cpu_cores: 8, memory_mb: 16384, disk_gb: 200, arch: x86_64}
      network: {zone: cloud, egress: true}
  - id: app-1
    address: 10.0.0.12
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 8192, disk_gb: 100, arch: x86_64}
      network: {zone: lan, egress: true}
  - id: app-2
    address: 10.0.0.13
    capabilities:
      hardware: {cpu_cores: 4, memory_mb: 8192, disk_gb: 100, arch: x86_64}
      network: {zone: lan, egress: false}
placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
`

// stageProposeFixture writes the fixture file, points HOME at a temp
// dir (the session record), and returns the file path + loaded config.
func stageProposeFixture(t *testing.T) (string, *config.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "topology.yaml")
	require.NoError(t, os.WriteFile(path, []byte(proposeFixture), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return path, cfg
}

func proposeProfiles(t *testing.T) map[string]padvisor.Profile {
	t.Helper()
	profiles, err := padvisor.Parse(padvisor.DefaultProfilesYAML())
	require.NoError(t, err)
	return profiles
}

// askScript serves canned answers in order; running dry is a test bug.
func askScript(answers ...string) func(string) (string, error) {
	i := 0
	return func(_ string) (string, error) {
		if i >= len(answers) {
			return "", errors.New("askScript: out of answers")
		}
		answer := answers[i]
		i++
		return answer, nil
	}
}

// chatScript serves recorded model responses in order.
type chatScript struct {
	responses []string
	requests  []padvisor.ChatRequest
}

func (c *chatScript) Chat(_ context.Context, req padvisor.ChatRequest) (*padvisor.ChatResponse, error) {
	if len(c.responses) == 0 {
		return nil, errors.New("chatScript: out of recorded responses")
	}
	c.requests = append(c.requests, req)
	content := c.responses[0]
	c.responses = c.responses[1:]
	return &padvisor.ChatResponse{Choices: []struct {
		Message padvisor.ChatMessage `json:"message"`
	}{{Message: padvisor.ChatMessage{Role: "assistant", Content: content}}}}, nil
}

// recordedProposal is the fixture-shaped model answer: identityd is
// new on app-1 (the allocator picks the lowest free listen port),
// gateway-front moves off the edge box keeping its published port.
// topologyd is deliberately absent: placing it would demand the
// guide-token env_file wiring (config.validateGuideWiring), which the
// advisor cannot invent — the guardrail's final validation would
// (correctly) refuse a candidate that spliced invalid.
const recordedProposal = `{"placements":[` +
	`{"component":"identityd","host":"app-1","reason":"LAN reachability to the state plane; egress declared"},` +
	`{"component":"gateway-front","host":"app-1","reason":"the edge box stays lean; app-1 has egress and headroom"}],"risks":["gw-1 now hosts only the state plane"]}`

func reasonDepsWith(client padvisor.LLMClient, ask func(string) (string, error), edit func(string) error) reasonIO {
	return reasonIO{client: client, ask: ask, edit: edit, now: time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)}
}

// readSessionLine reads the single session record the run appended.
func readSessionLine(t *testing.T) sessionRecord {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Join(home, sessionsDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// #nosec G304 -- the path is the session log the run just wrote.
	data, err := os.ReadFile(filepath.Join(home, sessionsDirName, entries[0].Name()))
	require.NoError(t, err)
	var rec sessionRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	return rec
}

// TestReasonConfirmWritesFileAndRecords: the happy path — the diff is
// previewed, the operator confirms, the file lands with the proposed
// placements (allocated ports for the new components), the session
// record carries the proposal and the decision, and apply stays the
// operator's next move.
func TestReasonConfirmWritesFileAndRecords(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	client := &chatScript{responses: []string{recordedProposal}}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "keep the edge lean", reasonDepsWith(client, askScript("c"), nil)))

	out := stdout.String()
	assert.Contains(t, out, "PROPOSED PLACEMENTS")
	assert.Contains(t, out, "identityd → app-1")
	assert.Contains(t, out, "--- "+path)
	assert.Contains(t, out, "+++ "+path+" (proposed)")
	assert.Contains(t, out, "never automatic")
	assert.Empty(t, stderr.String())

	// #nosec G304 -- the test reads the fixture file it staged.
	// #nosec G304 -- the test reads the fixture file it staged.
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(written)
	assert.NotContains(t, text, "host: gw-1\n    ports: {http: 8080}", "the moved placement left its old host")
	assert.Contains(t, text, "- component: identityd\n    host: app-1")
	assert.Contains(t, text, "http: 1024", "a brand-new component gets the lowest free listen port")
	assert.Contains(t, text, "- component: gateway-front\n    host: app-1")
	assert.True(t, strings.Index(text, "identityd") < strings.Index(text, "gateway-front"), "proposal rank order, then the file's")

	reloaded, err := config.Load(path)
	require.NoError(t, err)
	assert.Len(t, reloaded.Placements, 2)
	assert.Equal(t, "app-1", reloaded.Placements[1].Host)

	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonConfirmed, rec.Decision)
	require.NotNil(t, rec.Proposal)
	assert.Equal(t, "keep the edge lean", rec.Proposal.Preference)
	assert.Equal(t, 1, rec.Proposal.Attempts)
	require.Len(t, rec.Proposal.Placements, 2)
	assert.Equal(t, "identityd", rec.Proposal.Placements[0].Component)
	assert.NotEmpty(t, rec.Proposal.Placements[0].Reason)
	assert.Equal(t, []string{"gw-1 now hosts only the state plane"}, rec.Proposal.Risks)
}

// TestReasonAbortLeavesDiskUntouched: abort is a no-op on the file but
// still an honest record.
func TestReasonAbortLeavesDiskUntouched(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	// #nosec G304 -- the test reads the fixture file it staged.
	// #nosec G304 -- the test reads the fixture file it staged.
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	client := &chatScript{responses: []string{recordedProposal}}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "", reasonDepsWith(client, askScript("a"), nil)))

	// #nosec G304 -- the test reads the fixture file it staged.
	// #nosec G304 -- the test reads the fixture file it staged.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "abort never touches the file")
	assert.NotContains(t, stdout.String(), "topology file updated")

	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonAborted, rec.Decision)
	require.NotNil(t, rec.Proposal)
	assert.Empty(t, rec.Proposal.Preference)
}

// TestReasonRegenerateFoldsThePreferenceBack: one regeneration — the
// added preference reaches the second model call, the fresh proposal
// replaces the candidate, and the record quotes the folded preference.
func TestReasonRegenerateFoldsThePreferenceBack(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	replacement := `{"placements":[{"component":"identityd","host":"app-1","reason":"egress plus the new preference"},{"component":"gateway-front","host":"gw-1","reason":"back on the edge after all"}],"risks":[]}`
	client := &chatScript{responses: []string{recordedProposal, replacement}}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "keep the edge lean",
		reasonDepsWith(client, askScript("r", "app-1 has spare cpu", "c"), nil)))

	require.Len(t, client.requests, 2)
	secondDoc := client.requests[1].Messages[1].Content
	assert.Contains(t, secondDoc, "keep the edge lean; app-1 has spare cpu")

	// #nosec G304 -- the test reads the fixture file it staged.
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(written), "- component: gateway-front\n    host: gw-1")

	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonConfirmed, rec.Decision)
	assert.Equal(t, "keep the edge lean; app-1 has spare cpu", rec.Proposal.Preference)
	assert.Equal(t, 1, rec.Proposal.Attempts, "attempts count the last proposal round's guardrail tries; two model calls across the regeneration are visible in the request log")
}

// TestReasonEditRevalidatesAndLandsTheEditedFile: the operator edits
// the staged candidate (topologyd moves to gw-1), the edit passes
// validation, and the edited content is what confirm writes.
func TestReasonEditRevalidatesAndLandsTheEditedFile(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	client := &chatScript{responses: []string{recordedProposal}}
	editor := func(editedPath string) error {
		// #nosec G304 -- the path is the advisor's own staged temp file.
		content, err := os.ReadFile(editedPath)
		if err != nil {
			return err
		}
		updated := strings.Replace(string(content), "- component: gateway-front\n    host: app-1", "- component: gateway-front\n    host: gw-1", 1)
		if updated == string(content) {
			return errors.New("editor: fixture shape changed")
		}
		return os.WriteFile(editedPath, []byte(updated), 0o600)
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "", reasonDepsWith(client, askScript("e", "c"), editor)))

	// #nosec G304 -- the test reads the fixture file it staged.
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(written), "- component: gateway-front\n    host: gw-1")

	if _, err := config.Load(path); err != nil {
		t.Fatalf("edited file must stay a valid topology: %v", err)
	}
	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonEdited, rec.Decision)
}

// TestReasonEditRejectsInvalidResult: an editor session that breaks
// the topology is surfaced and nothing lands.
func TestReasonEditRejectsInvalidResult(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	// #nosec G304 -- the test reads the fixture file it staged.
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	client := &chatScript{responses: []string{recordedProposal, replacementProposalForEditTest()}}
	editor := func(editedPath string) error {
		return os.WriteFile(editedPath, []byte("version: 2\n"), 0o600)
	}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "",
		reasonDepsWith(client, askScript("e", "r", "", "a"), editor)))

	assert.Contains(t, stdout.String(), "not a valid topology")
	// #nosec G304 -- the test reads the fixture file it staged.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "no decision, no write")

	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonAborted, rec.Decision)
}

// replacementProposalForEditTest keeps the regenerate-after-failed-edit
// path honest: the second recorded response serves the same proposal.
func replacementProposalForEditTest() string {
	return recordedProposal
}

// TestReasonDegradesToTableAfterMaxRejections: the model insists on an
// egress-needing component on a no-egress host; three guardrailed
// attempts fail; the table answers and the record says why.
func TestReasonDegradesToTableAfterMaxRejections(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	// #nosec G304 -- the test reads the fixture file it staged.
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	violating := `{"placements":[{"component":"identityd","host":"app-2","reason":"it fits, trust me"}],"risks":[]}`
	client := &chatScript{responses: []string{violating, violating, violating}}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "", reasonDepsWith(client, askScript(), nil)))

	assert.Contains(t, stderr.String(), "falling back to the deterministic table")
	assert.Contains(t, stdout.String(), "COMPONENT")
	assert.Contains(t, stdout.String(), "FEASIBLE")
	// #nosec G304 -- the test reads the fixture file it staged.
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, before, after, "degraded sessions never touch the file")

	rec := readSessionLine(t)
	assert.Equal(t, decisionDegradedTable, rec.Decision)
	require.NotNil(t, rec.Proposal)
	assert.True(t, rec.Proposal.Degraded)
	assert.Len(t, rec.Proposal.Rejections, padvisor.MaxProposalAttempts)
	assert.Contains(t, rec.Proposal.Rejections[0], "infeasible pair")
}

// TestReasonRegenerationDegradesMidLoop: a regeneration that exhausts
// the guardrail degrades from inside the decide loop — the table
// renders, the record carries the fresh rejection trail.
func TestReasonRegenerationDegradesMidLoop(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	violating := `{"placements":[{"component":"identityd","host":"app-2","reason":"still wrong"}],"risks":[]}`
	client := &chatScript{responses: []string{recordedProposal, violating, violating, violating}}

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReason(&stdout, &stderr, path, cfg, proposeProfiles(t), "",
		reasonDepsWith(client, askScript("r", "try anyway", ""), nil)))

	assert.Contains(t, stdout.String(), "COMPONENT")
	rec := readSessionLine(t)
	assert.Equal(t, decisionDegradedTable, rec.Decision)
	assert.Len(t, rec.Proposal.Rejections, padvisor.MaxProposalAttempts)
	assert.Equal(t, "try anyway", rec.Proposal.Preference)
}

// TestReasonUnknownChoiceKeepsAsking: an unrecognized answer re-prompts
// instead of acting.
func TestReasonUnknownChoiceKeepsAsking(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	client := &chatScript{responses: []string{recordedProposal}}

	var stdout, _ bytes.Buffer
	require.NoError(t, runReason(&stdout, &bytes.Buffer{}, path, cfg, proposeProfiles(t), "",
		reasonDepsWith(client, askScript("yes", "c"), nil)))

	assert.Contains(t, stdout.String(), `unrecognized choice "yes"`)
	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonConfirmed, rec.Decision)
}

// TestReasonAskErrorAborts: a closed stdin at the decide prompt is an
// abort, not a crash.
func TestReasonAskErrorAborts(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	// #nosec G304 -- the test reads the fixture file it staged.
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	client := &chatScript{responses: []string{recordedProposal}}

	var stdout, _ bytes.Buffer
	require.NoError(t, runReason(&stdout, &bytes.Buffer{}, path, cfg, proposeProfiles(t), "",
		reasonDepsWith(client, func(string) (string, error) { return "", errors.New("stdin closed") }, nil)))

	assert.Contains(t, stdout.String(), "aborting")
	// #nosec G304 -- the test reads the fixture file it staged.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonAborted, rec.Decision)
}

// TestReasonEditWithoutEditorExplainsItself: choosing edit with no
// $EDITOR wired is a surfaced explanation, then the loop continues.
func TestReasonEditWithoutEditorExplainsItself(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	client := &chatScript{responses: []string{recordedProposal}}

	var stdout, _ bytes.Buffer
	require.NoError(t, runReason(&stdout, &bytes.Buffer{}, path, cfg, proposeProfiles(t), "",
		reasonDepsWith(client, askScript("e", "a"), nil)))

	assert.Contains(t, stdout.String(), "no editor configured")
	rec := readSessionLine(t)
	assert.Equal(t, decisionReasonAborted, rec.Decision)
}

// TestPlacementsForProposalMovesKeepWiringAndAllocatePorts: the
// mechanical conversion the model never sees — moved components keep
// their published ports and wiring; new ones get the lowest free
// listen port; unproposed placements stay last in file order.
func TestPlacementsForProposalMovesKeepWiringAndAllocatePorts(t *testing.T) {
	stageProposeFixture(t)
	profiles := proposeProfiles(t)

	withState, err := config.Load(writeGenesisTopology(t, `
version: 1
access: {mode: public, transport: direct, endpoint: "10.0.0.10"}
state:
  postgres: {image: "postgres:16-alpine", env_file: "/etc/looming/postgres.env", data_dir: "/var/lib/looming/postgres", port: 5432}
hosts:
  - id: gw-1
    address: 10.0.0.11
  - id: app-1
    address: 10.0.0.12
placements:
  - component: gateway-front
    host: gw-1
    ports: {http: 8080}
`))
	require.NoError(t, err)

	// gateway-front moves to app-1 keeping its published port (the
	// allocator only frees slots on the target host); identityd is new
	// on gw-1, where 8080 and the state plane's 5432 are both taken.
	proposal := &padvisor.Proposal{Placements: []padvisor.ProposedPlacement{
		{Component: "gateway-front", Host: "app-1"},
		{Component: "identityd", Host: "gw-1"},
	}}
	placements, err := placementsForProposal(withState, profiles, proposal)
	require.NoError(t, err)
	require.Len(t, placements, 2)
	assert.Equal(t, "app-1", placements[0].Host)
	assert.Equal(t, map[string]int{"http": 8080}, placements[0].Ports, "moved components keep their published ports")
	assert.Equal(t, "gw-1", placements[1].Host)
	assert.Equal(t, 1024, placements[1].Ports["http"], "8080 and the state plane's 5432 are both avoided on gw-1")

}

// TestReasonRecordQuotesNoCredential: the redaction line extends to
// the reasoned record — a preference that accidentally quotes the
// genesis key would be the exact leak #143 forbids, so the advisor
// scrubs it before anything is written.
func TestReasonRecordQuotesNoCredential(t *testing.T) {
	path, cfg := stageProposeFixture(t)
	client := &chatScript{responses: []string{recordedProposal}}

	scrub := func(s string) string { return padvisor.Redact(s, genesisKeyFixture) }
	deps := reasonDepsWith(client, askScript("c"), nil)
	deps.redact = scrub

	var stdout bytes.Buffer
	require.NoError(t, runReason(&stdout, &bytes.Buffer{}, path, cfg, proposeProfiles(t), genesisKeyFixture, deps))
	assert.NotContains(t, stdout.String(), genesisKeyFixture, "the preview is scrubbed too")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	raw, err := os.ReadDir(filepath.Join(home, sessionsDirName))
	require.NoError(t, err)
	require.Len(t, raw, 1)
	// #nosec G304 -- the path is the session log the run just wrote.
	content, err := os.ReadFile(filepath.Join(home, sessionsDirName, raw[0].Name()))
	require.NoError(t, err)
	assert.NotContains(t, string(content), genesisKeyFixture, "the record must be scrubbed of key material")
	assert.Contains(t, string(content), "***")
}
