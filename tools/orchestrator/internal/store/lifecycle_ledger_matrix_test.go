package store

// Lifecycle/budget ledger matrix with independent hard-coded expectations.
// Every expectation is a contract literal written in this file - 3_600_000ms is
// the one-hour segment grant and 7_200_000ms the two-hour group budget - chosen
// here rather than read from the production constants that happen to hold the
// same numbers, so changing a production default cannot silently move a goalpost.
//
// Coverage map (required behavior -> test):
//
//  1. Terminal settlement matrix:
//     - deadline/stop then exited with a positive measurement -> used=measurement:
//     mapped -> TestExitActiveTimeSettlementIsConservativeAndIdempotent/"measured"
//     - exited missing/0 ActiveMS -> used=granted (conservative full charge):
//     mapped -> same test/"omitted"
//     - negative ActiveMS -> used=granted: mapped -> same test/"unknown_negative"
//     - measurement above grant -> used=min(measured,granted)=granted:
//     mapped -> same test/"above_grant"
//     - normal exit (result outcome) with positive measurement: NEW ->
//     TestLedgerMatrixResultExitTerminalSettlement (row result_exit_measured)
//     - normal exit (result outcome) with unmeasured 0 ActiveMS -> used=granted:
//     NEW -> same test (row result_exit_unmeasured_conservative)
//  2. Duplicate exited: identical canonical replay returns an idempotent ACK
//     with the ledger unchanged, and a fresh-event-id late exited is durable
//     history that neither re-settles nor refunds. Mapped ->
//     TestExitActiveTimeSettlementIsConservativeAndIdempotent and
//     TestMeasuredSettlementSurvivesRestartReplayAndLateSettle.
//  3. Late events must not affect the current execution:
//     - old work_revision events stay historical: mapped ->
//     TestReworkFencesOldHostEventsButKeepsDurableReplay and
//     TestReworkLateOldHostEventIsHistoricalAndACKable
//     - old-attempt late result after retry: mapped ->
//     TestCandidateReviewRetryUsesFreshBoundAttemptWithoutResettingBudget
//     - old-attempt late EXITED with a fresh id must not touch either ledger
//     row, and cross-attempt identity must be refused: NEW ->
//     TestLedgerMatrixLateOldAttemptExitIsHistoryAndCannotRefund
//  4. Unknown terminal leaves the reservation unsettled and unreleased while
//     charging the group at granted: NEW ->
//     TestLedgerMatrixUnknownTerminalKeepsReservationCharged. Unknown is not a
//     permanent state either: a later genuine exited reconciles it to a measured
//     settlement, which is the documented recovery path - mapped to
//     TestExitActiveTimeSettlementIsConservativeAndIdempotent ("measured" row) and
//     TestRecoveryMatrixUnknownIsNotResumable. What no path may do is settle a
//     segment that the owner declared stopped without a confirmed exit.
//  5. Technical retry accumulates group usage across settled segments:
//     mapped -> TestCandidateReviewRetryThirdAttemptKeepsOriginalScopeAndBudget
//     (1234+2345=3579). Rework (new work_revision) still settles per segment:
//     NEW -> TestLedgerMatrixReworkSettlesEachSegmentPerRevision
//  6. close/reopen keeps ledger reads identical: mapped ->
//     TestMeasuredSettlementSurvivesRestartReplayAndLateSettle; re-exercised on
//     the rework ledger in TestLedgerMatrixReworkSettlesEachSegmentPerRevision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// ledgerBudgetRow reads the segment's own reservation row: exact contract
// expectations are passed in, never derived from helper code below.
func ledgerBudgetRow(t *testing.T, db *DB, segmentID string) (string, int64, int64) {
	t.Helper()
	var status string
	var used, granted int64
	if err := db.sql.QueryRow(`SELECT status,used_ms,granted_ms FROM budget_runtime WHERE segment_id=?`, segmentID).Scan(&status, &used, &granted); err != nil {
		t.Fatal(err)
	}
	return status, used, granted
}

