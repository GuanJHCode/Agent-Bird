package store

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
	"testing"
)

// The resurrection ordering an earlier reviewer raised was never decided on
// disk: an UNKNOWN segment stays unconfirmed unless a real terminal event
// arrives, but a genuine exited carrying the plan-pinned artifact digest is
// stronger evidence than the earlier loss of visibility. Both halves are
// asserted here so the ordering is a pinned contract instead of an accident of
// the outcome-stickiness update.
const resurrectionArtifactSHA256 = "0f1e2d3c4b5a69788796a5b3c1d0e2f4" + "0f1e2d3c4b5a69788796a5b3c1d0e2f4"

const mismatchedArtifactSHA256 = "ff00ff00ff00ff00ff00ff00ff00ff00" + "ff00ff00ff00ff00ff00ff00ff00ff00"

func TestUnknownSegmentSettlesOnlyOnVerifiedExit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		steps       []string
		wantTask    string
		wantSegment string
		wantBudget  string
	}{
		{"unknown alone never completes", []string{"unknown"}, "unknown", "unknown", "reserved"},
		{"unknown then result never completes", []string{"unknown", "result"}, "unknown", "unknown", "reserved"},
		{"unknown then mismatched digest never completes", []string{"unknown", "result", "exit:" + mismatchedArtifactSHA256}, "result_ready", "exited", "settled"},
		{"unknown then verified digest completes", []string{"unknown", "result", "exit:" + resurrectionArtifactSHA256}, "completed", "exited", "settled"},
		{"unknown then exit without result is interrupted", []string{"unknown", "exit:"}, "interrupted", "exited", "settled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeliveryFixture(t, "collect", func(spec *TaskSpec) {
				spec.CompletionPolicy = "artifact"
				spec.ExpectedArtifactSHA256 = resurrectionArtifactSHA256
			})
			for _, step := range tc.steps {
				switch {
				case step == "unknown":
					f.commit(t, contract.EventUnknown, "ru-unknown")
				case step == "result":
					f.commit(t, contract.EventResult, "ru-result")
				case strings.HasPrefix(step, "exit:"):
					digest := strings.TrimPrefix(step, "exit:")
					f.seq++
					var artifact *contract.ArtifactRef
					if digest != "" {
						artifact = &contract.ArtifactRef{ID: "ru-art", Path: "/tmp/ru-diagnostic", Size: 12, SHA256: digest}
					}
					e := contract.Event{Version: 1, ProducerID: f.hostID, EventID: "ru-exit", RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.seq, Kind: contract.EventExited, PayloadHash: strings.Repeat("c", 64), ActiveMS: 1000, Artifact: artifact}
					exitZero := 0
					e.ExitCode = &exitZero
					if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
						t.Fatalf("commit exited: %v", err)
					}
				default:
					t.Fatalf("unknown step %q", step)
				}
			}
			if got := f.taskStatus(t, "task"); got != tc.wantTask {
				t.Fatalf("task status=%s want=%s", got, tc.wantTask)
			}
			var segment string
			if err := f.db.sql.QueryRow(`SELECT status FROM segment_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&segment); err != nil {
				t.Fatal(err)
			}
			if segment != tc.wantSegment {
				t.Fatalf("segment status=%s want=%s", segment, tc.wantSegment)
			}
			status, used, granted := ledgerBudgetRow(t, f.db, f.grant.SegmentID)
			if status != tc.wantBudget {
				t.Fatalf("budget row=%s want=%s (used=%d granted=%d)", status, tc.wantBudget, used, granted)
			}
			switch tc.wantBudget {
			case "reserved":
				if used != 0 || granted != f.grant.GrantedActiveMS {
					t.Fatalf("unconfirmed stop must keep the whole grant charged: used=%d granted=%d", used, granted)
				}
			case "settled":
				if used != 1000 {
					t.Fatalf("settled used=%d want 1000 (granted=%d)", used, granted)
				}
			}
		})
	}
}

// The refunding direction: a late UNKNOWN after a confirmed exit becomes
// deliverable history, but it must not move the segment back, settle anything
// again, or touch the receipts/decisions rows.
func TestLateUnknownAfterExitNeverReopensSettlement(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t, "collect")
	f.commit(t, contract.EventResult, "lu-result")
	f.commit(t, contract.EventExited, "lu-exit")
	f.seq++
	late := contract.Event{Version: 1, ProducerID: f.hostID, EventID: "lu-unknown", RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.seq, Kind: contract.EventUnknown, PayloadHash: strings.Repeat("d", 64)}
	if _, err := f.db.CommitHostEvent(ctx, late); err != nil {
		t.Fatalf("late unknown refused: %v", err)
	}
	const fingerprintSettledPlusHistory = "task=result_ready;receipts=0;collections=0;decisions=0;reviews=0;events=lu-exit=internal;lu-result=pending;lu-unknown=pending;"
	if got := f.ledgerFingerprint(t); got != fingerprintSettledPlusHistory {
		t.Fatalf("late unknown ledger:\nwant %s\ngot  %s", fingerprintSettledPlusHistory, got)
	}
	var segment string
	if err := f.db.sql.QueryRow(`SELECT status FROM segment_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&segment); err != nil {
		t.Fatal(err)
	}
	if segment != "exited" {
		t.Fatalf("late unknown reopened segment: %s", segment)
	}
	if got := f.taskStatus(t, "task"); got != "result_ready" {
		t.Fatalf("late unknown changed task: %s", got)
	}
}
