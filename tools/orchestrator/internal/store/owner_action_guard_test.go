package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func TestOwnerActionBindingRechecksAfterRebindBeforeEveryDecision(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateOwnerGrant(ctx, "owner", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth}, Host: HostLaunchSpec{OriginContextID: owner.OriginContextID, HostGeneration: owner.HostGeneration, Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := db.TaskOwnerActionBinding(ctx, "task", "owner", receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.LocalManaged {
		t.Fatal("DB did not identify local original context")
	}
	guarded := WithOwnerActionBinding(ctx, binding)
	// Same PID/birth with a new Host generation must still invalidate a snapshot.
	if err := db.RebindOwner(ctx, RebindOwnerSpec{RunID: "run", OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: "new-generation", AttachmentProofSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"resume": func() error { return db.QueueResume(guarded, "task", 1) },
		"request-changes": func() error {
			_, err := db.QueueRequestChanges(guarded, OwnerReworkSpec{TaskID: "task", WorkRevision: 1, EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "action", SegmentID: "segment", CandidateOID: strings.Repeat("a", 40), Feedback: "fix", Acceptance: "check", CommandID: "command"})
			return err
		},
		"answer": func() error {
			_, err := db.AnswerQuestion(guarded, AnswerSpec{TaskID: "task", WorkRevision: 1, QuestionID: "q", QuestionRevision: 1, Answer: "answer"})
			return err
		},
		"accept": func() error {
			_, err := db.ReviewResult(guarded, ReviewSpec{TaskID: "task", WorkRevision: 1, EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "action", Decision: "accept", CommandID: "command"})
			return err
		},
		"retry": func() error {
			_, err := db.QueueRetry(guarded, RetrySpec{TaskID: "task", WorkRevision: 1, EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "action", SegmentID: "segment", NextAttemptNo: 2, CommandID: "command"})
			return err
		},
		"rework": func() error {
			event := ReworkEvent{EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64)}
			_, err := db.QueueRework(guarded, ReworkSpec{TaskID: "task", WorkRevision: 1, ReviewTaskID: "review", ReviewWorkRevision: 1, Candidate: event, Review: event, CandidateOID: strings.Repeat("a", 40), ActionSlot: "action", Feedback: "fix", Acceptance: "check", CommandID: "command"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err != CodeError("owner_binding_changed") {
				t.Fatalf("stale owner snapshot reached business/replay path: %v", err)
			}
		})
	}
	// Check the helper at transaction scope too: neither another task nor a
	// forged/older logical context may borrow this snapshot.
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateOwnerActionBinding(guarded, tx, "different-task"); err != CodeError("owner_binding_changed") {
		t.Fatalf("snapshot crossed task: %v", err)
	}
	var decisions int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM retry_decisions)+(SELECT COUNT(*) FROM review_decisions)`).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("stale action wrote decision: count=%d err=%v", decisions, err)
	}
}

func TestOwnerActionBindingRechecksBeforeReturningExistingRetryReceipt(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	// Existing trusted Store fixture creates a real retry receipt. Capture its
	// old owner, then rebind before replaying exactly the same decision.
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	binding, err := f.db.TaskOwnerActionBinding(ctx, f.spec.TaskID, "owner", f.control)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RebindOwner(ctx, RebindOwnerSpec{RunID: binding.RunID, OriginContextID: binding.OriginContextID, OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "rebound-generation", AttachmentProofSHA256: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(WithOwnerActionBinding(ctx, binding), f.spec); err != CodeError("owner_binding_changed") {
		t.Fatalf("old snapshot replay bypassed current owner: %v", err)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE command_id=?`, f.spec.CommandID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay changed decisions: count=%d err=%v", count, err)
	}
}
