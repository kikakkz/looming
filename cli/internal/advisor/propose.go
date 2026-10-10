// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/config"
	"github.com/kikakkz/looming/platform/go/render"
)

// Reasoned-session decision vocabulary, quoted verbatim by the session
// record and its consumers.
const (
	decisionReasonConfirmed = "reason-confirmed"
	decisionReasonEdited    = "reason-edited"
	decisionReasonAborted   = "reason-aborted"
	decisionDegradedTable   = "degraded-table"
)

// reasonIO is the reasoned flow's injected interaction surface: the
// model channel, the operator ask, the editor, and the redaction hook
// that scrubs channel credentials out of everything the model echoes
// back (reasons, risks, the quoted preference) before it reaches the
// terminal or the session record — production wires stdin/stdout and
// $EDITOR, tests script them.
type reasonIO struct {
	client padvisor.LLMClient
	ask    func(question string) (string, error)
	edit   func(path string) error
	redact func(string) string
	now    time.Time
}

// redactScrubs applies the hook when wired, else the identity — the
// zero reasonIO stays usable in tests that never quote credentials.
func (ioDeps reasonIO) redactScrubs(s string) string {
	if ioDeps.redact == nil {
		return s
	}
	return ioDeps.redact(s)
}

// runReason is `looming advise --reason` (advisor-l1 §3 steps 3–7):
// propose through the guardrail, degrade to the table when the hard
// layer keeps rejecting, else preview the splice as a unified diff and
// let the operator confirm (write — never auto-apply), edit,
// regenerate, or abort. The session record lands either way.
func runReason(stdout, stderr io.Writer, filePath string, cfg *config.Config, profiles map[string]padvisor.Profile, preference string, ioDeps reasonIO) error {
	verdicts := padvisor.Evaluate(cfg.Hosts, profiles)
	// #nosec G304 -- the path is the operator-supplied --file argument.
	disk, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("advise: read topology file: %w", err)
	}

	input := padvisor.ProposalInput{Verdicts: verdicts, Profiles: profiles, Hosts: cfg.Hosts, Preference: preference}
	propose := func(pref string) (*padvisor.ProposalResult, error) {
		input.Preference = pref
		return padvisor.ProposeWithGuardrail(context.Background(), ioDeps.client, input)
	}
	rebuild := func(proposal *padvisor.Proposal) ([]byte, error) {
		return buildCandidate(cfg, disk, profiles, proposal)
	}

	result, err := propose(preference)
	if err != nil {
		return fmt.Errorf("advise: propose: %w", err)
	}
	if result.Degraded {
		_, _ = fmt.Fprintln(stderr, "advise: the model's proposals kept violating hard constraints — falling back to the deterministic table")
		return finishReasoned(stdout, stderr, filePath, cfg.Hosts, verdicts, preference, result, decisionDegradedTable, ioDeps.redactScrubs, ioDeps.now)
	}

	candidate, err := rebuild(result.Proposal)
	if err != nil {
		return err
	}
	decision, final, finalPreference, finalResult := interactionLoop(stdout, ioDeps, filePath, disk, candidate, preference, propose, rebuild, result)

	if decision == decisionReasonConfirmed || decision == decisionReasonEdited {
		if err := writeConfirmed(filePath, final); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "advise: topology file updated — review it, then converge with `looming apply` (never automatic).\n")
	}
	return finishReasoned(stdout, stderr, filePath, cfg.Hosts, verdicts, finalPreference, finalResult, decision, ioDeps.redactScrubs, ioDeps.now)
}

// finishReasoned renders the table for degraded sessions and appends
// the session record — scrubbed of channel credentials first (the
// model may echo the quoted preference back into its reasons and
// risks); a record failure warns, it does not rewrite the exit
// contract.
func finishReasoned(stdout, stderr io.Writer, filePath string, hosts []config.Host, verdicts []padvisor.Verdict, preference string, result *padvisor.ProposalResult, decision string, redact func(string) string, now time.Time) error {
	if decision == decisionDegradedTable {
		if _, err := fmt.Fprint(stdout, renderTable(verdicts)); err != nil {
			_, _ = fmt.Fprintf(stderr, "advise: warning: table not written: %v\n", err)
			return nil
		}
	}
	rec := buildSessionRecord(filePath, hosts, verdicts, profileSHA256(padvisor.DefaultProfilesYAML()), now)
	rec.Decision = decision
	rec.Proposal = scrubProposalRecord(preference, result, redact)
	if err := appendSessionRecord(rec); err != nil {
		_, _ = fmt.Fprintf(stderr, "advise: warning: session record not written: %v\n", err)
	}
	return nil
}