// ledgerGroupCharged is the durable group ledger: settled rows contribute the
// settled amount, unsettled rows continue to hold their whole grant. Expected
// values in the tests below are hard-coded contract totals.
func ledgerGroupCharged(t *testing.T, db *DB, group string) int64 {
	t.Helper()
	var total int64
	if err := db.sql.QueryRow(`SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, group).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

type ledgerMatrixRow struct {
	name     string
	outcome  string
	reported int64
	// Independent expectations for MaxActiveMS=1000 (grant is never the binding
	// constraint in this group), hard-coded per the settlement contract.
	wantUsed       int64
	wantTaskStatus string
}

// Behavior 1 gap rows: a normal (result-outcome) exit still follows the same
// settlement contract: positive measurement settles exactly, an unmeasured
// exit conservatively consumes the whole grant.
func TestLedgerMatrixResultExitTerminalSettlement(t *testing.T) {
	for _, row := range []ledgerMatrixRow{
		{"result_exit_measured", contract.EventResult, 450, 450, "result_ready"},
		{"result_exit_unmeasured_conservative", contract.EventResult, 0, 1000, "result_ready"},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: []byte(`{}`)}}})
			if err != nil {
				t.Fatal(err)
			}
			host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner", HostGeneration: "gen", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := db.ClaimReady(ctx, host, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if grant.GrantedActiveMS != 1000 {
				t.Fatalf("grant=%d want the contract default of the requested 1000ms cap", grant.GrantedActiveMS)
			}
			sequence := int64(0)
			send := func(kind string, activeMS int64) {
				t.Helper()
				sequence++
				e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprintf("e-%d", sequence), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("hash-%d", sequence), ActiveMS: activeMS}
				if kind == contract.EventExited {
					// Both rows simulate a clean OS exit; the row distinction
					// is the reported measurement, not the exit status.
					zero := 0
					e.ExitCode = &zero
				}
				wire, _ := json.Marshal(e)
				var decoded contract.Event
				if err = json.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				if _, err = db.CommitHostEvent(ctx, decoded); err != nil {
					t.Fatalf("%s: %v", kind, err)
				}
			}
			send(contract.EventPrepared, 0)
			send(contract.EventSpawned, 0)
			send(contract.EventRunning, 0)
			send(row.outcome, 0)
			send(contract.EventExited, row.reported)
			if status, used, granted := ledgerBudgetRow(t, db, grant.SegmentID); status != "settled" || used != row.wantUsed || granted != 1000 {
				t.Fatalf("budget status=%s used=%d granted=%d want settled/%d/1000", status, used, granted, row.wantUsed)
			}
			var segmentStatus, taskStatus string
			if err = db.sql.QueryRow(`SELECT status FROM segment_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&segmentStatus); err != nil || segmentStatus != "exited" {
				t.Fatalf("segment status=%s err=%v", segmentStatus, err)
			}
			if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&taskStatus); err != nil || taskStatus != row.wantTaskStatus {
				t.Fatalf("task status=%s err=%v want %s", taskStatus, err, row.wantTaskStatus)
			}
			if got := ledgerGroupCharged(t, db, "task"); got != row.wantUsed {
				t.Fatalf("group charged=%d want %d", got, row.wantUsed)
			}
		})
	}
}

