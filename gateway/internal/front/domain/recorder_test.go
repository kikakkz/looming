// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestInteractionBodyCarriesTruncationFlag(t *testing.T) {
	b := InteractionBody{Subject: "ker", Model: "gpt-5", Truncated: true}
	if !b.Truncated {
		t.Fatal("truncation flag must survive as-is")
	}
}

func TestMeterRecordUsageOnlyShape(t *testing.T) {
	m := MeterRecord{Subject: "ker", Model: "gpt-5", Outcome: "completed", RequestBytes: 10, ResponseBytes: 20}
	if m.Outcome != "completed" || m.ResponseBytes != 20 {
		t.Fatalf("unexpected meter shape: %+v", m)
	}
}