// scrubProposalRecord narrows the guardrail result into the session
// record's shape, running every free-text field through the redaction
// hook: the preference, each placement's reason, the risks, and the
// rejection trail are exactly the fields that can quote operator free
// text or a model echo of it.
func scrubProposalRecord(preference string, result *padvisor.ProposalResult, redact func(string) string) *proposalRecord {
	rec := proposalRecordOf(preference, result)
	rec.Preference = redact(rec.Preference)
	for i := range rec.Placements {
		rec.Placements[i].Reason = redact(rec.Placements[i].Reason)
	}
	for i, risk := range rec.Risks {
		rec.Risks[i] = redact(risk)
	}
	for i, rejection := range rec.Rejections {
		rec.Rejections[i] = redact(rejection)
	}
	return rec
}

// proposalRecordOf narrows the guardrail result into the session
// record's shape.
func proposalRecordOf(preference string, result *padvisor.ProposalResult) *proposalRecord {
	rec := &proposalRecord{
		Preference: preference,
		Attempts:   result.Attempts,
		Degraded:   result.Degraded,
		Rejections: result.Rejections,
	}
	if result.Proposal != nil {
		for _, p := range result.Proposal.Placements {
			rec.Placements = append(rec.Placements, placementRecord{Component: p.Component, Host: p.Host, Reason: p.Reason})
		}
		rec.Risks = result.Proposal.Risks
	}
	return rec
}

// interactionLoop runs the preview/decide cycle until the operator
// confirms, aborts, or a regeneration degrades. It returns the final
// content and the decision vocabulary value; writing to disk is the
// caller's job (only a confirm reaches disk, never automatically).
func interactionLoop(stdout io.Writer, ioDeps reasonIO, filePath string, disk, candidate []byte, preference string, propose func(string) (*padvisor.ProposalResult, error), rebuild func(*padvisor.Proposal) ([]byte, error), result *padvisor.ProposalResult) (string, []byte, string, *padvisor.ProposalResult) {
	edited := false
	for {
		renderProposalPreview(stdout, ioDeps, filePath, disk, candidate, result)
		choice, err := ioDeps.ask("[c]onfirm / [e]dit / [r]egenerate / [a]bort: ")
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "advise: %v — aborting.\n", err)
			return decisionReasonAborted, candidate, preference, result
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "c", "confirm":
			if edited {
				return decisionReasonEdited, candidate, preference, result
			}
			return decisionReasonConfirmed, candidate, preference, result
		case "e", "edit":
			editedCandidate, editErr := editCandidate(ioDeps, candidate)
			if editErr != nil {
				_, _ = fmt.Fprintf(stdout, "advise: edit: %v\n", editErr)
				continue
			}
			candidate = editedCandidate
			edited = true
		case "r", "regenerate":
			next, regenErr := regenerate(ioDeps, propose, rebuild, preference, candidate)
			if next != nil {
				// The folded preference updates even on degradation — it
				// is what the last (failed) proposal round reasoned over.
				preference = next.preference
			}
			if errors.Is(regenErr, errRegenerateDegraded) {
				return decisionDegradedTable, candidate, preference, next.result
			}
			if regenErr != nil {
				_, _ = fmt.Fprintf(stdout, "advise: regenerate: %v\n", regenErr)
				continue
			}
			candidate, result = next.candidate, next.result
		case "a", "abort":
			return decisionReasonAborted, candidate, preference, result
		default:
			_, _ = fmt.Fprintf(stdout, "advise: unrecognized choice %q — c, e, r, or a.\n", choice)
		}
	}
}

// errRegenerateDegraded signals that a regeneration exhausted the
// guardrail — the caller answers with the table mode.
var errRegenerateDegraded = errors.New("regeneration degraded")

// regenOutcome is one successful regeneration's fresh state.
type regenOutcome struct {
	preference string
	candidate  []byte
	result     *padvisor.ProposalResult
}

