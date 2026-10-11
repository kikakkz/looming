// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kikakkz/looming/platform/go/config"
)

// errProposal marks proposal-protocol failures (unparseable model
// output) apart from channel errors.
var errProposal = errors.New("advisor: proposal")

// ProposedPlacement is one model-proposed (component, host) assignment
// with the operator-facing reason. It is a PROPOSAL until the
// guardrail accepts it and the human confirms it (AD-38 decision 1, 6).
type ProposedPlacement struct {
	Component string `json:"component"`
	Host      string `json:"host"`
	Reason    string `json:"reason"`
}

// Proposal is the model's ranked answer: placements in preference
// order plus the risk list the operator weighs before deciding.
type Proposal struct {
	Placements []ProposedPlacement `json:"placements"`
	Risks      []string            `json:"risks"`
}

// ProposalInput is everything the model may see (advisor-l1 §3 step 3):
// the evaluator's feasible set (hard rules already applied — the model
// never sees infeasible pairs or hard-rule internals), the profiles'
// soft sections, the declared hosts' soft context, and the operator's
// free-text preference, which lives for this session only.
type ProposalInput struct {
	Verdicts   []Verdict
	Profiles   map[string]Profile
	Hosts      []config.Host
	Preference string
}

// BuildProposalMessages assembles the chat the model reasons over:
// a system contract (you rank the feasible set, explain trade-offs,
// answer with one JSON document) and a deterministic user document.
// Pure and deterministic — the same input yields the same messages.
func BuildProposalMessages(in ProposalInput) []ChatMessage {
	return []ChatMessage{
		{Role: "system", Content: proposalSystemPrompt},
		{Role: "user", Content: buildProposalDocument(in)},
	}
}

// proposalSystemPrompt is the channel contract: the deterministic
// layer already filtered the feasible set; the model ranks, explains,
// and flags risks; the answer is one JSON document, nothing else.
const proposalSystemPrompt = `You are the placement advisor of the Looming cluster. A deterministic evaluator has already applied the hard constraints; you receive ONLY feasible (component, host) pairs. Rank the pairs into a proposed placement (at most one host per component), explain each choice, and list risks. Answer with ONE JSON document and no prose around it: {"placements":[{"component":"...","host":"...","reason":"..."}],"risks":["..."]}. Components may be omitted from the placements when no host is a good fit.`

// buildProposalDocument renders the user document: the feasible set
// with its soft context, the profile soft preferences, and the
// operator's session preference. Deterministic ordering (pairs by
// component then host, profiles by name) keeps records diffable.
func buildProposalDocument(in ProposalInput) string {
	var b strings.Builder
	b.WriteString("# Feasible (component, host) pairs\n")
	pairs := 0
	for _, v := range in.Verdicts {
		if !v.Feasible {
			continue
		}
		pairs++
		fmt.Fprintf(&b, "- %s on %s%s%s\n", v.Component, v.Host, headroomClause(v), hostClause(in.Hosts, v.Host))
	}
	if pairs == 0 {
		b.WriteString("(none — every pair failed a hard rule)\n")
	}

	b.WriteString("\n# Component soft preferences\n")
	names := make([]string, 0, len(in.Profiles))
	for name := range in.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		soft := in.Profiles[name].Soft
		fmt.Fprintf(&b, "- %s: preferred_zone=%q spread=%q\n", name, soft.PreferredZone, soft.Spread)
	}

	b.WriteString("\n# Operator preference (this session only)\n")
	preference := strings.TrimSpace(in.Preference)
	if preference == "" {
		preference = "(none)"
	}
	b.WriteString(preference + "\n")

	b.WriteString("\nPropose the placement JSON now.")
	return b.String()
}

// headroomClause renders the pair's remaining capacity when declared.
func headroomClause(v Verdict) string {
	if v.Headroom == nil {
		return ""
	}
	return fmt.Sprintf(" (headroom: %d MB memory, %d CPU cores)", v.Headroom.MemoryMB, v.Headroom.CPUCores)
}

// hostClause appends the host's declared zone when known — the soft
// context for cloud-vs-lan trade-offs.
func hostClause(hosts []config.Host, hostID string) string {
	for _, h := range hosts {
		if h.ID != hostID || h.Capabilities == nil {
			continue
		}
		if zone := h.Capabilities.Network.Zone; zone != "" {
			return " (zone: " + zone + ")"
		}
	}
	return ""
}

// ParseProposal decodes the model's answer. The contract says JSON
// only, but the parser tolerates prose around the document by scanning
// for the outermost braces — a recorded-response fake pins the exact
// tolerance. A missing or malformed document is a protocol error, not
// a guessable one.
func ParseProposal(content string) (*Proposal, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("%w: no JSON document in the response", errProposal)
	}
	var p Proposal
	dec := json.NewDecoder(strings.NewReader(content[start : end+1]))
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: malformed proposal JSON: %v", errProposal, err)
	}
	return &p, nil
}

// ProposalViolation is one rejected placement with the
// operator-facing problem — the vocabulary the regeneration feedback
// and the session record quote.
type ProposalViolation struct {
	Placement ProposedPlacement
	Problem   string
}

