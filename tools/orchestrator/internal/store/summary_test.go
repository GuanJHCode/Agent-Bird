package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestSummaryReportFailureDoesNotAuthorizeRetryWithoutHostOutcome(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/orchestrator"},
		Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "report")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(directory, "failure.txt")
	if err := os.WriteFile(artifact, []byte("failed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: grant.ReportCapabilityID, ProducerID: hostID, RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, TokenHash: runtimeHash("report-token"), CapabilityDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"status": "failed", "artifact": contract.ArtifactRef{ID: "failure", Path: artifact, Size: 6, SHA256: runtimeHash("failed")}})
	_, err = db.CommitReportEvent(ctx, ReportEventSpec{CapabilityID: grant.ReportCapabilityID, Token: "report-token", EventID: "worker-failure", Sequence: 1, Kind: "failure", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	exit := 1
	_, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: hostID, EventID: hostID + ":1", RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventExited, ExitCode: &exit, PayloadHash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := db.SummarizeRun(ctx, "task", "thread", receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	page, err := db.CollectPending(ctx, "task", "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	event := page.Events[0]
	_, err = db.QueueRetry(ctx, RetrySpec{TaskID: "task", WorkRevision: 1, EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, SegmentID: grant.SegmentID, NextAttemptNo: 2, CommandID: "retry"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("report alone authorized retry: %v", err)
	}
	if len(summary.Tasks) != 1 || slices.Contains(summary.Tasks[0].AllowedActions, "retry") || !slices.Contains(summary.Tasks[0].AllowedActions, "resume") {
		t.Fatalf("report alone changed action eligibility: %#v", summary)
	}
}

func TestSummaryUnknownRetainsSlotAndForbidsResume(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/orchestrator"},
		Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaryActions(t, db, "task", receipt.ControlToken, "running", 1, "status", "collect", "wait-events", "stop")
	_, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: hostID, EventID: hostID + ":1", RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventUnknown, PayloadHash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	assertSummaryActions(t, db, "task", receipt.ControlToken, "blocked", 1, "status", "collect", "stop")
	if err := db.QueueResume(ctx, "task", 1); err == nil {
		t.Fatal("unknown resumed before process confirmation")
	}
}

func assertSummaryActions(t *testing.T, db *DB, taskID, token, group string, active int, actions ...string) {
	t.Helper()
	summary, err := db.SummarizeRun(context.Background(), taskID, "thread", token)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range summary.Tasks {
		if task.TaskID == taskID {
			if task.Group != group || task.ActiveSegments != active || !slices.Equal(task.AllowedActions, actions) {
				t.Fatalf("summary task=%#v want group=%s active=%d actions=%v", task, group, active, actions)
			}
			return
		}
	}
	t.Fatalf("task missing: %s", taskID)
}