// regenerate asks for an additional preference, re-runs the guardrailed
// proposal, and rebuilds the candidate. The outcome carries the folded
// preference either way — on degradation (errRegenerateDegraded, result
// set) the caller records what the failed round reasoned over;
// channel/parse errors surface verbatim with a nil outcome.
func regenerate(ioDeps reasonIO, propose func(string) (*padvisor.ProposalResult, error), rebuild func(*padvisor.Proposal) ([]byte, error), preference string, candidate []byte) (*regenOutcome, error) {
	add, err := ioDeps.ask("preference to add for this regeneration (empty keeps the current one): ")
	if err != nil {
		return nil, err
	}
	preference = joinPreference(preference, add)
	result, err := propose(preference)
	if err != nil {
		return nil, err
	}
	if result.Degraded {
		return &regenOutcome{preference: preference, candidate: candidate, result: result}, errRegenerateDegraded
	}
	next, err := rebuild(result.Proposal)
	if err != nil {
		return nil, err
	}
	return &regenOutcome{preference: preference, candidate: next, result: result}, nil
}

// joinPreference folds a regeneration addition into the session
// preference; empty additions change nothing. Session-scoped only —
// nothing persists it beyond the record.
func joinPreference(current, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return current
	}
	if current == "" {
		return addition
	}
	return current + "; " + addition
}

// renderProposalPreview prints the ranked proposal with reasons and
// risks, then the unified diff against the on-disk file.
func renderProposalPreview(stdout io.Writer, ioDeps reasonIO, filePath string, disk, candidate []byte, result *padvisor.ProposalResult) {
	_, _ = fmt.Fprint(stdout, "\nPROPOSED PLACEMENTS (ranked)\n")
	for i, p := range result.Proposal.Placements {
		_, _ = fmt.Fprintf(stdout, "  %d. %s → %s\n     reason: %s\n", i+1, p.Component, p.Host, p.Reason)
	}
	if len(result.Proposal.Risks) > 0 {
		_, _ = fmt.Fprint(stdout, "RISKS\n")
		for _, risk := range result.Proposal.Risks {
			_, _ = fmt.Fprintf(stdout, "  - %s\n", risk)
		}
	}
	_, _ = fmt.Fprint(stdout, "\n")
	_, _ = fmt.Fprint(stdout, unifiedDiff(filePath, filePath+" (proposed)", disk, candidate))
}

// buildCandidate converts the accepted proposal into the placements
// list, splices it into the on-disk document, and runs the result
// through the real config validator — the deterministic layer's last
// word before anything reaches the operator's eyes (a proposal that
// spliced into an invalid topology dies here, never at write time).
func buildCandidate(cfg *config.Config, disk []byte, profiles map[string]padvisor.Profile, proposal *padvisor.Proposal) ([]byte, error) {
	placements, err := placementsForProposal(cfg, profiles, proposal)
	if err != nil {
		return nil, err
	}
	spliced, err := splicePlacements(disk, placements, cfg.Placements)
	if err != nil {
		return nil, err
	}
	if err := validateCandidate(spliced); err != nil {
		return nil, fmt.Errorf("proposed placement does not produce a valid topology: %w", err)
	}
	return spliced, nil
}

// placementsForProposal converts the accepted proposal into the file's
// new placements list: proposed components keep their existing entry
// (config, env_file, published ports ride along; only the host moves),
// unproposed existing placements stay untouched, and brand-new
// components get their contract listen port allocated as the lowest
// free port ≥ 1024 on the target host. The result preserves the file's
// existing order, appending new components in proposal rank order.
func placementsForProposal(cfg *config.Config, profiles map[string]padvisor.Profile, proposal *padvisor.Proposal) ([]config.Placement, error) {
	existing := map[string]config.Placement{}
	for _, p := range cfg.Placements {
		existing[p.Component] = p
	}
	taken := portsTaken(cfg)

	var out []config.Placement
	inProposal := map[string]bool{}
	for _, a := range proposal.Placements {
		inProposal[a.Component] = true
		if current, ok := existing[a.Component]; ok {
			moved := current
			moved.Host = a.Host
			out = append(out, moved)
			claimPorts(taken, moved)
			continue
		}
		allocated, err := allocatePlacementPorts(a, taken)
		if err != nil {
			return nil, err
		}
		out = append(out, allocated)
	}
	for _, p := range cfg.Placements {
		if !inProposal[p.Component] {
			out = append(out, p)
		}
	}
	return out, nil
}

