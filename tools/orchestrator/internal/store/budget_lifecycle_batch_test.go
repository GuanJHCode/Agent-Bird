package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const (
	batchGrantMS        int64 = 1_000
	batchDefaultGrantMS int64 = 3_600_000
)

type budgetBatchFixture struct {
	db       *DB
	host     string
	grant    contract.LaunchCommand
	sequence int64
	outcome  string
}

func newBudgetBatchFixture(t *testing.T, path string) budgetBatchFixture {
	t.Helper()
	ctx := context.Background()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"},
		Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: batchGrantMS}},
	})
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
	if grant.GrantedActiveMS != batchGrantMS {
		t.Fatalf("grant=%d want=%d", grant.GrantedActiveMS, batchGrantMS)
	}
	return budgetBatchFixture{db: db, host: host, grant: grant}
}

func (f *budgetBatchFixture) send(t *testing.T, kind string, activeMS int64) contract.Event {
	t.Helper()
	f.sequence++
	event := contract.Event{
		Version:        1,
		ProducerID:     f.host,
		EventID:        fmt.Sprintf("event-%d", f.sequence),
		RunID:          f.grant.RunID,
		TaskID:         f.grant.TaskID,
		AttemptID:      f.grant.AttemptID,
		SegmentID:      f.grant.SegmentID,
		WorkRevision:   f.grant.WorkRevision,
		ExecutionEpoch: 1,
		CommandID:      f.grant.CommandID,
		Sequence:       f.sequence,
		Kind:           kind,
		PayloadHash:    fmt.Sprintf("hash-%d", f.sequence),
		ActiveMS:       activeMS,
	}
	if kind == contract.EventExited && f.outcome == contract.EventResult {
		exit := 0
		event.ExitCode = &exit
	}
	if kind != contract.EventExited {
		f.outcome = kind
	}
	if _, err := f.db.CommitHostEvent(context.Background(), event); err != nil {
		t.Fatalf("commit %s: %v", kind, err)
	}
	return event
}

func (f *budgetBatchFixture) start(t *testing.T) {
	t.Helper()
	f.send(t, contract.EventPrepared, 0)
	f.send(t, contract.EventSpawned, 0)
	f.send(t, contract.EventRunning, 0)
}