// CheckProposal runs the guardrail (AD-38 decision 6): every proposed
// placement re-enters the deterministic layer. Unknown components,
// unknown hosts, infeasible pairs (with the violated rules quoted),
// and duplicate components are rejected; the rest pass through. The
// hard layer is the backstop, not the model's own judgment.
func CheckProposal(p *Proposal, in ProposalInput) (accepted []ProposedPlacement, violations []ProposalViolation) {
	profiles := in.Profiles
	hosts := hostIDSet(in.Hosts)
	feasible := feasiblePairSet(in.Verdicts)
	seen := map[string]bool{}

	for _, placement := range p.Placements {
		switch {
		case !profilesKnow(profiles, placement.Component):
			violations = append(violations, ProposalViolation{placement, "unknown component " + fmt.Sprintf("%q", placement.Component)})
		case !hosts[placement.Host]:
			violations = append(violations, ProposalViolation{placement, "unknown host " + fmt.Sprintf("%q", placement.Host)})
		case !feasible[placement.Component+"\x00"+placement.Host]:
			violations = append(violations, ProposalViolation{placement, "infeasible pair: " + infeasibleReason(in.Verdicts, placement)})
		case seen[placement.Component]:
			violations = append(violations, ProposalViolation{placement, fmt.Sprintf("component %q proposed more than once", placement.Component)})
		default:
			seen[placement.Component] = true
			accepted = append(accepted, placement)
		}
	}
	return accepted, violations
}

// profilesKnow reports whether name is a known profile (a plain map
// lookup kept as a function so the guardrail's rule vocabulary stays
// obvious at the call site).
func profilesKnow(profiles map[string]Profile, name string) bool {
	_, ok := profiles[name]
	return ok
}

// hostIDSet flattens the declared hosts into a lookup set.
func hostIDSet(hosts []config.Host) map[string]bool {
	out := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		out[h.ID] = true
	}
	return out
}

// feasiblePairSet indexes the verdicts' feasible pairs.
func feasiblePairSet(verdicts []Verdict) map[string]bool {
	out := map[string]bool{}
	for _, v := range verdicts {
		if v.Feasible {
			out[v.Component+"\x00"+v.Host] = true
		}
	}
	return out
}

// infeasibleReason quotes why the pair is not feasible — the violated
// rules and the missing facts, the table mode's own vocabulary.
func infeasibleReason(verdicts []Verdict, placement ProposedPlacement) string {
	for _, v := range verdicts {
		if v.Component != placement.Component || v.Host != placement.Host {
			continue
		}
		parts := make([]string, 0, len(v.Violations)+1)
		for _, violation := range v.Violations {
			parts = append(parts, fmt.Sprintf("%s: %s", violation.Rule, violation.Detail))
		}
		if len(v.Missing) > 0 {
			parts = append(parts, "missing facts: "+strings.Join(v.Missing, ", "))
		}
		if len(parts) == 0 {
			return "evaluator marked the pair infeasible"
		}
		return strings.Join(parts, "; ")
	}
	return "no such pair in the evaluator's matrix"
}

// ProposalResult is the guardrailed proposal: the accepted proposal
// (nil when degraded), the attempt count, the rejection trail, and
// the degradation flag — max attempts exhausted means the caller
// falls back to the deterministic table mode.
type ProposalResult struct {
	Proposal   *Proposal
	Attempts   int
	Rejections []string
	Degraded   bool
}

// MaxProposalAttempts bounds the propose → validate → regenerate loop
// (advisor-l1 §3 step 4): three tries, then the table mode answers.
const MaxProposalAttempts = 3

// ProposeWithGuardrail is the re-validation loop: propose, run every
// placement back through the evaluator, feed the violations back as
// regeneration context, and degrade after MaxProposalAttempts. The
// loop is the seam's only caller of the model — CI fakes the client
// with recorded responses.
func ProposeWithGuardrail(ctx context.Context, client LLMClient, in ProposalInput) (*ProposalResult, error) {
	var rejections []string
	messages := BuildProposalMessages(in)
	for attempt := 1; attempt <= MaxProposalAttempts; attempt++ {
		resp, err := client.Chat(ctx, ChatRequest{Messages: messages})
		if err != nil {
			return nil, err
		}
		content := resp.Choices[0].Message.Content
		proposal, err := ParseProposal(content)
		if err != nil {
			rejections = append(rejections, fmt.Sprintf("attempt %d: %v", attempt, err))
			messages = append(messages,
				ChatMessage{Role: "assistant", Content: content},
				ChatMessage{Role: "user", Content: "That was not the JSON document we agreed on. Propose again, exactly one JSON document."})
			continue
		}
		accepted, violations := CheckProposal(proposal, in)
		if len(violations) == 0 {
			return &ProposalResult{
				Proposal:   &Proposal{Placements: accepted, Risks: proposal.Risks},
				Attempts:   attempt,
				Rejections: rejections,
			}, nil
		}
		summary := violationSummary(violations)
		rejections = append(rejections, fmt.Sprintf("attempt %d: %s", attempt, summary))
		messages = append(messages,
			ChatMessage{Role: "assistant", Content: content},
			ChatMessage{Role: "user", Content: "Hard constraints rejected your proposal (" + summary + "). Propose again using only feasible pairs, one host per component, same JSON shape."})
	}
	return &ProposalResult{Attempts: MaxProposalAttempts, Rejections: rejections, Degraded: true}, nil
}

// violationSummary renders the rejection feedback as one line.
func violationSummary(violations []ProposalViolation) string {
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		parts = append(parts, fmt.Sprintf("%s on %s: %s", v.Placement.Component, v.Placement.Host, v.Problem))
	}
	return strings.Join(parts, " | ")
}
