package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
			{ID: "task-a", RunID: "run-1", MaxAttempts: 3, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)},
			{ID: "task-b", RunID: "run-1", MaxAttempts: 3, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)},
			{ID: "task-c", RunID: "run-1", Dependencies: []string{"task-a", "task-b"}, MaxAttempts: 3, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: "origin-1", OriginPID: 7, OriginBirth: "origin-birth", HostGeneration: "generation-1",
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
	binding, err := db.HostBinding(ctx, receipt.LaunchID)
	if err != nil || binding.PID != 101 || binding.Active != 2 {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
	if err = db.AuthorizeHostRebind(ctx, receipt.LaunchID, binding.PID, binding.Birth); err != nil {
		t.Fatal(err)
	}
	hostID, err = db.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: "origin-1", OriginPID: 7, OriginBirth: "origin-birth", HostGeneration: "generation-1",
		PID: 202, Birth: "replacement-active-birth", Executable: "/private/tmp/orchestrator",
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.BeginHostReconcile(ctx, hostID); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishHostReconcile(ctx, hostID); err != nil {
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
		OriginContextID: "origin-1", OriginPID: 7, OriginBirth: "origin-birth", HostGeneration: "generation-1",
		PID: 303, Birth: "replacement-birth", Executable: "/private/tmp/orchestrator",
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
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	for _, event := range pending {
		decision := "stale"
		if event.SegmentID == resumed.SegmentID {
			decision = "handled"
		}
		ackEvent(t, db, "task-c", event, decision)
		ackEvent(t, db, "task-c", event, decision)
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
		for _, event := range events {
			ackEvent(t, db, taskID, event, "handled")
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

func TestFailedBeforeSpawnSettlesSlotWithoutBecomingUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:  RunSpec{ID: "failed-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "failed-task", RunID: "failed-run", MaxAttempts: 1},
			{ID: "blocked-task", RunID: "failed-run", Dependencies: []string{"failed-task"}, MaxAttempts: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	for sequence, kind := range []string{contract.EventFailed, contract.EventExited} {
		exit := -1
		event := contract.Event{Version: 1, ProducerID: hostID, EventID: fmt.Sprintf("failed-%d", sequence+1), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: int64(sequence + 1), Kind: kind, PayloadHash: fmt.Sprintf("hash-%d", sequence+1), ExitCode: &exit}
		if _, err = db.CommitHostEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.TaskSnapshot(ctx, "failed-task")
	if err != nil || snapshot.Status != "failed" || snapshot.SegmentStatus != "exited" {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	late := contract.Event{Version: 1, ProducerID: hostID, EventID: "failed-late-prepared", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 3, Kind: contract.EventPrepared, PayloadHash: "hash-late-prepared"}
	if _, err = db.CommitHostEvent(ctx, late); err != nil {
		t.Fatal(err)
	}
	snapshot, err = db.TaskSnapshot(ctx, "failed-task")
	if err != nil || snapshot.Status != "failed" || snapshot.SegmentStatus != "exited" {
		t.Fatalf("late event regressed terminal state: %#v err=%v", snapshot, err)
	}
	if active, activeErr := db.ActiveTasks(ctx); activeErr != nil || len(active) != 0 {
		t.Fatalf("active=%#v err=%v", active, activeErr)
	}
	blocked, err := db.TaskSnapshot(ctx, "blocked-task")
	if err != nil || blocked.Status != "blocked_dependency" {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
	events, err := db.CollectPending(ctx, "failed-task")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		ackEvent(t, db, "failed-task", event, "handled")
	}
	retirable, err := db.RetirableHosts(ctx)
	if err != nil || len(retirable) != 1 || retirable[0] != hostID {
		t.Fatalf("failed/blocked host not retirable: %#v err=%v", retirable, err)
	}
}

func ackEvent(t *testing.T, db *DB, taskID string, event contract.Event, decision string) {
	t.Helper()
	err := db.AckDecision(context.Background(), taskID, AckDecision{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: decision, CommandID: "ack-" + event.EventID})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCancelBlocksRequiredDescendantButKeepsIndependentTaskReady(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:  RunSpec{ID: "cancel-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "cancel-root", RunID: "cancel-run", MaxAttempts: 3},
			{ID: "cancel-child", RunID: "cancel-run", Dependencies: []string{"cancel-root"}, MaxAttempts: 3},
			{ID: "independent", RunID: "cancel-run", MaxAttempts: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.RequestStop(ctx, "cancel-root", 1, "controller_stop"); err != nil {
		t.Fatal(err)
	}
	child, err := db.TaskSnapshot(ctx, "cancel-child")
	if err != nil || child.Status != "blocked_dependency" {
		t.Fatalf("child=%#v err=%v", child, err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || grant.TaskID != "independent" {
		t.Fatalf("grant=%#v err=%v", grant, err)
	}
}

func TestSubmitPlanReservesAtMostFourSourceHosts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for index := 1; index <= 5; index++ {
		id := fmt.Sprintf("run-%d", index)
		_, err = db.SubmitPlan(ctx, PlanSpec{
			Run:   RunSpec{ID: id, ControllerThread: "thread", PlanRevision: 1, OriginContextID: id, OriginPID: index, OriginBirth: "birth"},
			Host:  HostLaunchSpec{OriginContextID: id, HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
			Tasks: []TaskSpec{{ID: "task-" + id, RunID: id, MaxAttempts: 3}},
		})
		if index < 5 && err != nil {
			t.Fatalf("submit %d: %v", index, err)
		}
		if index == 5 && !errors.Is(err, ErrAdmissionDeferred) {
			t.Fatalf("fifth submit err=%v", err)
		}
	}
}

func TestQuestionAnswerResumesSameAttemptOnce(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "question-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{{ID: "question-task", RunID: "question-run", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"antigravity-cli"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	question := contract.Event{Version: 1, ProducerID: hostID, EventID: hostID + ":1", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventQuestion, PayloadHash: strings.Repeat("a", 64), QuestionID: "question-1", QuestionRevision: 1, SessionKind: "conversation-id", SessionID: "conversation-1"}
	if _, err = db.CommitHostEvent(ctx, question); err != nil {
		t.Fatal(err)
	}
	changed := question
	changed.SessionID = "conversation-changed"
	if _, changedErr := db.CommitHostEvent(ctx, changed); !errors.Is(changedErr, ErrEventSequence) {
		t.Fatalf("same hash with changed session err=%v", changedErr)
	}
	exit := 0
	question.Sequence, question.EventID, question.Kind, question.PayloadHash, question.ExitCode = 2, hostID+":2", contract.EventExited, strings.Repeat("b", 64), &exit
	if _, err = db.CommitHostEvent(ctx, question); err != nil {
		t.Fatal(err)
	}
	if status, answerErr := db.AnswerQuestion(ctx, AnswerSpec{TaskID: grant.TaskID, WorkRevision: 1, QuestionID: "question-1", QuestionRevision: 1, Answer: "Use the narrow API."}); answerErr != nil || status != "resume_queued" {
		t.Fatalf("answer status=%q err=%v", status, answerErr)
	}
	if status, answerErr := db.AnswerQuestion(ctx, AnswerSpec{TaskID: grant.TaskID, WorkRevision: 1, QuestionID: "question-1", QuestionRevision: 1, Answer: "Use the narrow API."}); answerErr != nil || status != "resume_queued" {
		t.Fatalf("repeat answer status=%q err=%v", status, answerErr)
	}
	if _, answerErr := db.AnswerQuestion(ctx, AnswerSpec{TaskID: grant.TaskID, WorkRevision: 1, QuestionID: "question-1", QuestionRevision: 1, Answer: "Use a different API."}); !errors.Is(answerErr, ErrConflict) {
		t.Fatalf("conflicting answer err=%v", answerErr)
	}
	resumed, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AttemptID != grant.AttemptID || resumed.SegmentID == grant.SegmentID || resumed.Answer != "Use the narrow API." || resumed.SessionID != "conversation-1" || resumed.QuestionID != "question-1" {
		t.Fatalf("resumed=%#v initial=%#v", resumed, grant)
	}
}

func TestRetryUsesOnlyPreauthorizedFallbackAndAcceptReleasesDependency(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	primary := json.RawMessage(`{"kind":"fake","name":"primary"}`)
	fallback := json.RawMessage(`{"kind":"fake","name":"fallback"}`)
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "retry-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
		Tasks: []TaskSpec{{ID: "retry-task", RunID: "retry-run", MaxAttempts: 3, AdapterPayload: primary, FallbackPayloads: []json.RawMessage{fallback}}, {ID: "retry-dependent", RunID: "retry-run", Dependencies: []string{"retry-task"}, MaxAttempts: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(grant contract.LaunchCommand, sequence int64, kind, hash string) {
		exit := 1
		event := contract.Event{Version: 1, ProducerID: hostID, EventID: fmt.Sprintf("%s:%d", hostID, sequence), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: sequence, Kind: kind, PayloadHash: hash, ExitCode: &exit}
		if _, commitErr := db.CommitHostEvent(ctx, event); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	commit(first, 1, contract.EventFailed, strings.Repeat("c", 64))
	commit(first, 2, contract.EventExited, strings.Repeat("d", 64))
	if err = db.QueueRetry(ctx, first.TaskID, 1, true); err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || second.AttemptID == first.AttemptID || string(second.AdapterPayload) != string(fallback) {
		t.Fatalf("fallback grant=%#v err=%v", second, err)
	}
	commit(second, 3, contract.EventResult, strings.Repeat("e", 64))
	commit(second, 4, contract.EventExited, strings.Repeat("f", 64))
	if snapshot, snapshotErr := db.TaskSnapshot(ctx, first.TaskID); snapshotErr != nil || snapshot.Status != "result_ready" {
		t.Fatalf("result snapshot=%#v err=%v", snapshot, snapshotErr)
	}
	events, err := db.CollectPending(ctx, first.TaskID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	result := events[1]
	review := ReviewSpec{TaskID: first.TaskID, WorkRevision: 1, EventID: result.EventID, EventRevision: result.EventRevision, EventHash: result.PayloadHash, ActionSlot: result.ActionSlot, Decision: "reject", CommandID: "review-result"}
	if status, reviewErr := db.ReviewResult(ctx, review); reviewErr != nil || status != "rejected" {
		t.Fatalf("review status=%q err=%v", status, reviewErr)
	}
	if status, reviewErr := db.ReviewResult(ctx, review); reviewErr != nil || status != "rejected" {
		t.Fatalf("repeat review status=%q err=%v", status, reviewErr)
	}
	conflict := review
	conflict.Decision = "accept"
	if _, reviewErr := db.ReviewResult(ctx, conflict); !errors.Is(reviewErr, ErrConflict) {
		t.Fatalf("conflicting review err=%v", reviewErr)
	}
	if err = db.QueueRetry(ctx, first.TaskID, 1, false); err != nil {
		t.Fatal(err)
	}
	third, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || third.AttemptID == second.AttemptID || string(third.AdapterPayload) != string(fallback) {
		t.Fatalf("third grant=%#v err=%v", third, err)
	}
	commit(third, 5, contract.EventResult, strings.Repeat("1", 64))
	commit(third, 6, contract.EventExited, strings.Repeat("2", 64))
	events, err = db.CollectPending(ctx, first.TaskID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events after rework=%#v err=%v", events, err)
	}
	finalResult := events[2]
	accepted := ReviewSpec{TaskID: first.TaskID, WorkRevision: 1, EventID: finalResult.EventID, EventRevision: finalResult.EventRevision, EventHash: finalResult.PayloadHash, ActionSlot: finalResult.ActionSlot, Decision: "accept", CommandID: "review-final"}
	if status, reviewErr := db.ReviewResult(ctx, accepted); reviewErr != nil || status != "accepted" {
		t.Fatalf("final review status=%q err=%v", status, reviewErr)
	}
	dependent, err := db.TaskSnapshot(ctx, "retry-dependent")
	if err != nil || dependent.Status != "ready" {
		t.Fatalf("dependent=%#v err=%v", dependent, err)
	}
	if ackErr := db.AckDecision(ctx, first.TaskID, AckDecision{EventID: events[0].EventID, EventRevision: events[0].EventRevision, EventHash: events[0].PayloadHash, ActionSlot: events[0].ActionSlot, Decision: "handled", CommandID: "bad-current-ack"}); !errors.Is(ackErr, ErrConflict) {
		t.Fatalf("old event handled err=%v", ackErr)
	}
	ackEvent(t, db, first.TaskID, events[0], "stale")
	ackEvent(t, db, first.TaskID, events[1], "stale")
	ackEvent(t, db, first.TaskID, events[2], "handled")
}

func TestStorageAdmissionBlocksDispatchAndEventAck(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "storage-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"}, Tasks: []TaskSpec{{ID: "storage-task", RunID: "storage-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.minFreeBytes = ^uint64(0)
	if _, claimErr := db.ClaimReady(ctx, hostID, 1, 2); claimErr == nil || claimErr.Error() != "storage_blocked" {
		t.Fatalf("low disk claim err=%v", claimErr)
	}
	db.minFreeBytes = 0
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	db.runControlLimit = 1
	event := contract.Event{Version: 1, ProducerID: hostID, EventID: hostID + ":1", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: strings.Repeat("a", 64)}
	if _, commitErr := db.CommitHostEvent(ctx, event); commitErr == nil || commitErr.Error() != "control_spool_full" {
		t.Fatalf("quota event err=%v", commitErr)
	}
}