func (f *budgetBatchFixture) assertLedger(t *testing.T, wantStatus string, wantUsed int64) {
	t.Helper()
	var status string
	var grant, used int64
	err := f.db.sql.QueryRow(`SELECT status,granted_ms,used_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&status, &grant, &used)
	if err != nil || status != wantStatus || grant != batchGrantMS || used != wantUsed {
		t.Fatalf("ledger status=%q grant=%d used=%d err=%v", status, grant, used, err)
	}
}

func (f *budgetBatchFixture) assertTask(t *testing.T, want string) {
	t.Helper()
	var status string
	if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != want {
		t.Fatalf("task status=%q err=%v", status, err)
	}
}

func TestBudgetLifecycleTerminalPathsSettleIndependentLedger(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		prepare    func(t *testing.T, fixture *budgetBatchFixture)
		activeMS   int64
		wantUsed   int64
		wantStatus string
	}{
		{"normal", func(t *testing.T, fixture *budgetBatchFixture) { fixture.send(t, contract.EventResult, 0) }, 239, 239, "result_ready"},
		{"deadline", func(t *testing.T, fixture *budgetBatchFixture) {
			dispatch, err := fixture.db.RequestStop(context.Background(), "task", 1, "deadline_exceeded")
			if err != nil || dispatch.Command.Reason != "deadline_exceeded" {
				t.Fatalf("deadline stop=%+v err=%v", dispatch, err)
			}
			fixture.send(t, contract.EventStopped, 0)
		}, 0, batchGrantMS, "interrupted"},
		{"owner_stop", func(t *testing.T, fixture *budgetBatchFixture) {
			dispatch, err := fixture.db.RequestStop(context.Background(), "task", 1, "owner_stop_requested")
			if err != nil || dispatch.Command.Reason != "owner_stop_requested" {
				t.Fatalf("owner stop=%+v err=%v", dispatch, err)
			}
			fixture.send(t, contract.EventStopped, 0)
		}, 317, 317, "interrupted"},
		{"running_failure", func(t *testing.T, fixture *budgetBatchFixture) { fixture.send(t, contract.EventFailed, 0) }, 0, batchGrantMS, "failed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
			defer fixture.db.Close()
			fixture.start(t)
			testCase.prepare(t, &fixture)
			exit := fixture.send(t, contract.EventExited, testCase.activeMS)
			fixture.assertLedger(t, "settled", testCase.wantUsed)
			fixture.assertTask(t, testCase.wantStatus)
			if _, err := fixture.db.CommitHostEvent(context.Background(), exit); err != nil {
				t.Fatalf("identical exit replay: %v", err)
			}
			fixture.assertLedger(t, "settled", testCase.wantUsed)
		})
	}
}

func TestBudgetLifecycleStartupFailureSettlesLaunchRequestedGrant(t *testing.T) {
	fixture := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer fixture.db.Close()
	fixture.send(t, contract.EventFailed, 0)
	exit := fixture.send(t, contract.EventExited, 0)
	fixture.assertLedger(t, "settled", batchGrantMS)
	fixture.assertTask(t, "failed")
	if _, err := fixture.db.CommitHostEvent(context.Background(), exit); err != nil {
		t.Fatalf("identical startup exit replay: %v", err)
	}
	fixture.assertLedger(t, "settled", batchGrantMS)
}

func TestBudgetLifecycleUnknownRetainsReservationAcrossCoordinatorRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	fixture := newBudgetBatchFixture(t, path)
	fixture.start(t)
	fixture.send(t, contract.EventUnknown, 0)
	fixture.assertLedger(t, "reserved", 0)
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixture.db = db
	fixture.assertLedger(t, "reserved", 0)
	if _, err := db.ClaimReady(context.Background(), fixture.host, 1, 1); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("unknown reservation incorrectly dispatched work: %v", err)
	}
}

func TestBudgetLifecycleTechnicalRetryAndFeedbackReworkAccumulate(t *testing.T) {
	t.Run("technical_retry", func(t *testing.T) {
		fixture := newReviewRetryFixture(t, contract.EventStopped)
		if _, err := fixture.db.QueueRetry(context.Background(), fixture.spec); err != nil {
			t.Fatal(err)
		}
		grant, err := fixture.db.ClaimReady(context.Background(), fixture.host, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		var sequence int64
		if err = fixture.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, fixture.host).Scan(&sequence); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{contract.EventStopped, contract.EventExited} {
			sequence++
			event := contract.Event{Version: 1, ProducerID: fixture.host, EventID: fmt.Sprintf("retry-%d", sequence), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("retry-hash-%d", sequence)}
			if kind == contract.EventExited {
				event.ActiveMS = 437
			}
			if _, err = fixture.db.CommitHostEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		var attempts int
		var used int64
		if err = fixture.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='review'`).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if err = fixture.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id=?`, grant.BudgetGroupID).Scan(&used); err != nil || attempts != 2 || used != 1_671 {
			t.Fatalf("retry attempts=%d used=%d err=%v", attempts, used, err)
		}
	})
	t.Run("feedback_rework", func(t *testing.T) {
		fixture := newReworkFixture(t)
		if _, err := fixture.db.QueueRework(context.Background(), fixture.spec); err != nil {
			t.Fatal(err)
		}
		grant, err := fixture.db.ClaimReady(context.Background(), fixture.host, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		var sequence int64
		if err = fixture.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, fixture.host).Scan(&sequence); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{contract.EventStopped, contract.EventExited} {
			sequence++
			event := contract.Event{Version: 1, ProducerID: fixture.host, EventID: fmt.Sprintf("rework-%d", sequence), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("rework-hash-%d", sequence)}
			if kind == contract.EventExited {
				event.ActiveMS = 601
			}
			if _, err = fixture.db.CommitHostEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		var attempts int
		var used int64
		if err = fixture.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='edit'`).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if err = fixture.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id=?`, grant.BudgetGroupID).Scan(&used); err != nil || attempts != 2 || used != batchDefaultGrantMS+601 {
			t.Fatalf("rework attempts=%d used=%d err=%v", attempts, used, err)
		}
	})
}
