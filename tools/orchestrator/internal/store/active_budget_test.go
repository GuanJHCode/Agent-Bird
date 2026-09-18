package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestExitActiveTimeSettlementIsConservativeAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name           string
		reported, want int64
	}{{"omitted", 0, 1000}, {"unknown_negative", -1, 1000}, {"measured", 123, 123}, {"above_grant", 1500, 1000}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000}}})
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
			sequence := int64(0)
			send := func(kind string) contract.Event {
				t.Helper()
				sequence++
				e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("e-", sequence), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprint("hash-", sequence)}
				if kind == contract.EventExited {
					e.ActiveMS = tc.reported
				}
				wire, _ := json.Marshal(e)
				var decoded contract.Event
				if err = json.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				if _, err = db.CommitHostEvent(ctx, decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			send(contract.EventPrepared)
			send(contract.EventSpawned)
			send(contract.EventRunning)
			send(contract.EventUnknown)
			assertBudget := func(status string, wantUsed, wantCharged int64) {
				t.Helper()
				var gotStatus string
				var used, charged int64
				err = db.sql.QueryRow(`SELECT status,used_ms,CASE WHEN status='settled' THEN used_ms ELSE granted_ms END FROM budget_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&gotStatus, &used, &charged)
				if err != nil || gotStatus != status || used != wantUsed || charged != wantCharged {
					t.Fatalf("budget status=%s used=%d charged=%d err=%v", gotStatus, used, charged, err)
				}
			}
			assertBudget("reserved", 0, 1000)
			send(contract.EventStopped)
			exit := send(contract.EventExited)
			assertBudget("settled", tc.want, tc.want)
			if _, err = db.CommitHostEvent(ctx, exit); err != nil {
				t.Fatal(err)
			}
			assertBudget("settled", tc.want, tc.want)
			// A later exited event is history, not a way to refund the settled grant.
			exit.Sequence++
			exit.EventID = "later-exited"
			exit.ActiveMS = 1
			exit.PayloadHash = "later-hash"
			if _, err = db.CommitHostEvent(ctx, exit); err != nil {
				t.Fatal(err)
			}
			assertBudget("settled", tc.want, tc.want)
		})
	}
}

func TestReviewRetryCannotRefundUnmeasuredExit(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	// A prior settled segment leaves exactly one 1000ms segment available.
	// This sets the boundary without waiting through a real group allowance.
	if _, err := f.db.sql.Exec(`UPDATE budget_runtime SET used_ms=? WHERE segment_id=?`, defaultGroupActiveMS-1000, f.grant.SegmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	grant, err := f.db.ClaimReady(ctx, f.host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if grant.GrantedActiveMS != 1000 {
		t.Fatalf("remaining grant=%d", grant.GrantedActiveMS)
	}
	var seq int64
	if err = f.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, f.host).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
		seq++
		e := contract.Event{Version: 1, ProducerID: f.host, EventID: fmt.Sprint("retry-event-", seq), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1, Sequence: seq, Kind: kind, PayloadHash: runtimeHash(fmt.Sprint("retry-hash-", seq))}
		if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.db.CollectPending(ctx, "review", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var event contract.Event
	for _, e := range page.Events {
		if e.SegmentID == grant.SegmentID && e.Kind == contract.EventStopped {
			event = e
		}
	}
	if event.EventID == "" {
		t.Fatal("missing current stopped event")
	}
	retry := RetrySpec{TaskID: "review", WorkRevision: 1, EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, SegmentID: grant.SegmentID, NextAttemptNo: 3, CommandID: "retry-exhausted"}
	if _, err = f.db.QueueRetry(ctx, retry); !errors.Is(err, CodeError("budget_exhausted")) {
		t.Fatalf("retry err=%v; unknown usage must not restore budget", err)
	}
	if _, err = f.db.ClaimReady(ctx, f.host, 1, 1); err == nil {
		t.Fatal("exhausted group dispatched another grant")
	}
	var used int64
	if err = f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='review'`).Scan(&used); err != nil || used != defaultGroupActiveMS {
		t.Fatalf("used=%d err=%v", used, err)
	}
	var decisions int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE task_id='review'`).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("decisions=%d err=%v", decisions, err)
	}
}
