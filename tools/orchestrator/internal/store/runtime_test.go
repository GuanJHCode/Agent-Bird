package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestRuntimePersistsDAGSlotsEventsAndExplicitResume(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:  RunSpec{ID: "run-1", ControllerThread: "thread-1", PlanRevision: 1, OriginContextID: "origin-1", OriginPID: 7, OriginBirth: "origin-birth"},
		Host: HostLaunchSpec{OriginContextID: "origin-1", HostGeneration: "generation-1", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "task-a", RunID: "run-1", MaxAttempts: 3},
			{ID: "task-b", RunID: "run-1", MaxAttempts: 3},
			{ID: "task-c", RunID: "run-1", Dependencies: []string{"task-a", "task-b"}, MaxAttempts: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: "origin-1", HostGeneration: "generation-1",
		PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskID != "task-a" || second.TaskID != "task-b" {
		t.Fatalf("FIFO claims=%s,%s", first.TaskID, second.TaskID)
	}
	if first.GrantedActiveMS != 3_600_000 || first.ReservationID == "" || first.BudgetGroupID != "task-a" {
		t.Fatalf("grant=%#v", first)
	}
	if _, err = db.ClaimReady(ctx, hostID, 1, 2); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("third claim err=%v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	// A coordinator restart never turns persisted running work back into ready work.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if status, statusErr := db.HostLaunchStatus(ctx, receipt.LaunchID, receipt.LaunchToken); statusErr != nil || status != "offline" {
		t.Fatalf("restart host status=%q err=%v", status, statusErr)
	}
	hostID, err = db.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: "origin-1", HostGeneration: "generation-1",
		PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator",
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	pendingGrants, err := db.PendingLaunches(ctx, hostID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingGrants) != 2 || pendingGrants[0].ReservationID != first.ReservationID || pendingGrants[1].ReservationID != second.ReservationID || pendingGrants[0].GrantedActiveMS != first.GrantedActiveMS {
		t.Fatalf("pending grants=%#v", pendingGrants)
	}
	if _, err = db.ClaimReady(ctx, hostID, 2, 2); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("restart claim err=%v", err)
	}

	seq := int64(0)
	commit := func(command contract.LaunchCommand, kind string, activeMS int64) contract.DurableAck {
		t.Helper()
		seq++
		event := contract.Event{
			Version: 1, ProducerID: hostID, EventID: "event-" + command.SegmentID + "-" + kind,
			RunID: command.RunID, TaskID: command.TaskID, AttemptID: command.AttemptID,
			SegmentID: command.SegmentID, WorkRevision: 1, ExecutionEpoch: 2,
			CommandID: command.CommandID, Sequence: seq, Kind: kind,
			PayloadHash: "hash-" + command.TaskID + "-" + kind, ActiveMS: activeMS,
		}
		ack, commitErr := db.CommitHostEvent(ctx, event)
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		return ack
	}
	for _, command := range []contract.LaunchCommand{first, second} {
		commit(command, contract.EventResult, 0)
		ack := commit(command, contract.EventExited, 12_000)
		if ack.AckedThrough != seq || ack.Status != "durable" {
			t.Fatalf("ack=%#v", ack)
		}
	}
	third, err := db.ClaimReady(ctx, hostID, 2, 2)
	if err != nil || third.TaskID != "task-c" {
		t.Fatalf("dependency claim=%#v err=%v", third, err)
	}
	stop, err := db.RequestStop(ctx, "task-c", 1, "controller_exit")
	if err != nil || stop.HostID != hostID || stop.Command.SegmentID != third.SegmentID {
		t.Fatalf("stop=%#v err=%v", stop, err)
	}
	active, err := db.ActiveTasks(ctx)
	if err != nil || len(active) != 1 || active[0].TaskID != "task-c" || active[0].WorkRevision != 1 {
		t.Fatalf("active=%#v err=%v", active, err)
	}

	// Interrupted work stays silent across restart and receives a new segment
	// only after an explicit resume request.
	commit(third, contract.EventStopped, 0)
	commit(third, contract.EventExited, 2_000)
	if _, err = db.ClaimReady(ctx, hostID, 2, 2); !errors.Is(err, ErrNoReady) {
		t.Fatalf("silent claim err=%v", err)
	}
	hostID, err = db.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: "origin-1", HostGeneration: "generation-1",
		PID: 202, Birth: "replacement-birth", Executable: "/private/tmp/orchestrator",
	}, 2)
	if err != nil {
		t.Fatalf("replacement source host: %v", err)
	}
	if err = db.QueueResume(ctx, "task-c", 1); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.ClaimReady(ctx, hostID, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AttemptID != third.AttemptID || resumed.SegmentID == third.SegmentID || resumed.ReservationID == third.ReservationID {
		t.Fatalf("resume=%#v previous=%#v", resumed, third)
	}

	commit(resumed, contract.EventResult, 0)
	commit(resumed, contract.EventExited, 1_000)
	pending, err := db.CollectPending(ctx, "task-c")
	if err != nil || len(pending) != 4 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	ids := []string{pending[0].EventID, pending[1].EventID, pending[2].EventID, pending[3].EventID}
	if err = db.AckEvents(ctx, "task-c", ids); err != nil {
		t.Fatal(err)
	}
	if err = db.AckEvents(ctx, "task-c", ids); err != nil {
		t.Fatalf("repeat ack=%v", err)
	}
	pending, err = db.CollectPending(ctx, "task-c")
	if err != nil || len(pending) != 0 {
		t.Fatalf("remaining=%d err=%v", len(pending), err)
	}
	for _, taskID := range []string{"task-a", "task-b"} {
		events, collectErr := db.CollectPending(ctx, taskID)
		if collectErr != nil {
			t.Fatal(collectErr)
		}
		ids = ids[:0]
		for _, event := range events {
			ids = append(ids, event.EventID)
		}
		if err = db.AckEvents(ctx, taskID, ids); err != nil {
			t.Fatal(err)
		}
	}
	retirable, err := db.RetirableHosts(ctx)
	if err != nil || len(retirable) != 1 || retirable[0] != hostID {
		t.Fatalf("retirable=%#v err=%v", retirable, err)
	}
	if err = db.RetireHost(ctx, hostID); err != nil {
		t.Fatal(err)
	}
	if idle, idleErr := db.CompletelyIdle(ctx); idleErr != nil || !idle {
		t.Fatalf("idle=%v err=%v", idle, idleErr)
	}
}

func TestSubmitPlanRejectsCycleWithoutPartialWrite(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	_, err = db.SubmitPlan(ctx, PlanSpec{
		Run:  RunSpec{ID: "run-cycle", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "left", RunID: "run-cycle", Dependencies: []string{"right"}, MaxAttempts: 3},
			{ID: "right", RunID: "run-cycle", Dependencies: []string{"left"}, MaxAttempts: 3},
		},
	})
	if !errors.Is(err, ErrInvalidDAG) {
		t.Fatalf("err=%v", err)
	}
	if _, err = db.RunID(ctx, "left"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial task err=%v", err)
	}
}
