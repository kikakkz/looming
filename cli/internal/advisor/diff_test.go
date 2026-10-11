// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUnifiedDiffNoChanges: identical documents produce the header and
// nothing else — the preview stays silent when there is nothing to
// decide.
func TestUnifiedDiffNoChanges(t *testing.T) {
	doc := "version: 1\nhosts: [{id: a}]\n"
	diff := unifiedDiff("a.yaml", "b.yaml", []byte(doc), []byte(doc))
	assert.Equal(t, "--- a.yaml\n+++ b.yaml\n", diff)
}

// TestUnifiedDiffOneLineChange: the changed line with three context
// lines on each side, hunk header counting the real positions.
func TestUnifiedDiffOneLineChange(t *testing.T) {
	var oldDoc, newDoc strings.Builder
	oldDoc.WriteString("version: 1\n")
	newDoc.WriteString("version: 1\n")
	for i := 2; i <= 10; i++ {
		line := "line " + itoa(i) + "\n"
		oldDoc.WriteString(line)
		if i != 5 {
			newDoc.WriteString(line)
		} else {
			newDoc.WriteString("CHANGED\n")
		}
	}
	diff := unifiedDiff("old", "new", []byte(oldDoc.String()), []byte(newDoc.String()))

	assert.Contains(t, diff, "@@ -2,7 +2,7 @@")
	assert.Contains(t, diff, "-line 5\n")
	assert.Contains(t, diff, "+CHANGED\n")
	assert.Contains(t, diff, " line 4\n")
	assert.Contains(t, diff, " line 6\n")
	assert.NotContains(t, diff, "line 10", "outside the context window")
}

// TestUnifiedDiffAppend: an append-only change reports the insertion
// with the correct trailing positions.
func TestUnifiedDiffAppend(t *testing.T) {
	oldDoc := "a\nb\n"
	newDoc := "a\nb\nc\n"
	diff := unifiedDiff("old", "new", []byte(oldDoc), []byte(newDoc))
	assert.Contains(t, diff, "@@ -1,2 +1,3 @@")
	assert.Contains(t, diff, "+c\n")
}

// TestUnifiedDiffEmptyOld: the empty-document convention — an
// all-addition hunk reads "-0,0".
func TestUnifiedDiffEmptyOld(t *testing.T) {
	diff := unifiedDiff("old", "new", nil, []byte("a\nb\n"))
	assert.Contains(t, diff, "@@ -0,0 +1,2 @@")
	assert.Contains(t, diff, "+a\n+b\n")
}

// TestUnifiedDiffMergeAdjacent: changes closer than the context window
// merge into a single hunk.
func TestUnifiedDiffMergeAdjacent(t *testing.T) {
	oldDoc := "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	newDoc := "1\n2\nX\n4\n5\n6\n7\nY\n9\n"
	diff := unifiedDiff("old", "new", []byte(oldDoc), []byte(newDoc))
	assert.Equal(t, 1, strings.Count(diff, "@@ "), "one merged hunk, got:\n%s", diff)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
