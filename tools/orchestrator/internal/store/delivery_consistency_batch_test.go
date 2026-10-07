package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// collect ACK only records receipt consumption. It must neither turn a result
// into owner acceptance nor let a stale result complete a revised task.
func TestDeliveryConsistencyBatchSeparatesCollectACKFromOwnerAcceptance(t *testing.T) {
	ctx := context.Background()
	f := newSummaryBindingFixture(t)
	e := f.event
	decision := AckDecision{
		EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash,
		ActionSlot: e.ActionSlot, Decision: "waiting_user", CommandID: "receipt-only",
	}
	firstID, firstProof, err := f.db.CollectionReceipt(ctx, "task", []contract.Event{e})
	if err != nil {
		t.Fatal(err)
	}
	secondID, secondProof, err := f.db.CollectionReceipt(ctx, "task", []contract.Event{e})
	if err != nil || firstID != secondID || firstProof != secondProof {
		t.Fatalf("repeat collect changed receipt: %q %q %q %q %v", firstID, firstProof, secondID, secondProof, err)
	}
	if err = f.db.AckDelivery(ctx, "task", firstID, "", strings.Repeat("0", 64), []AckDecision{decision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong collection proof=%v", err)
	}
	if err = f.db.AckDelivery(ctx, "task", firstID, strings.Repeat("a", 64), firstProof, []AckDecision{decision}); !errors.Is(err, CodeError("delivery_proof_mode_mismatch")) {
		t.Fatalf("mixed delivery proof=%v", err)
	}
	if err = f.db.AckDelivery(ctx, "task", firstID, "", firstProof, []AckDecision{decision}); err != nil {
		t.Fatal(err)
	}
	var status string
	var ownerDecisions int
	if err = f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&ownerDecisions); err != nil {
		t.Fatal(err)
	}
	if status != "result_ready" || ownerDecisions != 0 {
		t.Fatalf("receipt ACK became acceptance: status=%s owner_decisions=%d", status, ownerDecisions)
	}
	changed := decision
	changed.Decision = "handled"
	if err = f.db.AckDelivery(ctx, "task", firstID, "", firstProof, []AckDecision{changed}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different ACK replay=%v", err)
	}
	if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "task", WorkRevision: e.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "owner-accept"}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("owner acceptance did not complete result: status=%s err=%v", status, err)
	}
}

