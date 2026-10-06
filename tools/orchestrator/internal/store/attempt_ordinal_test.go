package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// This is a Store transaction fixture, not a physical Host/process proof.
func TestTaskLocalAttemptOrdinalSurvivesSiblingInterleave(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:  RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner-birth"},
		Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "a", RunID: "run", BudgetGroupID: "shared", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)},
			{ID: "b", RunID: "run", BudgetGroupID: "shared", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner-birth", HostGeneration: "generation", PID: 10, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || first.TaskID != "a" {
		t.Fatalf("initial claim task=%s err=%v", first.TaskID, err)
	}
	exit := 1
	for i, kind := range []string{contract.EventFailed, contract.EventExited} {
		event := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprintf("event-%d", i+1), RunID: first.RunID, TaskID: first.TaskID, AttemptID: first.AttemptID, SegmentID: first.SegmentID, WorkRevision: first.WorkRevision, ExecutionEpoch: 1, CommandID: first.CommandID, Sequence: int64(i + 1), Kind: kind, PayloadHash: fmt.Sprintf("%064x", i+1), ExitCode: &exit}
		if kind == contract.EventExited {
			event.ActiveMS = 1
		}
		if _, err = db.CommitHostEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.CollectPending(ctx, "a", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("failure collect count=%d err=%v", len(page.Events), err)
	}
	failure := page.Events[0]
	promise, err := db.QueueRetry(ctx, RetrySpec{TaskID: "a", WorkRevision: 1, EventID: failure.EventID, EventRevision: failure.EventRevision, EventHash: failure.PayloadHash, ActionSlot: failure.ActionSlot, SegmentID: failure.SegmentID, NextAttemptNo: 2, CommandID: "retry-a"})
	if err != nil {
		t.Fatal(err)
	}
	// B's older ready sequence interleaves after A's durable promise.
	sibling, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || sibling.TaskID != "b" {
		t.Fatalf("sibling claim task=%s err=%v", sibling.TaskID, err)
	}
	retry, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || retry.TaskID != "a" {
		t.Fatalf("retry claim task=%s err=%v", retry.TaskID, err)
	}
	ordinal := func(id string) int {
		t.Helper()
		var n int
		if err := db.sql.QueryRow(`SELECT attempt_no FROM attempts WHERE id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := ordinal(first.AttemptID); got != 1 {
		t.Errorf("first task ordinal=%d, want 1", got)
	}
	if got := ordinal(sibling.AttemptID); got != 1 {
		t.Errorf("sibling first ordinal=%d, want 1", got)
	}
	if got := ordinal(retry.AttemptID); got != promise.NextAttemptNo {
		t.Errorf("A admitted ordinal=%d differs from durable promise=%d", got, promise.NextAttemptNo)
	}
	var count int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id='shared'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("shared capacity charged count=%d err=%v", count, err)
	}
	t.Logf("Store causal chain: A failed ordinal=1; durable promise=%d; B ordinal=%d; A retry ordinal=%d; shared admitted count=%d", promise.NextAttemptNo, ordinal(sibling.AttemptID), ordinal(retry.AttemptID), count)
}