// Behavior 3 gap: after a technical retry dispatched a new attempt, a fresh
// late exited for the old attempt is durable history only. It must not
// re-settle the old segment, refund or release anything, and cross-attempt
// identity for the live segment must be refused without ledger change.
func TestLedgerMatrixLateOldAttemptExitIsHistoryAndCannotRefund(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventFailed)
	ctx := context.Background()
	old := f.grant
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Independently hard-coded expectations: the fixture settled the old
	// segment with ActiveMS=1234 against the 1h default grant, and the fresh
	// attempt only reserves its own 1h grant.
	if status, used, granted := ledgerBudgetRow(t, f.db, old.SegmentID); status != "settled" || used != 1234 || granted != 3_600_000 {
		t.Fatalf("old budget status=%s used=%d granted=%d", status, used, granted)
	}
	if status, used, granted := ledgerBudgetRow(t, f.db, fresh.SegmentID); status != "reserved" || used != 0 || granted != 3_600_000 {
		t.Fatalf("fresh budget status=%s used=%d granted=%d", status, used, granted)
	}
	wantCharged := int64(1234) + 3_600_000
	if got := ledgerGroupCharged(t, f.db, "review"); got != wantCharged {
		t.Fatalf("group charged=%d want %d", got, wantCharged)
	}
	late := contract.Event{Version: 1, ProducerID: f.host, EventID: "late-old-exit", RunID: "run", TaskID: "review", AttemptID: old.AttemptID, SegmentID: old.SegmentID, CommandID: old.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: 7, Kind: contract.EventExited, PayloadHash: "late-old-hash", ActiveMS: 1}
	ack, err := f.db.CommitHostEvent(ctx, late)
	if err != nil || ack.Status != "durable" {
		t.Fatalf("late old exit: ack=%+v err=%v", ack, err)
	}
	var delivery string
	if err = f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='late-old-exit'`).Scan(&delivery); err != nil || delivery != "internal" {
		t.Fatalf("late old exit delivery=%s err=%v", delivery, err)
	}
	if status, used, granted := ledgerBudgetRow(t, f.db, old.SegmentID); status != "settled" || used != 1234 || granted != 3_600_000 {
		t.Fatalf("late history changed old row: status=%s used=%d granted=%d", status, used, granted)
	}
	if status, used, granted := ledgerBudgetRow(t, f.db, fresh.SegmentID); status != "reserved" || used != 0 || granted != 3_600_000 {
		t.Fatalf("late history changed fresh row: status=%s used=%d granted=%d", status, used, granted)
	}
	if got := ledgerGroupCharged(t, f.db, "review"); got != wantCharged {
		t.Fatalf("late history changed group ledger: %d want %d", got, wantCharged)
	}
	var taskStatus string
	if err = f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='review'`).Scan(&taskStatus); err != nil || taskStatus != "running" {
		t.Fatalf("task status=%s err=%v", taskStatus, err)
	}
	// An exited event carrying the live segment ids but the retired attempt id
	// is an identity forgery, not history: it must be refused outright.
	forged := contract.Event{Version: 1, ProducerID: f.host, EventID: "cross-attempt-exit", RunID: "run", TaskID: "review", AttemptID: old.AttemptID, SegmentID: fresh.SegmentID, CommandID: fresh.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: 8, Kind: contract.EventExited, PayloadHash: "cross-attempt-hash", ActiveMS: 1}
	if _, err = f.db.CommitHostEvent(ctx, forged); err == nil {
		t.Fatal("cross-attempt exited event accepted")
	}
	if got := ledgerGroupCharged(t, f.db, "review"); got != wantCharged {
		t.Fatalf("refused forgery changed group ledger: %d want %d", got, wantCharged)
	}
	var persisted int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE event_id='cross-attempt-exit'`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("refused forgery persisted rows=%d err=%v", persisted, err)
	}
}

// Behavior 4: unknown is a terminal observation, not a settlement. The
// reservation stays unsettled and unreleased while the whole grant keeps
// charging the shared budget group, so a third dispatch in the group is refused
// as exhausted. Reconciliation by a later genuine exited is a separate, already
// pinned path; nothing in this test may settle through the unknown row itself.
func TestLedgerMatrixUnknownTerminalKeepsReservationCharged(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Three segments at the 1h default cap share one 2h group budget; the
	// second unknown leaves zero headroom for a third dispatch.
	if err = db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":3}`)); err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{
		{ID: "t1", RunID: "run", MaxAttempts: 3, MaxActiveMS: 3_600_000, BudgetGroupID: "shared", AdapterPayload: []byte(`{}`)},
		{ID: "t2", RunID: "run", MaxAttempts: 3, MaxActiveMS: 3_600_000, BudgetGroupID: "shared", AdapterPayload: []byte(`{}`)},
		{ID: "t3", RunID: "run", MaxAttempts: 3, MaxActiveMS: 3_600_000, BudgetGroupID: "shared", AdapterPayload: []byte(`{}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner", HostGeneration: "gen", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	send := func(grant contract.LaunchCommand, kind string) {
		t.Helper()
		sequence++
		e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprintf("u-%d", sequence), RunID: "run", TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("uhash-%d", sequence)}
		wire, _ := json.Marshal(e)
		var decoded contract.Event
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = db.CommitHostEvent(ctx, decoded); err != nil {
			t.Fatalf("%s/%s: %v", grant.TaskID, kind, err)
		}
	}
	for _, task := range []string{"t1", "t2"} {
		grant, err := db.ClaimReady(ctx, host, 1, 3)
		if err != nil {
			t.Fatal(err)
		}
		if grant.TaskID != task || grant.GrantedActiveMS != 3_600_000 {
			t.Fatalf("grant=%s/%d want %s/%d", grant.TaskID, grant.GrantedActiveMS, task, 3_600_000)
		}
		for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventUnknown} {
			send(grant, kind)
		}
		if status, used, granted := ledgerBudgetRow(t, db, grant.SegmentID); status != "reserved" || used != 0 || granted != 3_600_000 {
			t.Fatalf("%s unknown-terminal budget row: status=%s used=%d granted=%d", task, status, used, granted)
		}
	}
	// The shared group is now fully consumed by two unsettled reservations.
	if got := ledgerGroupCharged(t, db, "shared"); got != 7_200_000 {
		t.Fatalf("group charged=%d want %d", got, 7_200_000)
	}
	if _, err = db.ClaimReady(ctx, host, 1, 3); !errors.Is(err, CodeError("budget_exhausted")) {
		t.Fatalf("third dispatch err=%v want budget_exhausted", err)
	}
	// The refused claim must not release or settle either unknown reservation.
	rows, err := db.sql.Query(`SELECT segment_id, status, used_ms FROM budget_runtime WHERE budget_group_id='shared' ORDER BY segment_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		var segment, status string
		var used int64
		if err = rows.Scan(&segment, &status, &used); err != nil {
			t.Fatal(err)
		}
		if status != "reserved" || used != 0 {
			t.Fatalf("reservation %s leaked settlement: status=%s used=%d", segment, status, used)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("reservation count=%d want exactly the two unknown segments", count)
	}
}

// Behavior 5 gap: after a rework (new work_revision), every segment still
// settles on its own reservation row; the old revision's conservative
// settlement is untouched and the group ledger is the hard-coded sum. The
// read is re-verified after a close/reopen (behavior 6) on this ledger.
func TestLedgerMatrixReworkSettlesEachSegmentPerRevision(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	// Same file the fixture opened; t.TempDir() hands out a new directory per
	// call, so reopen the recorded path instead of a fresh join.
	path := f.db.path
	// Fixture baseline (independent contract expectation): revision-1 exits
	// reported no ActiveMS, so the old edit segment consumed its whole 1h
	// grant conservatively.
	if status, used, granted := ledgerBudgetRow(t, f.db, f.edit.SegmentID); status != "settled" || used != 3_600_000 || granted != 3_600_000 {
		t.Fatalf("rev1 edit row: status=%s used=%d granted=%d", status, used, granted)
	}
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if next.TaskID != "edit" || next.WorkRevision != 2 || next.SegmentID == f.edit.SegmentID {
		t.Fatalf("grant=%+v", next)
	}
	if next.GrantedActiveMS != 3_600_000 {
		// Honest limit of this row: with a 2h group and 1h already charged, the
		// remaining headroom equals the segment cap, so this value alone cannot
		// tell the two clamps apart. TestLedgerMatrixGroupHeadroomClampsTheGrant
		// is the case where they differ.
		t.Fatalf("rev2 grant=%d want 3600000", next.GrantedActiveMS)
	}
	if status, used, granted := ledgerBudgetRow(t, f.db, next.SegmentID); status != "reserved" || used != 0 || granted != 3_600_000 {
		t.Fatalf("rev2 reservation: status=%s used=%d granted=%d", status, used, granted)
	}
	if got := ledgerGroupCharged(t, f.db, "edit"); got != 7_200_000 {
		t.Fatalf("group charged before rev2 settle=%d want %d", got, 7_200_000)
	}
	sequence := int64(6)
	send := func(kind string, activeMS int64) {
		t.Helper()
		sequence++
		e := contract.Event{Version: 1, ProducerID: f.host, EventID: fmt.Sprintf("rev2-%d", sequence), RunID: "run", TaskID: "edit", AttemptID: next.AttemptID, SegmentID: next.SegmentID, CommandID: next.CommandID, WorkRevision: 2, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("rev2-hash-%d", sequence), ActiveMS: activeMS}
		wire, _ := json.Marshal(e)
		var decoded contract.Event
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.CommitHostEvent(ctx, decoded); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventResult} {
		send(kind, 0)
	}
	send(contract.EventExited, 777)
	// Segment-level settlement, revision-independent hard-coded values.
	if status, used, granted := ledgerBudgetRow(t, f.db, next.SegmentID); status != "settled" || used != 777 || granted != 3_600_000 {
		t.Fatalf("rev2 settled row: status=%s used=%d granted=%d", status, used, granted)
	}
	if status, used, granted := ledgerBudgetRow(t, f.db, f.edit.SegmentID); status != "settled" || used != 3_600_000 || granted != 3_600_000 {
		t.Fatalf("rev2 settle touched rev1 row: status=%s used=%d granted=%d", status, used, granted)
	}
	if got := ledgerGroupCharged(t, f.db, "edit"); got != 3_600_000+777 {
		t.Fatalf("group charged=%d want %d", got, 3_600_000+777)
	}
	// Behavior 6 on this ledger: restart reads identical values.
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if status, used, granted := ledgerBudgetRow(t, db, next.SegmentID); status != "settled" || used != 777 || granted != 3_600_000 {
		t.Fatalf("after reopen rev2 row: status=%s used=%d granted=%d", status, used, granted)
	}
	if status, used, granted := ledgerBudgetRow(t, db, f.edit.SegmentID); status != "settled" || used != 3_600_000 || granted != 3_600_000 {
		t.Fatalf("after reopen rev1 row: status=%s used=%d granted=%d", status, used, granted)
	}
	if got := ledgerGroupCharged(t, db, "edit"); got != 3_600_000+777 {
		t.Fatalf("after reopen group charged=%d want %d", got, 3_600_000+777)
	}
}

// Behavior 1 gap: the group budget itself bounds a grant, not only the segment
// cap. Two 40-minute settlements against the fixed 2-hour group leave 40 minutes
// of headroom, which is below the 1-hour segment cap - the only configuration
// where the two clamps are distinguishable, so a grant of 3_600_000 here would
// prove the group clamp is skipped.
func TestLedgerMatrixGroupHeadroomClampsTheGrant(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":3}`)); err != nil {
		t.Fatal(err)
	}
	task := func(id string) TaskSpec {
		return TaskSpec{ID: id, RunID: "run", MaxAttempts: 3, MaxActiveMS: 3_600_000, BudgetGroupID: "clamped", AdapterPayload: []byte(`{}`)}
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{task("c1"), task("c2"), task("c3")}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner", HostGeneration: "gen", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	send := func(grant contract.LaunchCommand, kind string, activeMS int64) {
		t.Helper()
		sequence++
		e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprintf("h-%d", sequence), RunID: "run", TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprintf("hhash-%d", sequence), ActiveMS: activeMS}
		wire, _ := json.Marshal(e)
		var decoded contract.Event
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = db.CommitHostEvent(ctx, decoded); err != nil {
			t.Fatalf("%s/%s: %v", grant.TaskID, kind, err)
		}
	}
	wantGrants := []int64{3_600_000, 3_600_000, 2_400_000}
	// The last segment reports more active time than it was granted, so settlement
	// must cut the claimed 3_600_000 ms (60 minutes) back to the 2_400_000 ms
	// (40 minutes) actually granted to it.
	wantReported := []int64{2_400_000, 2_400_000, 3_600_000}
	for index, want := range wantGrants {
		grant, err := db.ClaimReady(ctx, host, 1, 3)
		if err != nil {
			t.Fatalf("claim %d: %v", index, err)
		}
		if grant.GrantedActiveMS != want {
			t.Fatalf("claim %d (%s) grant=%d want %d", index, grant.TaskID, grant.GrantedActiveMS, want)
		}
		for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
			send(grant, kind, 0)
		}
		send(grant, contract.EventExited, wantReported[index])
		if status, used, granted := ledgerBudgetRow(t, db, grant.SegmentID); status != "settled" || used != 2_400_000 || granted != want {
			t.Fatalf("%s row: status=%s used=%d granted=%d want settled/2400000/%d", grant.TaskID, status, used, granted, want)
		}
		if got := ledgerGroupCharged(t, db, "clamped"); got != int64(index+1)*2_400_000 {
			t.Fatalf("group charged=%d want %d after %d settlements", got, int64(index+1)*2_400_000, index+1)
		}
	}
	// The group is now exactly at its 2h ceiling; exhaustion itself is pinned by
	// TestLedgerMatrixUnknownTerminalKeepsReservationCharged.
}
