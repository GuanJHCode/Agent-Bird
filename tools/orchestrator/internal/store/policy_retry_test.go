package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func newPolicyFixture(t *testing.T, tasks []TaskSpec) budgetBatchFixture {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: tasks})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	h, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, h, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return budgetBatchFixture{db: db, host: h, grant: grant}
}
func policyTask(id string, attempts int) TaskSpec {
	return TaskSpec{ID: id, RunID: "run", MaxAttempts: attempts, MaxActiveMS: 500, ExecutionPolicy: &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 1000, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 200, Retry: &contract.RetryPolicy{Codes: []string{"probe_start_eagain", "probe_start_eintr"}, BackoffMS: []int64{5000, 20000}[:min(2, attempts-1)]}}}
}
func policyRejection(t *testing.T, f *budgetBatchFixture, code string) contract.Event {
	t.Helper()
	f.sequence++
	body, err := json.Marshal(map[string]string{"status": "launch_rejected", "reason": code})
	if err != nil {
		t.Fatal(err)
	}
	var artifact *contract.ArtifactRef
	if f.grant.HostArtifacts == nil { // Only explicitly prepared pre-upgrade fixtures.
		artifact = &contract.ArtifactRef{ID: "launch-failure", Path: "/private/historical/launch-failure", Size: int64(len(body)), SHA256: runtimeHash(string(body))}
	} else {
		artifact = managedHostArtifactForTest(t, f.db, f.grant, "prelaunch", 0, body)
	}
	return contract.Event{Version: 1, ProducerID: f.host, EventID: fmt.Sprintf("policy-%d", f.sequence), RunID: f.grant.RunID, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: f.grant.WorkRevision, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.sequence, Kind: contract.EventLaunchRejected, PayloadHash: artifact.SHA256, Artifact: artifact, Failure: &contract.FailureFacts{Version: 1, Stage: "prelaunch_probe", Code: code, WorkerState: "not_started", ProbeState: "not_started"}}
}
func TestPolicyRetryDurableBackoffExactBinding(t *testing.T) {
	f := newPolicyFixture(t, []TaskSpec{policyTask("task", 3)})
	ctx := context.Background()
	e := policyRejection(t, &f, "probe_start_eagain")
	before := time.Now().UnixMilli()
	if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	var at int64
	var slot, status string
	var count int
	if err := f.db.sql.QueryRow(`SELECT d.eligible_at_unix_ms,d.action_slot,t.status FROM retry_decisions d JOIN tasks t ON t.id=d.task_id WHERE d.origin='policy'`).Scan(&at, &slot, &status); err != nil || at < before+5000 || status != "ready" {
		t.Fatalf("durable retry missing: at=%d status=%s err=%v", at, status, err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatalf("early claim=%v", err)
	}
	if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	f.db.Close()
	var err error
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	var reopened int64
	if err = f.db.sql.QueryRow(`SELECT eligible_at_unix_ms FROM retry_decisions WHERE action_slot=?`, slot).Scan(&reopened); err != nil || reopened != at {
		t.Fatalf("backoff reset %d/%d %v", at, reopened, err)
	}
	if _, err = f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatalf("reopen early=%v", err)
	}
	if _, err = f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE action_slot=?`, slot); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || next.AttemptID == f.grant.AttemptID {
		t.Fatalf("due claim %+v %v", next, err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings b JOIN attempts a ON a.id=b.attempt_id WHERE b.action_slot=? AND a.attempt_no=2`, slot).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exact binding=%d %v", count, err)
	}
	if _, err = f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatalf("duplicate claim=%v", err)
	}
	ids, err := f.db.RetirableHosts(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("pending retry retired Host %v %v", ids, err)
	}
}
func TestPolicyRetryPendingCapacityCannotBeStolen(t *testing.T) {
	a, b := policyTask("a", 2), policyTask("b", 2)
	a.BudgetGroupID = "shared"
	b.BudgetGroupID = "shared"
	a.ExecutionPolicy.GroupMaxAttempts = 2
	b.ExecutionPolicy.GroupMaxAttempts = 2
	f := newPolicyFixture(t, []TaskSpec{a, b})
	e := policyRejection(t, &f, "probe_start_eintr")
	if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); !errors.Is(err, ErrAttemptLimit) {
		t.Fatalf("sibling stole pending capacity: %v", err)
	}
	var n int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&n)
	if n != 1 {
		t.Fatalf("allocated %d attempts before backoff", n)
	}
	if _, err := f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1 WHERE origin='policy'`); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || next.TaskID != "a" {
		t.Fatalf("blocked sibling starved due promised retry: %+v %v", next, err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sibling allocated attempts=%d err=%v", n, err)
	}
}
func TestPolicyFailureFactsExcludeUnsafeEvents(t *testing.T) {
	for _, kind := range []string{"running", "unknown", "failed", "progress"} {
		t.Run(kind, func(t *testing.T) {
			f := newPolicyFixture(t, []TaskSpec{policyTask("task", 3)})
			e := policyRejection(t, &f, "probe_start_eagain")
			e.Kind = kind
			if _, err := f.db.CommitHostEvent(context.Background(), e); err == nil {
				t.Fatal("non-rejection accepted failure facts")
			}
		})
	}
	for _, change := range []string{"code", "worker", "probe", "question", "started"} {
		t.Run(change, func(t *testing.T) {
			f := newPolicyFixture(t, []TaskSpec{policyTask("task", 3)})
			if change == "started" {
				f.start(t)
			}
			e := policyRejection(t, &f, "probe_start_eagain")
			switch change {
			case "code":
				e.Failure.Code = "launch_failed"
			case "worker":
				e.Failure.WorkerState = "unknown"
			case "probe":
				e.Failure.ProbeState = "started"
			case "question":
				e.QuestionID = "permission"
			}
			if _, err := f.db.CommitHostEvent(context.Background(), e); err == nil {
				t.Fatal("unsafe facts accepted")
			}
			var n int
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&n)
			if n != 0 {
				t.Fatal("unsafe facts scheduled")
			}
		})
	}
}
func TestExecutionPolicyAdmissionDefaultsAndMixedGroup(t *testing.T) {
	for _, raw := range []string{`{"version":1,"version":1}`, `{"version":1,"extra":1}`, `{"Version":1}`} {
		var p contract.ExecutionPolicy
		if json.Unmarshal([]byte(raw), &p) == nil {
			t.Fatalf("open JSON accepted %s", raw)
		}
	}
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := policyTask("a", 2)
	a.BudgetGroupID = "same"
	b := TaskSpec{ID: "b", RunID: "run", MaxAttempts: 2, BudgetGroupID: "same"}
	_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{a, b}})
	if !errors.Is(err, CodeError("execution_policy_group_mixed")) {
		t.Fatalf("mixed group admitted: %v", err)
	}
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n)
	if n != 0 {
		t.Fatal("partial admission")
	}
}

func TestPolicyTaskCumulativeBudgetStopsNewAttempts(t *testing.T) {
	task := policyTask("task", 3)
	task.ExecutionPolicy.Retry = nil
	task.MaxActiveMS = 200
	task.ExecutionPolicy.TaskMaxActiveMS = 300
	f := newPolicyFixture(t, []TaskSpec{task})
	ctx := context.Background()
	for i, used := range []int64{200, 100} {
		f.start(t)
		f.sequence++
		failure := contract.Event{Version: 1, ProducerID: f.host, EventID: fmt.Sprintf("failure-%d", f.sequence), RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.sequence, Kind: contract.EventFailed, PayloadHash: strings.Repeat("f", 64)}
		if _, err := f.db.CommitHostEvent(ctx, failure); err != nil {
			t.Fatal(err)
		}
		f.outcome = contract.EventFailed
		f.send(t, contract.EventExited, used)
		var hash string
		var rev int64
		var slot string
		if err := f.db.sql.QueryRow(`SELECT payload_hash,event_revision,action_slot FROM runtime_events WHERE event_id=?`, failure.EventID).Scan(&hash, &rev, &slot); err != nil {
			t.Fatal(err)
		}
		spec := RetrySpec{TaskID: "task", WorkRevision: 1, EventID: failure.EventID, EventRevision: rev, EventHash: hash, ActionSlot: slot, SegmentID: f.grant.SegmentID, NextAttemptNo: i + 2, CommandID: fmt.Sprintf("owner-%d", i)}
		if i == 1 {
			if _, err := f.db.QueueRetry(ctx, spec); !errors.Is(err, CodeError("budget_exhausted")) {
				t.Fatalf("task budget refilled while group had 700ms: %v", err)
			}
			break
		}
		if _, err := f.db.QueueRetry(ctx, spec); err != nil {
			t.Fatal(err)
		}
		next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil || next.GrantedActiveMS != 100 {
			t.Fatalf("task remainder=%d want100 err=%v", next.GrantedActiveMS, err)
		}
		f.grant = next
	}
	if got := ledgerGroupCharged(t, f.db, "task"); got != 300 {
		t.Fatalf("charged=%d want300", got)
	}
}

func TestPolicyUnknownRetainsBothTaskAndGroupReservation(t *testing.T) {
	f := newPolicyFixture(t, []TaskSpec{policyTask("task", 3)})
	f.start(t)
	f.send(t, contract.EventUnknown, 0)
	if err := f.db.QueueResume(context.Background(), "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("UNKNOWN resumable", err)
	}
	var used int64
	var status string
	if err := f.db.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&status, &used); err != nil || status != "reserved" || used != 0 || ledgerGroupCharged(t, f.db, "task") != 200 {
		t.Fatalf("UNKNOWN refunded %s/%d %v", status, used, err)
	}
}

func TestPolicyRetryStopsAfterFiniteAuthorizedBackoff(t *testing.T) {
	f := newPolicyFixture(t, []TaskSpec{policyTask("task", 3)})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		e := policyRejection(t, &f, "probe_start_eagain")
		if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		var n int
		f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE origin='policy'`).Scan(&n)
		if n != min(i+1, 2) {
			t.Fatalf("retry count=%d at rejection%d", n, i)
		}
		if i == 2 {
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
				t.Fatalf("fourth attempt: %v", err)
			}
			break
		}
		var at int64
		f.db.sql.QueryRow(`SELECT eligible_at_unix_ms FROM retry_decisions ORDER BY backoff_index DESC LIMIT 1`).Scan(&at)
		lower := int64(5000)
		if i == 1 {
			lower = 20000
		}
		if at < time.Now().UnixMilli()+lower-100 {
			t.Fatalf("backoff was reset/shortened at=%d", at)
		}
		f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1`)
		next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		f.grant = next
	}
	var attempts int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts)
	if attempts != 3 || ledgerGroupCharged(t, f.db, "task") != 0 {
		t.Fatalf("not_started recycled capacity: %d attempts", attempts)
	}
}
