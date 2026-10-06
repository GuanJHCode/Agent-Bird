package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDecisionBindingsReworkConsumesExactEditAttempt(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	receipt, err := f.db.QueueRework(ctx, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings WHERE action_slot=? AND target_work_revision=2 AND attempt_id IS NULL`, f.spec.ActionSlot).Scan(&count); err != nil || count != 4 {
		t.Fatalf("four exact targets pending: count=%d err=%v", count, err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var bound, task string
	var revision, ordinal int
	if err = f.db.sql.QueryRow(`SELECT b.attempt_id,b.task_id,b.target_work_revision,a.attempt_no FROM decision_attempt_bindings b JOIN attempts a ON a.id=b.attempt_id WHERE b.action_slot=? AND b.task_id='edit'`, f.spec.ActionSlot).Scan(&bound, &task, &revision, &ordinal); err != nil || bound != g.AttemptID || task != g.TaskID || revision != receipt.WorkRevision || ordinal != 2 {
		t.Fatalf("exact consumed binding=%s/%s/%d/%d err=%v", bound, task, revision, ordinal, err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings WHERE action_slot=? AND attempt_id IS NULL`, f.spec.ActionSlot).Scan(&count); err != nil || count != 3 {
		t.Fatalf("successors pending count=%d err=%v", count, err)
	}
}

func TestInitialOpaqueNonCandidateGrantKeepsOriginalAdmission(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"kind":"gitops","operation":"validate","request":{"opaque":true}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: p.LaunchID, LaunchToken: p.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || g.TaskID != "task" {
		t.Fatalf("original opaque initial admission changed: task=%s err=%v", g.TaskID, err)
	}
}

func TestStartAttemptRejectsManagedWithoutLedgerWrites(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.StartAttempt(ctx, "task", "bypass-segment"); !errors.Is(err, CodeError("managed_attempt_requires_claim")) {
		t.Errorf("managed bypass err=%v", err)
	}
	for _, table := range []string{"attempts", "segments", "segment_runtime", "budget_runtime"} {
		var count int
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("bypass wrote %s count=%d err=%v", table, count, err)
		}
	}
	var status string
	if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "ready" {
		t.Errorf("managed status=%s err=%v", status, err)
	}
}

func TestTaskLocalOrdinalPreservesHistoricalLegacyGap(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.CreateRun(ctx, RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(ctx, TaskSpec{ID: "task", RunID: "run", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	first, err := db.StartAttempt(ctx, "task", "segment-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FinishAttempt(ctx, first.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	// Synthetic immutable pre-upgrade row with a gap; it must not be renumbered.
	if _, err = db.sql.Exec(`UPDATE attempts SET attempt_no=9 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	next, err := db.StartAttempt(ctx, "task", "segment-2")
	if err != nil || next.AttemptNo != 10 {
		t.Fatalf("after historical gap=%d err=%v", next.AttemptNo, err)
	}
	var original int
	if err = db.sql.QueryRow(`SELECT attempt_no FROM attempts WHERE id=?`, first.ID).Scan(&original); err != nil || original != 9 {
		t.Fatal("historical ordinal changed", original, err)
	}
}
