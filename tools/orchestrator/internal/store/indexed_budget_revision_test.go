package store

import (
	"context"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Used by the real indexed DAG transaction fixture, including configuration
// package/preparation admission and final coverage-based integration acceptance.
type indexedBudgetPromise struct {
	Slot, PolicyHash, PolicyJSON, DecisionHash, Payload string
	Due, Held, Granted, Used                            int64
}

func readIndexedBudgetPromise(t *testing.T, db *DB, original contract.LaunchCommand) indexedBudgetPromise {
	t.Helper()
	var p indexedBudgetPromise
	err := db.sql.QueryRow(`SELECT d.action_slot,p.policy_sha256,p.body_json,d.policy_sha256,d.revision_payload,d.eligible_at_unix_ms,b.reserved_active_ms,r.granted_ms,r.used_ms FROM retry_decisions d JOIN task_execution_policies p ON p.task_id=d.task_id JOIN decision_attempt_bindings b ON b.action_slot=d.action_slot JOIN budget_runtime r ON r.segment_id=? WHERE d.task_id='r2' AND d.origin='policy' AND b.attempt_id IS NULL AND b.retired=0`, original.SegmentID).Scan(&p.Slot, &p.PolicyHash, &p.PolicyJSON, &p.DecisionHash, &p.Payload, &p.Due, &p.Held, &p.Granted, &p.Used)
	if err != nil {
		t.Fatal("missing original indexed policy promise", err)
	}
	return p
}

func assertIndexedBudgetSchema(t *testing.T, db *DB, want int) {
	t.Helper()
	var version, tables int
	if err := db.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('budget_revisions','budget_revision_receipts'))`).Scan(&version, &tables); err != nil || version != want || want == 17 && tables != 0 || want == 18 && tables != 2 {
		t.Fatal("indexed budget migration boundary", version, tables, err)
	}
}

func amendIndexedBudgetPromise(t *testing.T, db *DB, original contract.LaunchCommand, token string, before indexedBudgetPromise) {
	t.Helper()
	assertIndexedBudgetSchema(t, db, 18)
	payload, err := adapter.DecodeInvocationPayload(original.AdapterPayload)
	if err != nil || payload.ConfigGeneration == nil || payload.CandidateAction == nil || payload.CandidateAction.ReviewIndex == nil || original.DependencyInputs == nil || len(original.DependencyInputs.Inputs) == 0 || original.Input == nil || before.Held != 200 || before.Granted != 200 || before.Used != 0 || before.Due <= 0 || before.PolicyHash == "" || before.DecisionHash != before.PolicyHash {
		t.Fatal("fixture omitted config/index/vector/original budget", before, err)
	}
	if got := readIndexedBudgetPromise(t, db, original); got != before {
		t.Fatal("reopen or migration rewrote indexed retry promise")
	}
	command := BudgetRevisionCommand{AnchorTaskID: "r2", Controller: "owner", ControlToken: token, Request: BudgetRevisionRequest{BudgetGroupID: "r2", CommandID: "indexed-budget-increase", GroupMaxAttempts: 4, GroupMaxActiveMS: 1500, Tasks: []BudgetRevisionTask{{TaskID: "r2", WorkRevision: 1, MaxAttempts: 4, AttemptMaxActiveMS: 700, TaskMaxActiveMS: 900, SegmentMaxActiveMS: 400}}}}
	receipt, err := db.ReviseBudget(context.Background(), command)
	if err != nil || receipt.Revision != 1 {
		t.Fatal("indexed review budget amendment", receipt, err)
	}
	if replay, err := db.ReviseBudget(context.Background(), command); err != nil || replay != receipt {
		t.Fatal("indexed budget receipt replay", replay, err)
	}
	if got := readIndexedBudgetPromise(t, db, original); got != before {
		t.Fatal("amendment rewrote policy/config/input promise, old hold or historical grant")
	}
	snapshot, err := db.TaskSnapshot(context.Background(), "r2")
	if err != nil || snapshot.EffectiveBudget == nil || snapshot.EffectiveBudget.Revision != 1 || snapshot.EffectiveBudget.SegmentMaxActiveMS != 400 || snapshot.EffectiveBudget.AttemptMaxActiveMS != 700 {
		t.Fatal("amendment not publicly visible on indexed task", snapshot, err)
	}
}

func assertIndexedBudgetClaim(t *testing.T, db *DB, original, next contract.LaunchCommand, before indexedBudgetPromise) {
	t.Helper()
	if next.GrantedActiveMS != before.Held || next.AttemptID == original.AttemptID || next.SegmentID == original.SegmentID {
		t.Fatal("effective caps enlarged or discarded original indexed promise", next.GrantedActiveMS, before.Held)
	}
	var attempt, policy, payload string
	var held, granted, oldGranted, oldUsed int64
	err := db.sql.QueryRow(`SELECT b.attempt_id,b.reserved_active_ms,d.policy_sha256,d.revision_payload,r.granted_ms,old.granted_ms,old.used_ms FROM decision_attempt_bindings b JOIN retry_decisions d ON d.action_slot=b.action_slot JOIN budget_runtime r ON r.segment_id=? JOIN budget_runtime old ON old.segment_id=? WHERE b.action_slot=?`, next.SegmentID, original.SegmentID, before.Slot).Scan(&attempt, &held, &policy, &payload, &granted, &oldGranted, &oldUsed)
	if err != nil || attempt != next.AttemptID || held != before.Held || granted != before.Held || policy != before.DecisionHash || payload != before.Payload || oldGranted != before.Granted || oldUsed != before.Used {
		t.Fatal("indexed Claim changed original decision or budget ledger", attempt, held, granted, oldGranted, oldUsed, err)
	}
}
