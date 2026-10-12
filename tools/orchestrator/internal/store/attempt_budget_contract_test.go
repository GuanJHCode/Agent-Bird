package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestAttemptBudgetResumesChargeSameAttempt(t *testing.T) {
	ctx := context.Background()
	f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer f.db.Close()
	first := f.grant
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 400)
	if err := f.db.QueueResume(ctx, first.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || second.GrantedActiveMS != 600 || second.AttemptID != first.AttemptID || second.SegmentID == first.SegmentID {
		t.Fatalf("same attempt second grant=%d want 600; command=%+v err=%v", second.GrantedActiveMS, second, err)
	}
	f.grant = second
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 250)
	if err := f.db.QueueResume(ctx, first.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	third, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || third.GrantedActiveMS != 350 || third.AttemptID != first.AttemptID || third.SegmentID == second.SegmentID {
		t.Fatalf("same attempt third grant=%d want 350; command=%+v err=%v", third.GrantedActiveMS, third, err)
	}
	f.grant = third
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 0) // Missing measurement consumes all 350.
	for _, want := range []struct {
		segment string
		grant   int64
		used    int64
	}{{first.SegmentID, 1000, 400}, {second.SegmentID, 600, 250}, {third.SegmentID, 350, 350}} {
		status, used, grant := ledgerBudgetRow(t, f.db, want.segment)
		if status != "settled" || grant != want.grant || used != want.used {
			t.Fatalf("independent ledger=%s/%d/%d want settled/%d/%d", status, grant, used, want.grant, want.used)
		}
	}
	if err := f.db.QueueResume(ctx, first.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, CodeError("budget_exhausted")) {
		t.Fatalf("exhausted own attempt error=%v want budget_exhausted", err)
	}
	var attempts, segments, reservations int
	var charged int64
	if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime),(SELECT SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END) FROM budget_runtime)`).Scan(&attempts, &segments, &reservations, &charged); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || segments != 3 || reservations != 3 || charged != 1000 {
		t.Fatalf("exhausted ledger counts=%d/%d/%d charged=%d want 1/3/3 and 1000", attempts, segments, reservations, charged)
	}
}

func TestAttemptBudgetReopenReplaysReservedRemainder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	f := newBudgetBatchFixture(t, path)
	t.Cleanup(func() { _ = f.db.Close() })
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 400)
	if err := f.db.QueueResume(ctx, f.grant.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || second.GrantedActiveMS != 600 {
		t.Fatalf("before reopen grant=%d want 600 err=%v", second.GrantedActiveMS, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path) // Persistence check, not an OS-crash claim.
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(pending) != 1 || pending[0].GrantedActiveMS != 600 || pending[0].AttemptID != second.AttemptID || pending[0].SegmentID != second.SegmentID || pending[0].CommandID != second.CommandID || pending[0].ReservationID != second.ReservationID || pending[0].LaunchIntentHash != second.LaunchIntentHash {
		t.Fatalf("reserved remainder replay changed: %+v err=%v", pending, err)
	}
	if charged := ledgerGroupCharged(t, f.db, "task"); charged != 1000 {
		t.Fatalf("reopen refunded/reset reserved remainder: charged=%d want 1000", charged)
	}
	f.grant = second
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	exited := f.send(t, contract.EventExited, 250)
	if _, err := f.db.CommitHostEvent(ctx, exited); err != nil {
		t.Fatalf("durable exited replay rejected: %v", err)
	}
	if charged := ledgerGroupCharged(t, f.db, "task"); charged != 650 {
		t.Fatalf("duplicate exit charged/refunded twice: charged=%d want 650", charged)
	}
	if err := f.db.QueueResume(ctx, second.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	third, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || third.GrantedActiveMS != 350 || third.AttemptID != second.AttemptID {
		t.Fatalf("reopen reset attempt: grant=%d want 350 err=%v", third.GrantedActiveMS, err)
	}
}

func TestAttemptBudgetUnknownDoesNotRefundRemainder(t *testing.T) {
	for _, measured := range []int64{250, 0} {
		name := "measured_exit"
		if measured == 0 {
			name = "missing_measurement"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
			defer f.db.Close()
			f.start(t)
			f.send(t, contract.EventStopped, 0)
			f.send(t, contract.EventExited, 400)
			if err := f.db.QueueResume(ctx, f.grant.TaskID, 1); err != nil {
				t.Fatal(err)
			}
			second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil || second.GrantedActiveMS != 600 {
				t.Fatalf("pre-UNKNOWN grant=%d want 600 err=%v", second.GrantedActiveMS, err)
			}
			f.grant = second
			f.start(t)
			if _, err := f.db.RequestStop(ctx, second.TaskID, 1, "owner_stop_requested"); err != nil {
				t.Fatal(err)
			}
			if err := f.db.MarkStoppingUnknown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueueResume(ctx, second.TaskID, 1); !errors.Is(err, ErrConflict) {
				t.Fatalf("UNKNOWN became resumable: %v", err)
			}
			if charged := ledgerGroupCharged(t, f.db, "task"); charged != 1000 {
				t.Fatalf("UNKNOWN refunded reservation: charged=%d want 1000", charged)
			}
			status, used, grant := ledgerBudgetRow(t, f.db, second.SegmentID)
			if status != "reserved" || used != 0 || grant != 600 {
				t.Fatalf("UNKNOWN ledger=%s/%d/%d want reserved/0/600", status, used, grant)
			}
			// Follow the same legitimate late-exited path as the existing matrix;
			// this is no new recovery or UNKNOWN state-transition behavior.
			f.send(t, contract.EventExited, measured)
			if err := f.db.QueueResume(ctx, second.TaskID, 1); err != nil {
				t.Fatal(err)
			}
			third, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if measured == 0 {
				if !errors.Is(err, CodeError("budget_exhausted")) {
					t.Fatalf("missing UNKNOWN measurement refunded budget: err=%v", err)
				}
			} else if err != nil || third.GrantedActiveMS != 350 || third.AttemptID != second.AttemptID {
				t.Fatalf("settled UNKNOWN remainder=%d want 350 err=%v", third.GrantedActiveMS, err)
			}
		})
	}
}

func TestAttemptBudgetConcurrentClaimsReserveRemainderOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer f.db.Close()
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 400)
	if err := f.db.QueueResume(ctx, f.grant.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	type result struct {
		command contract.LaunchCommand
		err     error
	}
	ready := make(chan struct{})
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-ready
			command, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			results <- result{command, err}
		}()
	}
	close(ready)
	succeeded := 0
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err == nil {
				succeeded++
				if r.command.GrantedActiveMS != 600 || r.command.AttemptID != f.grant.AttemptID {
					t.Fatalf("concurrent remainder=%+v want same attempt/600", r.command)
				}
			} else if !errors.Is(r.err, ErrNoReady) {
				t.Fatalf("concurrent loser error=%v want no_ready_task", r.err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent remainder claims failed to join")
		}
	}
	var segments, reservations int
	if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime)`).Scan(&segments, &reservations); err != nil || succeeded != 1 || segments != 2 || reservations != 2 || ledgerGroupCharged(t, f.db, "task") != 1000 {
		t.Fatalf("concurrent claim ledger: winners=%d segments=%d reservations=%d err=%v", succeeded, segments, reservations, err)
	}
}