// portsTaken seeds the host → port-occupied map from the current
// config, including the state plane's published port.
func portsTaken(cfg *config.Config) map[string]map[int]bool {
	taken := map[string]map[int]bool{}
	if cfg.State != nil && len(cfg.Hosts) > 0 {
		taken[cfg.Hosts[0].ID] = map[int]bool{cfg.State.Postgres.Port: true}
	}
	for _, p := range cfg.Placements {
		claimPorts(taken, p)
	}
	return taken
}

// claimPorts marks one placement's published ports occupied on its
// host.
func claimPorts(taken map[string]map[int]bool, p config.Placement) {
	if taken[p.Host] == nil {
		taken[p.Host] = map[int]bool{}
	}
	for _, port := range p.Ports {
		taken[p.Host][port] = true
	}
}

// allocatePlacementPorts builds a fresh placement for a component that
// has none yet: the render contract's listen port publishes as the
// lowest free port ≥ 1024 on the target host. Config validation owns
// the final word when the candidate loads.
func allocatePlacementPorts(a padvisor.ProposedPlacement, taken map[string]map[int]bool) (config.Placement, error) {
	contract, ok := render.Lookup(a.Component)
	if !ok {
		return config.Placement{}, fmt.Errorf("advise: proposed component %q is outside the render allowlist", a.Component)
	}
	ports := map[string]int{}
	if contract.ListenPort != "" {
		if taken[a.Host] == nil {
			taken[a.Host] = map[int]bool{}
		}
		ports[contract.ListenPort] = lowestFreePort(taken[a.Host], 1024)
	}
	return config.Placement{Component: a.Component, Host: a.Host, Ports: ports}, nil
}

// lowestFreePort returns the smallest port ≥ from the host has free,
// marking it taken.
func lowestFreePort(taken map[int]bool, from int) int {
	for port := from; ; port++ {
		if !taken[port] {
			taken[port] = true
			return port
		}
	}
}

// editCandidate writes the candidate to a temp file, opens the
// operator's editor on it, reads the result back, and validates it as
// a topology config — an editor session that leaves an invalid file
// never reaches disk.
func editCandidate(ioDeps reasonIO, candidate []byte) ([]byte, error) {
	if ioDeps.edit == nil {
		return nil, errors.New("no editor configured (set $EDITOR)")
	}
	tmp, err := os.CreateTemp("", "looming-advise-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("stage edit: %w", err)
	}
	path := tmp.Name()
	defer func() { _ = os.Remove(path) }()
	if writeErr := writeAndClose(tmp, candidate); writeErr != nil {
		return nil, fmt.Errorf("stage edit: %w", writeErr)
	}

	if editErr := ioDeps.edit(path); editErr != nil {
		return nil, fmt.Errorf("editor: %w", editErr)
	}
	// #nosec G304 -- the path is this function's own staged temp file.
	edited, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read edited file: %w", err)
	}
	if err := validateCandidate(edited); err != nil {
		return nil, err
	}
	return edited, nil
}

// validateCandidate loads the would-be file through the real config
// validator — the same fail-closed checks apply runs.
func validateCandidate(content []byte) error {
	tmp, err := os.CreateTemp("", "looming-advise-check-*.yaml")
	if err != nil {
		return fmt.Errorf("validate edit: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeAndClose(tmp, content); err != nil {
		return fmt.Errorf("validate edit: %w", err)
	}
	// #nosec G304 -- the path is this function's own staged temp file.
	if _, err := config.Load(tmp.Name()); err != nil {
		return fmt.Errorf("edited file is not a valid topology: %w", err)
	}
	return nil
}

// writeAndClose writes content and closes the file — one error path
// for the staging helpers.
func writeAndClose(f *os.File, content []byte) error {
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeConfirmed lands the final content on disk, preserving the
// file's mode — the one write the command ever does, and only after
// the operator confirms.
func writeConfirmed(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("advise: stat topology file: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".looming-advise-*.yaml")
	if err != nil {
		return fmt.Errorf("advise: stage write: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("advise: stage write: %w", err)
	}
	if err := writeAndClose(tmp, content); err != nil {
		return fmt.Errorf("advise: stage write: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("advise: write topology file: %w", err)
	}
	return nil
}
