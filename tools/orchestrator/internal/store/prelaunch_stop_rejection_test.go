package store

import (
	"context"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestLaunchRejectedAfterPrelaunchOwnerStopCompletesReservationAndStop(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "launch_requested", true: "prepared"}[prepared], func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderAGY)
			ctx := context.Background()
			if prepared {
				f.send(t, f.event(contract.EventPrepared))
			}
			if err := f.db.ValidateTaskOwner(ctx, f.grant.TaskID, "owner", f.receipt.ControlToken); err != nil {
				t.Fatal(err)
			}
			dispatch, err := f.db.RequestStop(ctx, f.grant.TaskID, 1, "owner_stop")
			if err != nil || dispatch.HostID != f.host || dispatch.Command.SegmentID != f.grant.SegmentID || dispatch.Command.DeadlineUnixMS <= 0 {
				t.Fatalf("owner stop=%+v %v", dispatch, err)
			}
			var segment, outcome, stop, budget string
			var used int64
			readLedger := func() {
				t.Helper()
				if err := f.db.sql.QueryRow(`SELECT s.status,s.outcome,z.status,b.status,b.used_ms FROM segment_runtime s JOIN stop_runtime z ON z.segment_id=s.segment_id AND z.host_id=s.host_id JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.segment_id=? AND z.command_id=?`, f.grant.SegmentID, dispatch.Command.CommandID).Scan(&segment, &outcome, &stop, &budget, &used); err != nil {
					t.Fatal(err)
				}
			}
			readLedger()
			if segment != "stopping" || outcome != "stopped" || stop != "pending" || budget != "reserved" || used != 0 {
				t.Fatalf("unexpected legitimate stop state: %s/%s/%s/%s/%d", segment, outcome, stop, budget, used)
			}
			// No spawned/process/session/result/UNKNOWN event has ever existed.
			// The same authenticated grant positively attests Start was never entered.
			e := rejectionEvent(t, f)
			_, err = f.db.CommitHostEvent(ctx, e)
			readLedger()
			if err != nil {
				t.Fatalf("never-started rejection after owner stop denied; durable state=%s/%s/%s/%s/%d: %v", segment, outcome, stop, budget, used, err)
			}
			assertRejectedLedger(t, f, "settled", 0)
			if stop != "completed" {
				t.Fatalf("never-started stop remains %s", stop)
			}
			var reason string
			var deadline int64
			if err = f.db.sql.QueryRow(`SELECT reason,deadline_unix_ms FROM stop_runtime WHERE command_id=?`, dispatch.Command.CommandID).Scan(&reason, &deadline); err != nil || reason != dispatch.Command.Reason || deadline != dispatch.Command.DeadlineUnixMS {
				t.Fatalf("original stop audit changed: %s/%d %v", reason, deadline, err)
			}
			f.send(t, e)
			assertRejectedLedger(t, f, "settled", 0)
		})
	}
}

func TestPrelaunchStopRejectionRetainsContradictionsAndOtherPendingStops(t *testing.T) {
	for _, mode := range []string{"missing_stop", "foreign_host", "accepted_stop", "spawned", "unknown", "session"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderAGY)
			ctx := context.Background()
			if mode == "session" {
				f.start(t)
			} else {
				f.send(t, f.event(contract.EventPrepared))
				if mode == "spawned" || mode == "unknown" {
					f.send(t, f.event(mode))
				}
			}
			dispatch, err := f.db.RequestStop(ctx, f.grant.TaskID, 1, "owner_stop")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing_stop":
				_, err = f.db.sql.Exec(`DELETE FROM stop_runtime WHERE command_id=?`, dispatch.Command.CommandID)
			case "foreign_host":
				_, foreignHost := fifoBatchPlan(t, f.db, "foreign", 2, "fake")
				_, err = f.db.sql.Exec(`UPDATE stop_runtime SET host_id=? WHERE command_id=?`, foreignHost, dispatch.Command.CommandID)
			case "accepted_stop":
				_, err = f.db.sql.Exec(`UPDATE stop_runtime SET status='accepted' WHERE command_id=?`, dispatch.Command.CommandID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := hostRecoveryLedger(t, f.db)
			if _, err = f.db.CommitHostEvent(ctx, rejectionEvent(t, f)); err == nil {
				t.Fatalf("%s stop projection erased reservation", mode)
			}
			if after := hostRecoveryLedger(t, f.db); after != before {
				t.Fatal("unproved rejection changed ledger")
			}
			assertRejectedLedger(t, f, "reserved", 0)
		})
	}
	t.Run("other_segment", func(t *testing.T) {
		f := newProgressTerminalFixture(t)
		ctx := context.Background()
		first := f.grant
		f.send(t, f.event(contract.EventPrepared))
		if _, err := f.db.RequestStop(ctx, first.TaskID, 1, "owner_stop"); err != nil {
			t.Fatal(err)
		}
		second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		f.grant = second
		f.send(t, f.event(contract.EventPrepared))
		other, err := f.db.RequestStop(ctx, second.TaskID, 1, "owner_stop")
		if err != nil {
			t.Fatal(err)
		}
		f.grant = first
		e := rejectionEvent(t, f)
		f.send(t, e)
		f.send(t, e)
		assertRejectedLedger(t, f, "settled", 0)
		var status, budget string
		if err = f.db.sql.QueryRow(`SELECT z.status,b.status FROM stop_runtime z JOIN budget_runtime b ON b.segment_id=z.segment_id WHERE z.command_id=?`, other.Command.CommandID).Scan(&status, &budget); err != nil || status != "pending" || budget != "reserved" {
			t.Fatalf("another segment stop/reservation changed: %s/%s %v", status, budget, err)
		}
	})
}

func TestPrelaunchStopRejectionPreservesEarlierContinuationCharges(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderAGY)
	ctx := context.Background()
	f.start(t)
	q := f.question(t, 1)
	if _, err := f.db.AnswerQuestion(ctx, AnswerSpec{TaskID: f.grant.TaskID, WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "continue"}); err != nil {
		t.Fatal(err)
	}
	var err error
	f.grant, err = f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RequestStop(ctx, f.grant.TaskID, 1, "owner_stop"); err != nil {
		t.Fatal(err)
	}
	f.send(t, rejectionEvent(t, f))
	assertRejectedLedger(t, f, "settled", 0)
	var used int64
	var status string
	if err = f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime`).Scan(&used); err != nil || used != 200 {
		t.Fatalf("prior charge changed: %d %v", used, err)
	}
	if err = f.db.sql.QueryRow(`SELECT status FROM runtime_questions WHERE question_id=?`, q.QuestionID).Scan(&status); err != nil || status != "resolved" {
		t.Fatalf("consumed answer remained %s %v", status, err)
	}
}
