package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func budgetRevisionRuntimeFixture(t *testing.T, task TaskSpec, additional ...TaskSpec) (budgetBatchFixture, BudgetRevisionCommand) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	submitted, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: append([]TaskSpec{task}, additional...)})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	command := BudgetRevisionCommand{AnchorTaskID: task.ID, Controller: "owner", ControlToken: submitted.ControlToken, Request: BudgetRevisionRequest{BudgetGroupID: task.ID, CommandID: "increase", GroupMaxAttempts: 4, GroupMaxActiveMS: 1500, Tasks: []BudgetRevisionTask{{TaskID: task.ID, WorkRevision: 1, MaxAttempts: 4, AttemptMaxActiveMS: 700, TaskMaxActiveMS: 900, SegmentMaxActiveMS: 400}}}}
	return budgetBatchFixture{db: db, host: host, grant: grant}, command
}

func TestBudgetRevisionRejectsLegacyPolicyWithoutMutatingBudget(t *testing.T) {
	task := policyTask("task", 3)
	task.ExecutionPolicy = nil
	f, command := budgetRevisionRuntimeFixture(t, task)
	var before, after int64
	if err := f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ReviseBudget(context.Background(), command); err != CodeError("budget_revision_group_invalid") {
		t.Fatal("legacy group acquired mutable policy caps", err)
	}
	var revisions, receipts, policies int
	var granted int64
	if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM budget_revisions),(SELECT COUNT(*) FROM budget_revision_receipts),(SELECT COUNT(*) FROM task_execution_policies),b.granted_ms,u.total_bytes FROM budget_runtime b JOIN storage_usage u ON u.scope='run' AND u.scope_id='run' WHERE b.segment_id=?`, f.grant.SegmentID).Scan(&revisions, &receipts, &policies, &granted, &after); err != nil || revisions != 0 || receipts != 0 || policies != 0 || granted != f.grant.GrantedActiveMS || before != after {
		t.Fatalf("legacy rejection changed ledger/caps/usage: %d/%d/%d grant=%d/%d usage=%d/%d err=%v", revisions, receipts, policies, granted, f.grant.GrantedActiveMS, before, after, err)
	}
}

func TestBudgetRevisionPreservesPendingPolicyRetryPromiseAcrossReopen(t *testing.T) {
	f, command := budgetRevisionRuntimeFixture(t, policyTask("task", 3))
	ctx := context.Background()
	event := policyRejection(&f, "probe_start_eagain")
	if _, err := f.db.CommitHostEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	readPromise := func() (string, string, string, int64, int64) {
		t.Helper()
		var slot, hash, payload string
		var due, held int64
		if err := f.db.sql.QueryRow(`SELECT d.action_slot,d.policy_sha256,d.revision_payload,d.eligible_at_unix_ms,b.reserved_active_ms FROM retry_decisions d JOIN decision_attempt_bindings b ON b.action_slot=d.action_slot WHERE d.task_id='task' AND d.origin='policy' AND b.attempt_id IS NULL AND b.retired=0`).Scan(&slot, &hash, &payload, &due, &held); err != nil {
			t.Fatal("missing real policy retry promise", err)
		}
		return slot, hash, payload, due, held
	}
	slot, hash, payload, due, held := readPromise()
	if held != 200 || hash == "" || due <= 0 {
		t.Fatal("fixture did not establish original policy promise", held, due)
	}
	receipt, err := f.db.ReviseBudget(ctx, command)
	if err != nil || receipt.Revision != 1 {
		t.Fatal("amend pending task budget", err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	afterSlot, afterHash, afterPayload, afterDue, afterHeld := readPromise()
	if afterSlot != slot || afterHash != hash || afterPayload != payload || afterDue != due || afterHeld != held {
		t.Fatal("revision/reopen rewrote the existing policy promise")
	}
	if replay, err := f.db.ReviseBudget(ctx, command); err != nil || replay != receipt {
		t.Fatal("revision receipt did not survive reopen", err)
	}
	// Advance only the fixture's scheduling clock, as the existing policy
	// contract tests do; never replace the real promise or its fixed hold.
	if _, err = f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE action_slot=?`, slot); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 1)
	if err != nil || next.AttemptID == f.grant.AttemptID || next.GrantedActiveMS != held {
		t.Fatalf("amendment enlarged/lost promised grant: %+v held=%d err=%v", next, held, err)
	}
	var bound, finalHash string
	var finalHeld, finalGranted int64
	if err = f.db.sql.QueryRow(`SELECT b.attempt_id,b.reserved_active_ms,d.policy_sha256,r.granted_ms FROM decision_attempt_bindings b JOIN retry_decisions d ON d.action_slot=b.action_slot JOIN budget_runtime r ON r.segment_id=? WHERE b.action_slot=?`, next.SegmentID, slot).Scan(&bound, &finalHeld, &finalHash, &finalGranted); err != nil || bound != next.AttemptID || finalHeld != held || finalHash != hash || finalGranted != held {
		t.Fatal("Claim lost original reservation/hash", bound, finalHeld, finalHash, finalGranted, err)
	}
}
