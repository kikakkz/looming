// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"fmt"
	"sort"
	"strings"

	padvisor "github.com/kikakkz/looming/platform/go/advisor"
	"github.com/kikakkz/looming/platform/go/render"
)

// tableHeader names the rendered columns: one row per (component,
// host) pair — the complete matrix, no pagination (advisor-l1 §4: the
// pair count is profiles × hosts, bounded by definition).
var tableHeader = []string{"COMPONENT", "HOST", "STATUS", "HEADROOM", "DETAIL"}

// statusFeasible/statusInfeasible are the STATUS column's vocabulary,
// quoted verbatim by tests and the session log's consumers.
const (
	statusFeasible   = "FEASIBLE"
	statusInfeasible = "INFEASIBLE"
)

// headroomNA renders a feasible row whose host did not declare the
// facts headroom computes from.
const headroomNA = "n/a"

// renderTable renders the feasibility matrix as the aligned text table
// the command prints: components in render-allowlist order, hosts by
// memory headroom descending with CPU headroom as tiebreak — the
// bin-packing convention (advisor-l1 §4). FEASIBLE rows show the
// headroom; INFEASIBLE rows name the violated hard rules and any
// missing facts. Pure function of the verdict set, so the golden test
// pins it exactly.
func renderTable(verdicts []padvisor.Verdict) string {
	groups := groupByComponent(verdicts)

	rows := make([][]string, 0, len(verdicts)+1)
	rows = append(rows, tableHeader)
	for _, group := range groups {
		for _, v := range group {
			rows = append(rows, []string{v.Component, v.Host, statusOf(v), headroomOf(v), detailOf(v)})
		}
	}
	return alignRows(rows)
}

// groupByComponent buckets verdicts per component, components in
// render.Allowlist() order, hosts within a component sorted by
// headroom. A component outside the allowlist cannot occur (the
// loader fails closed), so allowlist order is total for the input.
func groupByComponent(verdicts []padvisor.Verdict) [][]padvisor.Verdict {
	byComponent := make(map[string][]padvisor.Verdict)
	for _, v := range verdicts {
		byComponent[v.Component] = append(byComponent[v.Component], v)
	}
	var groups [][]padvisor.Verdict
	for _, name := range render.Allowlist() {
		group := byComponent[name]
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return hostLess(group[i], group[j]) })
		groups = append(groups, group)
	}
	return groups
}

// hostLess orders a component's pairs: memory headroom descending,
// CPU headroom as tiebreak, pairs without computable headroom last,
// host id as the final stable tiebreak.
func hostLess(a, b padvisor.Verdict) bool {
	switch {
	case a.Headroom != nil && b.Headroom != nil:
		if a.Headroom.MemoryMB != b.Headroom.MemoryMB {
			return a.Headroom.MemoryMB > b.Headroom.MemoryMB
		}
		if a.Headroom.CPUCores != b.Headroom.CPUCores {
			return a.Headroom.CPUCores > b.Headroom.CPUCores
		}
	case a.Headroom != nil:
		return true
	case b.Headroom != nil:
		return false
	}
	return a.Host < b.Host
}

// statusOf is the STATUS column: feasible exactly when no hard rule
// fired and no needed fact is missing.
func statusOf(v padvisor.Verdict) string {
	if v.Feasible {
		return statusFeasible
	}
	return statusInfeasible
}

// headroomOf is the HEADROOM column: remaining memory and CPU after
// the profile's floors, or n/a when the feasible pair did not declare
// the facts. INFEASIBLE rows leave the column empty — the detail
// carries the answer.
func headroomOf(v padvisor.Verdict) string {
	if !v.Feasible {
		return ""
	}
	if v.Headroom == nil {
		return headroomNA
	}
	return fmt.Sprintf("%d MB / %d cores", v.Headroom.MemoryMB, v.Headroom.CPUCores)
}

// detailOf is the DETAIL column: the violated hard rules, each with
// its operator-facing reason, then the missing facts — the fail-closed
// gap, named (advisor-l1 §2).
func detailOf(v padvisor.Verdict) string {
	var parts []string
	for _, violation := range v.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", violation.Rule, violation.Detail))
	}
	if len(v.Missing) > 0 {
		parts = append(parts, "missing facts: "+strings.Join(v.Missing, ", "))
	}
	return strings.Join(parts, "; ")
}

// alignRows renders rows as a space-aligned table, each column as wide
// as its widest cell — the simple fixed-column alignment the dbcmd
// table precedent uses.
func alignRows(rows [][]string) string {
	widths := make([]int, len(tableHeader))
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var out strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			if i == len(row)-1 {
				_, _ = out.WriteString(cell)
				continue
			}
			_, _ = out.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)+2))
		}
		_ = out.WriteByte('\n')
	}
	return out.String()
}