func TestAttemptBudgetRetryStartsNewAttemptWithoutResettingGroup(t *testing.T) {
	ctx := context.Background()
	f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer f.db.Close()
	first := f.grant
	f.start(t)
	attemptBudgetFailure(t, &f, 400)
	page, err := f.db.CollectPending(ctx, first.TaskID, "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("retry failure page=%+v err=%v", page, err)
	}
	e := page.Events[0]
	spec := RetrySpec{TaskID: first.TaskID, WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: first.SegmentID, NextAttemptNo: 2, CommandID: "attempt-budget-retry"}
	receipt, err := f.db.QueueRetry(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || second.GrantedActiveMS != 1000 || second.AttemptID == first.AttemptID {
		t.Fatalf("new retry attempt inherited old charge: command=%+v err=%v", second, err)
	}
	replayed, err := f.db.QueueRetry(ctx, spec)
	if err != nil || replayed != receipt || ledgerGroupCharged(t, f.db, "task") != 1400 {
		t.Fatalf("retry replay reset/recharged group: receipt=%+v err=%v", replayed, err)
	}
	f.grant = second
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 250)
	if err := f.db.QueueResume(ctx, second.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || resumed.AttemptID != second.AttemptID || resumed.GrantedActiveMS != 750 {
		t.Fatalf("latest retry attempt inherited earlier attempt charge: grant=%d want 750 command=%+v err=%v", resumed.GrantedActiveMS, resumed, err)
	}
	if charged := ledgerGroupCharged(t, f.db, "task"); charged != 1400 {
		t.Fatalf("latest-attempt resume lost group history: charged=%d want 1400", charged)
	}
}

func TestAttemptBudgetReworkStartsNewAttemptKeepsGroup(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || second.GrantedActiveMS != 3_600_000 || second.AttemptID == f.edit.AttemptID || second.WorkRevision != 2 {
		t.Fatalf("new rework inherited old attempt charge: command=%+v err=%v", second, err)
	}
	if charged := ledgerGroupCharged(t, f.db, "edit"); charged != 7_200_000 {
		t.Fatalf("new rework reset group charge: charged=%d want 7200000", charged)
	}
}

func TestAttemptBudgetGlobalFIFOSkipsExhaustedForeignAttempt(t *testing.T) {
	ctx := context.Background()
	f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer f.db.Close()
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 0)
	if err := f.db.QueueResume(ctx, f.grant.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	host := attemptBudgetOtherHost(t, f.db)
	other, err := f.db.ClaimReady(ctx, host, 1, 2)
	if err != nil || other.TaskID != "other-task" || other.GrantedActiveMS != 1000 {
		t.Fatalf("exhausted foreign attempt blocked eligible FIFO successor: command=%+v err=%v", other, err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, CodeError("budget_exhausted")) {
		t.Fatalf("own exhausted FIFO candidate error=%v want budget_exhausted", err)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM segments WHERE attempt_id=?`, f.grant.AttemptID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("skipped foreign candidate created segment: count=%d err=%v", count, err)
	}
	status, used, grant := ledgerBudgetRow(t, f.db, f.grant.SegmentID)
	if status != "settled" || used != 1000 || grant != 1000 {
		t.Fatalf("foreign exhausted ledger changed: %s/%d/%d", status, used, grant)
	}
}

func attemptBudgetOtherHost(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "other-run", ControllerThread: "other-owner", PlanRevision: 1, OriginContextID: "other-origin", OriginPID: 3, OriginBirth: "other-birth"},
		Host:  HostLaunchSpec{OriginContextID: "other-origin", HostGeneration: "other-generation", Executable: "/private/bin/host"},
		Tasks: []TaskSpec{{ID: "other-task", RunID: "other-run", MaxAttempts: 3, MaxActiveMS: 1000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "other-origin", OriginPID: 3, OriginBirth: "other-birth", HostGeneration: "other-generation", PID: 4, Birth: "other-host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func TestAttemptBudgetGlobalFIFOKeepsEligibleForeignAttempt(t *testing.T) {
	ctx := context.Background()
	f := newBudgetBatchFixture(t, filepath.Join(t.TempDir(), "state.db"))
	defer f.db.Close()
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 400)
	if err := f.db.QueueResume(ctx, f.grant.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	host := attemptBudgetOtherHost(t, f.db)
	if _, err := f.db.ClaimReady(ctx, host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatalf("eligible foreign remainder was skipped: error=%v want no_ready_task", err)
	}
	first, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil || first.GrantedActiveMS != 600 || first.AttemptID != f.grant.AttemptID {
		t.Fatalf("earlier eligible remainder=%+v want 600 err=%v", first, err)
	}
	other, err := f.db.ClaimReady(ctx, host, 1, 2)
	if err != nil || other.TaskID != "other-task" {
		t.Fatalf("FIFO successor lost after earlier remainder claimed: %+v %v", other, err)
	}
}

func TestAttemptBudgetGroupBalanceStillCapsNewAttempt(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 3_600_000}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || first.GrantedActiveMS != 3_600_000 {
		t.Fatalf("initial large attempt=%+v %v", first, err)
	}
	f := budgetBatchFixture{db: db, host: host, grant: first}
	for i, measured := range []int64{0, 3_599_700} {
		f.start(t)
		failed := attemptBudgetFailure(t, &f, measured)
		page, err := db.CollectPending(ctx, "task", "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		var event contract.Event
		for _, candidate := range page.Events {
			if candidate.EventID == failed.EventID {
				event = candidate
			}
		}
		if event.EventID == "" {
			t.Fatal("actual failed event not collectable")
		}
		if _, err := db.QueueRetry(ctx, RetrySpec{TaskID: "task", WorkRevision: 1, EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, SegmentID: f.grant.SegmentID, NextAttemptNo: i + 2, CommandID: "budget-retry-" + event.EventID}); err != nil {
			t.Fatal(err)
		}
		next, err := db.ClaimReady(ctx, host, 1, 2)
		want := int64(3_600_000)
		if i == 1 {
			want = 300
		}
		if err != nil || next.GrantedActiveMS != want || next.AttemptID == f.grant.AttemptID {
			t.Fatalf("new attempt group cap=%d want %d err=%v", next.GrantedActiveMS, want, err)
		}
		f.grant = next
	}
	if charged := ledgerGroupCharged(t, db, "task"); charged != 7_200_000 {
		t.Fatalf("new attempt reset group remaining: charged=%d want 7200000", charged)
	}
}

// Retry requests validate the actual durable failure digest. The simpler
// settlement fixture's hash-N strings deliberately do not establish that input.
func attemptBudgetFailure(t *testing.T, f *budgetBatchFixture, measured int64) contract.Event {
	t.Helper()
	var failed contract.Event
	for _, kind := range []string{contract.EventFailed, contract.EventExited} {
		f.sequence++
		id := fmt.Sprintf("attempt-budget-event-%d", f.sequence)
		event := contract.Event{Version: 1, ProducerID: f.host, EventID: id, RunID: f.grant.RunID, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: f.grant.WorkRevision, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.sequence, Kind: kind, PayloadHash: runtimeHash(id)}
		if kind == contract.EventFailed {
			failed = event
			f.outcome = kind
		} else {
			event.ActiveMS = measured
		}
		if _, err := f.db.CommitHostEvent(context.Background(), event); err != nil {
			t.Fatalf("valid retry event %s: %v", kind, err)
		}
	}
	return failed
}
