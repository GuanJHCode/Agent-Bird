package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// A real SubmitPlan/ClaimReady boundary: ignoring the public policy must fail.
func TestExecutionPolicySegmentGrantAndContinuation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var task TaskSpec
	if err = json.Unmarshal([]byte(`{"ID":"task","RunID":"run","MaxAttempts":3,"MaxActiveMS":700,"execution_policy":{"version":1,"group_max_attempts":3,"group_max_active_ms":1000,"task_max_active_ms":600,"segment_max_active_ms":200}}`), &task); err != nil {
		t.Fatal(err)
	}
	// Attempt must fit its task; use 500 within a 600 cumulative allowance.
	task.MaxActiveMS = 500
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{task}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if grant.GrantedActiveMS != 200 {
		t.Fatalf("policy ignored: grant=%d want 200", grant.GrantedActiveMS)
	}
	f := budgetBatchFixture{db: db, host: host, grant: grant}
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 150)
	if err = db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatal(err)
	}
	next, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil || next.AttemptID != grant.AttemptID || next.GrantedActiveMS != 200 {
		t.Fatalf("continuation=%+v err=%v", next, err)
	}
}

func TestExecutionPolicyDistinctTaskAttemptCapsShareGroup(t *testing.T) {
	a, b := policyTask("a", 2), policyTask("b", 2)
	a.ExecutionPolicy.Retry = nil
	b.ExecutionPolicy.Retry = nil
	a.BudgetGroupID = "shared"
	b.BudgetGroupID = "shared"
	a.ExecutionPolicy.GroupMaxAttempts = 4
	b.ExecutionPolicy.GroupMaxAttempts = 4
	a.MaxActiveMS = 200
	a.ExecutionPolicy.TaskMaxActiveMS = 300
	a.ExecutionPolicy.SegmentMaxActiveMS = 100
	b.MaxActiveMS = 300
	b.ExecutionPolicy.TaskMaxActiveMS = 350
	b.ExecutionPolicy.SegmentMaxActiveMS = 200
	f := newPolicyFixture(t, []TaskSpec{a, b})
	next, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || f.grant.GrantedActiveMS != 100 || next.GrantedActiveMS != 200 {
		t.Fatalf("distinct caps=%d/%d %v", f.grant.GrantedActiveMS, next.GrantedActiveMS, err)
	}
	if charged := ledgerGroupCharged(t, f.db, "shared"); charged != 300 {
		t.Fatalf("parallel reservations=%d want300", charged)
	}
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 50)
	if err = f.db.QueueResume(context.Background(), "a", 1); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || resumed.AttemptID != f.grant.AttemptID || resumed.GrantedActiveMS != 100 {
		t.Fatalf("group continuation %+v %v", resumed, err)
	}
}

func TestExecutionPolicyDefaultsAndBoundsAtAdmission(t *testing.T) {
	f := newPolicyFixture(t, []TaskSpec{{ID: "task", RunID: "run", ExecutionPolicy: &contract.ExecutionPolicy{Version: 1}}})
	if f.grant.GrantedActiveMS != 3_600_000 {
		t.Fatalf("default attempt grant=%d", f.grant.GrantedActiveMS)
	}
	var attempts int
	var raw string
	if err := f.db.sql.QueryRow(`SELECT t.max_attempts,p.body_json FROM tasks t JOIN task_execution_policies p ON p.task_id=t.id WHERE t.id='task'`).Scan(&attempts, &raw); err != nil {
		t.Fatal(err)
	}
	var p contract.ExecutionPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || p.GroupMaxAttempts != 3 || p.GroupMaxActiveMS != 7_200_000 || p.TaskMaxActiveMS != 7_200_000 || p.SegmentMaxActiveMS != 3_600_000 || p.Retry != nil {
		t.Fatalf("default grants=%d %+v", attempts, p)
	}
	for _, name := range []string{"version", "negative", "group_attempts", "task_attempts", "group_time", "task_time", "attempt_time", "segment", "code", "duplicate_code", "zero_delay", "too_many_delays"} {
		t.Run(name, func(t *testing.T) {
			task := policyTask("bad", 3)
			switch name {
			case "version":
				task.ExecutionPolicy.Version = 2
			case "negative":
				task.ExecutionPolicy.GroupMaxActiveMS = -1
			case "group_attempts":
				task.ExecutionPolicy.GroupMaxAttempts = 257
			case "task_attempts":
				task.MaxAttempts = 17
			case "group_time":
				task.ExecutionPolicy.GroupMaxActiveMS = 86_400_001
			case "task_time":
				task.ExecutionPolicy.TaskMaxActiveMS = 1001
			case "attempt_time":
				task.MaxActiveMS = 601
			case "segment":
				task.ExecutionPolicy.SegmentMaxActiveMS = 501
			case "code":
				task.ExecutionPolicy.Retry.Codes = []string{"permission"}
			case "duplicate_code":
				task.ExecutionPolicy.Retry.Codes = []string{"probe_start_eagain", "probe_start_eagain"}
			case "zero_delay":
				task.ExecutionPolicy.Retry.BackoffMS = []int64{0}
			case "too_many_delays":
				task.ExecutionPolicy.Retry.BackoffMS = []int64{1, 2, 3}
			}
			db, err := Open(filepath.Join(t.TempDir(), "invalid.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{task}})
			if err == nil {
				t.Fatalf("invalid policy admitted %+v", task)
			}
			var count int
			db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count)
			if count != 0 {
				t.Fatal("invalid policy partially committed")
			}

		})
	}
}
