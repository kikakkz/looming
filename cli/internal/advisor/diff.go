// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"fmt"
	"strings"
)

// unifiedDiff renders oldText→newText as a unified diff with 3 lines
// of context — the preview format the decide step shows (advisor-l1
// §3 step 5). The simple LCS over lines fits the artifact: a topology
// file is a few hundred lines at most, so the quadratic table is
// small and the implementation stays dependency-free (the kit ships
// no diff library).
func unifiedDiff(oldLabel, newLabel string, oldText, newText []byte) string {
	a := splitDiffLines(string(oldText))
	b := splitDiffLines(string(newText))
	ops := diffOps(a, b)

	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldLabel, newLabel)
	for _, h := range hunks(ops) {
		_, _ = fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount)
		for _, line := range h.lines {
			prefix := " "
			switch line.kind {
			case opDel:
				prefix = "-"
			case opAdd:
				prefix = "+"
			}
			_, _ = out.WriteString(prefix + line.text + "\n")
		}
	}
	return out.String()
}

// opKind is one diff operation: context, deletion, or addition.
type opKind int

const (
	opCtx opKind = iota
	opDel
	opAdd
)

// diffOp is a run of same-kind lines.
type diffOp struct {
	kind  opKind
	lines []string
}

// diffLine is one output line with its 1-based old/new numbers (0 =
// the side does not have the line).
type diffLine struct {
	kind         opKind
	text         string
	oldNo, newNo int
}

// splitDiffLines splits on newlines, keeping the comparison line-based
// and the reconstruction exact (a trailing newline yields no phantom
// line).
func splitDiffLines(s string) []string {
	trimmed := strings.TrimSuffix(s, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// diffOps computes the line diff as an operation list: an LCS table
// backtracked from the end, the standard textbook construction.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				table[i][j] = table[i+1][j+1] + 1
			case table[i+1][j] >= table[i][j+1]:
				table[i][j] = table[i+1][j]
			default:
				table[i][j] = table[i][j+1]
			}
		}
	}

	var ops []diffOp
	flush := func(kind opKind, lines []string) {
		if len(lines) > 0 {
			ops = append(ops, diffOp{kind, lines})
		}
	}
	var del, add []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			flush(opDel, del)
			flush(opAdd, add)
			del, add = nil, nil
			ops = append(ops, diffOp{opCtx, []string{a[i]}})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			del = append(del, a[i])
			i++
		default:
			add = append(add, b[j])
			j++
		}
	}
	del = append(del, a[i:]...)
	add = append(add, b[j:]...)
	flush(opDel, del)
	flush(opAdd, add)
	return ops
}

// diffHunk is one @@-grouped region: the change plus up to 3 context
// lines on each side; adjacent regions merge into one hunk.
type diffHunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []diffLine
}

// hunks flattens the operations into numbered lines, marks a ±3-line
// window around every change, and groups the marked runs.
func hunks(ops []diffOp) []diffHunk {
	const context = 3

	var flat []diffLine
	oldNo, newNo := 1, 1
	for _, op := range ops {
		for _, text := range op.lines {
			line := diffLine{kind: op.kind, text: text, oldNo: oldNo, newNo: newNo}
			if op.kind == opAdd {
				line.oldNo = 0
			} else {
				oldNo++
			}
			if op.kind == opDel {
				line.newNo = 0
			} else {
				newNo++
			}
			flat = append(flat, line)
		}
	}

	marked := make([]bool, len(flat))
	for i, line := range flat {
		if line.kind == opCtx {
			continue
		}
		for j := max(0, i-context); j < min(len(flat), i+context+1); j++ {
			marked[j] = true
		}
	}

	var out []diffHunk
	for i := 0; i < len(flat); {
		if !marked[i] {
			i++
			continue
		}
		start := i
		for i < len(flat) && marked[i] {
			i++
		}
		out = append(out, buildHunk(flat[start:i]))
	}
	return out
}

// buildHunk assembles one hunk, deriving its header from the first
// line each side actually has. A hunk consisting purely of additions
// (an empty old file) has no old line at all: the unified-diff
// convention renders that side as "-0,0".
func buildHunk(lines []diffLine) diffHunk {
	h := diffHunk{oldStart: -1, newStart: -1}
	for _, line := range lines {
		if line.oldNo != 0 {
			if h.oldStart == -1 {
				h.oldStart = line.oldNo
			}
			h.oldCount++
		}
		if line.newNo != 0 {
			if h.newStart == -1 {
				h.newStart = line.newNo
			}
			h.newCount++
		}
	}
	if h.oldStart == -1 {
		h.oldStart = 0
	}
	if h.newStart == -1 {
		h.newStart = 0
	}
	h.lines = lines
	return h
}
