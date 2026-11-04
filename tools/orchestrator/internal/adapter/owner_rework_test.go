package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestReworkContextV1DigestRetainsExactOriginalBytes(t *testing.T) {
	c := ReworkContext{Version: 1, RunID: "run", TaskID: "task", PreviousWorkRevision: 1, PreviousCandidateOID: "oid", CandidateEventID: "event", CandidateSHA256: "sha", ReviewEventID: "review", ReviewSHA256: "reviewsha", ReviewSummary: "findings", Feedback: "fix", Acceptance: "test"}
	want := `{"version":1,"run_id":"run","task_id":"task","previous_work_revision":1,"previous_candidate_oid":"oid","candidate_event_id":"event","candidate_sha256":"sha","review_event_id":"review","review_sha256":"reviewsha","review_summary":"findings","feedback":"fix","acceptance":"test"}`
	raw, err := json.Marshal(c)
	if err != nil || string(raw) != want {
		t.Fatalf("v1 bytes changed: %s %v", raw, err)
	}
	sum := sha256.Sum256([]byte(want))
	if c.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatal("v1 digest changed")
	}
}
