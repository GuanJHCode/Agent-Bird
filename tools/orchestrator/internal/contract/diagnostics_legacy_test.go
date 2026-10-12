package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestDiagnosticsNilKeepsLiteralLegacyCandidateIntents(t *testing.T) {
	c := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", SlotToken: "slot", PlanRevision: 1, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
	for _, tc := range []struct {
		name   string
		vector *DependencyInputVector
		body   string
	}{
		{"three_fields", nil, `{"Plan":1,"Payload":{"kind":"fake"},"Input":null}`},
		{"four_fields", &DependencyInputVector{Version: 1}, `{"Plan":1,"Payload":{"kind":"fake"},"Input":null,"DependencyInputs":{"version":1,"inputs":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.DependencyInputs = tc.vector
			got, err := CandidateIntentHash(c)
			want := sha256.Sum256([]byte("run\x00task\x00attempt\x00segment\x00slot\x00" + tc.body))
			if err != nil || got != hex.EncodeToString(want[:]) {
				t.Fatal("nil grant changed immutable legacy bytes", got, err)
			}
		})
	}
}