// A durable result cannot outlive the revision or be repurposed after owner
// feedback has queued a new candidate chain.
func TestDeliveryConsistencyBatchRejectsExpiredResultAndOldReworkCandidate(t *testing.T) {
	t.Run("result_requires_successful_exit", func(t *testing.T) {
		for _, testCase := range []struct {
			name string
			exit *int
		}{
			{name: "missing_exit_code"},
			{name: "nonzero_exit_code", exit: pointerTo(17)},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				db, grant := quotaFixture(t)
				result := contract.Event{Version: 1, ProducerID: grant.HostID, EventID: "result", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("a", 64)}
				result.Artifact = managedHostArtifactForTest(t, db, grant, "terminal", 0, []byte("incomplete"))
				result.PayloadHash = result.Artifact.SHA256
				if _, err := db.CommitHostEvent(context.Background(), result); err != nil {
					t.Fatal(err)
				}
				exited := result
				exited.EventID, exited.Sequence, exited.Kind, exited.PayloadHash, exited.ExitCode = "exited", 2, contract.EventExited, strings.Repeat("b", 64), testCase.exit
				if _, err := db.CommitHostEvent(context.Background(), exited); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "failed" {
					t.Fatalf("result with unusable exit became %q: %v", status, err)
				}
				_, err := db.ReviewResult(context.Background(), ReviewSpec{TaskID: result.TaskID, WorkRevision: result.WorkRevision, EventID: result.EventID, EventRevision: 1, EventHash: result.PayloadHash, ActionSlot: "action-" + runtimeHash(result.ProducerID + "\x00" + result.EventID)[:32], Decision: "accept", CommandID: "accept-unusable-exit"})
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("unusable exit authorized result: %v", err)
				}
				if _, err := db.CommitHostEvent(context.Background(), exited); err != nil {
					t.Fatal(err)
				}
				var budgetStatus, segmentStatus, attemptStatus, body string
				var used, granted int64
				if err := db.sql.QueryRow(`SELECT status,used_ms,granted_ms FROM budget_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&budgetStatus, &used, &granted); err != nil || budgetStatus != "settled" || used != granted {
					t.Fatalf("unusable result lost settlement: status=%s used=%d grant=%d err=%v", budgetStatus, used, granted, err)
				}
				if err := db.sql.QueryRow(`SELECT s.status,a.status FROM segment_runtime s JOIN attempts a ON a.id=? WHERE s.segment_id=?`, grant.AttemptID, grant.SegmentID).Scan(&segmentStatus, &attemptStatus); err != nil || segmentStatus != "exited" || attemptStatus != "failed" {
					t.Fatalf("unusable result status: segment=%s attempt=%s err=%v", segmentStatus, attemptStatus, err)
				}
				if err := db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='result'`).Scan(&body); err != nil {
					t.Fatal(err)
				}
				var retained contract.Event
				if err := json.Unmarshal([]byte(body), &retained); err != nil || retained.Artifact == nil || !reflect.DeepEqual(retained.Artifact, result.Artifact) {
					t.Fatalf("diagnostic result lost: %+v %v", retained, err)
				}
			})
		}
	})
	t.Run("incomplete_exit", func(t *testing.T) {
		f := newSummaryBindingFixture(t)
		e := f.event
		e.Kind = contract.EventFailed
		body, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE event_id='result'`, string(body)); err != nil {
			t.Fatal(err)
		}
		_, err = f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "task", WorkRevision: e.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "incomplete-accept"})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("exit without result was accepted: %v", err)
		}
	})
	t.Run("expired_result", func(t *testing.T) {
		f := newSummaryBindingFixture(t)
		e := f.event
		if _, err := f.db.sql.Exec(`UPDATE task_runtime SET work_revision=2 WHERE task_id='task'`); err != nil {
			t.Fatal(err)
		}
		_, err := f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "task", WorkRevision: e.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "expired-accept"})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("expired result accepted: %v", err)
		}
		var decisions int
		if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&decisions); err != nil || decisions != 0 {
			t.Fatalf("expired acceptance persisted decisions=%d err=%v", decisions, err)
		}
	})
	t.Run("old_candidate", func(t *testing.T) {
		f := newReworkFixture(t)
		wrongOID := strings.Repeat("e", 40)
		f.spec.CandidateOID = wrongOID
		if _, err := f.db.QueueRework(context.Background(), f.spec); !errors.Is(err, CodeError("rework_candidate_mismatch")) {
			t.Fatalf("mismatched old candidate accepted: %v", err)
		}
		f.spec.CandidateOID = strings.Repeat("b", 40)
		receipt, err := f.db.QueueRework(context.Background(), f.spec)
		if err != nil || receipt.WorkRevision != 2 {
			t.Fatalf("rework=%+v err=%v", receipt, err)
		}
		_, err = f.db.ReviewResult(context.Background(), ReviewSpec{TaskID: "edit", WorkRevision: 1, EventID: f.result.EventID, EventRevision: f.spec.Candidate.EventRevision, EventHash: f.result.PayloadHash, ActionSlot: "action-" + runtimeHash(f.host + "\x00" + f.result.EventID)[:32], Decision: "accept", CommandID: "old-candidate-accept"})
		if err != nil {
			t.Fatal(err)
		}
		var status string
		if err = f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='edit'`).Scan(&status); err != nil || status != "ready" {
			t.Fatalf("old candidate completed revised task: status=%s err=%v", status, err)
		}
	})
}

func pointerTo(value int) *int {
	return &value
}
