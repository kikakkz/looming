// SPDX-License-Identifier: Apache-2.0

package domain

// InteractionBody is one assembled user↔model exchange (gateway-l1
// storm Q3): the raw request and the response transcript, emitted async
// when the stream completes. Truncated marks a response cut by the size
// cap — the recorder never blocks the data plane to keep everything.
//
// This shape is the first consumer of the records-plane event contract;
// when a second language needs it, it promotes to contracts/ (AD-34).
type InteractionBody struct {
	Subject      string
	Model        string
	RequestBody  []byte
	ResponseBody []byte
	Truncated    bool
}

// MeterRecord counts usage for calls that reached a model (AD-32 #5:
// denials are never metered — they live in the audit stream only).
type MeterRecord struct {
	Subject       string
	Model         string
	Outcome       string // "completed" today; usage fields land with token reporting
	RequestBytes  int64
	ResponseBytes int64
}
