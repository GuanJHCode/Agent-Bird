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

// Frozen before introducing references: v2 is a persisted byte and digest contract.
func TestReworkContextV2DigestRetainsExactOriginalBytes(t *testing.T) {
	spec := OwnerReworkSpec{TaskID: "task", WorkRevision: 1, EventID: "event", EventRevision: 2, EventHash: "sha", ActionSlot: "slot", SegmentID: "segment", CandidateOID: "oid", Feedback: "fix", Acceptance: "test", CommandID: "command"}
	c := ReworkContext{Version: 2, RunID: "run", TaskID: "task", PreviousWorkRevision: 1, PreviousCandidateOID: "oid", CandidateEventID: "event", CandidateSHA256: "sha", Feedback: "fix", Acceptance: "test", Source: "owner", OwnerDecisionID: "command", OwnerSpec: &spec, OwnerSpecSHA256: spec.Digest()}
	want := `{"version":2,"run_id":"run","task_id":"task","previous_work_revision":1,"previous_candidate_oid":"oid","candidate_event_id":"event","candidate_sha256":"sha","review_event_id":"","review_sha256":"","review_summary":"","feedback":"fix","acceptance":"test","source":"owner","owner_decision_id":"command","owner_spec":{"task_id":"task","work_revision":1,"event_id":"event","event_revision":2,"event_hash":"sha","action_slot":"slot","segment_id":"segment","candidate_oid":"oid","feedback":"fix","acceptance":"test","command_id":"command"},"owner_spec_sha256":"d69c9233042967f16ea24b5285cae1d0c5a8268cd7897050ec680e35e7ed5877"}`
	raw, err := json.Marshal(c)
	if err != nil || string(raw) != want {
		t.Fatalf("v2 bytes changed: %s %v", raw, err)
	}
	if c.Digest() != "b3d1448d8d99357179bb22231720bc5db7312521d28d190917a30542d86634a9" {
		t.Fatalf("v2 digest changed: %s", c.Digest())
	}
}
