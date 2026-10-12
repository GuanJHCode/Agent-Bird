package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type coreContinuationInvocation struct{ c02ReportWorkerInvocation }

func (coreContinuationInvocation) OutputProvider() string { return "antigravity-cli" }

// An owned executable fixture emits native-shaped output; it never calls a
// provider/account/model. Its second execution waits on the test's file barrier.
func TestCoreContinuationWorkerHelper(t *testing.T) {
	if os.Getenv("CORE_CONTINUATION_WORKER") != "1" {
		return
	}
	fmt.Println(`{"event":"init","conversation_id":"core-bound-session"}`)
	if os.Getenv("CORE_CONTINUATION_STAGE") == "first" {
		fmt.Println(`{"event":"result","result":{"conversation_id":"core-bound-session","status":"WAITING","response":"Technical question"}}`)
		os.Exit(0)
	}
	timer := time.NewTimer(10 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		if _, err := os.Stat(os.Getenv("CORE_CONTINUATION_RELEASE")); err == nil {
			fmt.Println(`{"event":"result","result":{"conversation_id":"core-bound-session","status":"SUCCESS","response":"continued"}}`)
			os.Exit(0)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			os.Exit(3)
		}
	}
}

func TestCoreTrustedLiveContinuationSurvivesCoordinatorEpochChange(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "core-continuation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	first, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Serve(firstCtx) }()
	t.Cleanup(func() { _ = first.Close(); cancelFirst() })
	pid, birth, executable := testHostIdentity(t)
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	payload, _ := json.Marshal(adapter.InvocationPayload{Provider: "antigravity-cli", Directory: root, Prompt: "Original instruction", Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 10000}, ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: adapter.BinaryPin{Path: executable, Version: "fixture-v1", SHA256: hex.EncodeToString(sum[:])}}})
	plan, err := first.db.SubmitPlan(context.Background(), store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: executable}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 2, MaxActiveMS: 15000, AdapterPayload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "release")
	grants := make(chan contract.LaunchCommand, 2)
	var invocations atomic.Int32
	hostCtx, cancelHost := context.WithTimeout(context.Background(), 15*time.Second)
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- host.RunSource(hostCtx, host.SourceConfig{SocketPath: first.SocketPath(), SpoolRoot: filepath.Join(root, "spool"), Hello: contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: pid, Birth: birth, Executable: executable}, InvocationProvider: func(_ context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
			stage := "first"
			if invocations.Add(1) == 2 {
				stage = "continued"
			}
			grants <- grant
			return coreContinuationInvocation{c02ReportWorkerInvocation{args: []string{executable, "-test.run=^TestCoreContinuationWorkerHelper$"}, env: map[string]string{"CORE_CONTINUATION_WORKER": "1", "CORE_CONTINUATION_STAGE": stage, "CORE_CONTINUATION_RELEASE": release}}}, nil
		}})
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		cancelHost()
		select {
		case <-hostDone:
		case <-time.After(12 * time.Second):
			t.Error("owned living continuation Source did not join")
		}
	})
	c02Wait(t, "original trusted question and exited segment", func() bool {
		var status string
		var exits int
		return first.db.SQL().QueryRow(`SELECT t.status,(SELECT COUNT(*) FROM segment_runtime WHERE status='exited') FROM tasks t WHERE t.id='task'`).Scan(&status, &exits) == nil && status == "waiting_question" && exits == 1
	})
	original := <-grants
	var question string
	var revision int
	if err = first.db.SQL().QueryRow(`SELECT question_id,question_revision FROM runtime_questions WHERE task_id='task' AND status='open'`).Scan(&question, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err = first.db.AnswerQuestion(context.Background(), store.AnswerSpec{TaskID: "task", WorkRevision: 1, QuestionID: question, QuestionRevision: revision, Answer: "trusted answer"}); err != nil {
		t.Fatal(err)
	}
	first.dispatchAll()
	continued := <-grants
	if continued.Continuation == nil || continued.Continuation.SourceKind != "host" || continued.Continuation.OwnerContextID != "origin" || continued.Continuation.SourceSegmentID != original.SegmentID || continued.AttemptID != original.AttemptID || continued.SessionID != "core-bound-session" || continued.SessionKind != "conversation-id" || continued.Answer != "trusted answer" || continued.GrantedActiveMS >= original.GrantedActiveMS {
		t.Fatalf("untrusted or reset continuation grant: %+v", continued)
	}
	var workerPID int
	var cap, token, directory string
	c02Wait(t, "live trusted resumed worker and report binding", func() bool {
		return first.db.SQL().QueryRow(`SELECT c.capability_id,c.token_hash,c.capability_dir,json_extract(e.body_json,'$.process.pid') FROM report_capabilities c JOIN runtime_events e ON e.segment_id=c.segment_id WHERE c.segment_id=? AND c.status='registered' AND json_extract(e.body_json,'$.kind')='running' AND EXISTS(SELECT 1 FROM continuation_decisions d JOIN attempt_provider_bindings b ON b.attempt_id=d.attempt_id WHERE d.target_segment_id=c.segment_id AND d.status='claimed')`, continued.SegmentID).Scan(&cap, &token, &directory, &workerPID) == nil
	})
	workerBirth, err := process.Birth(workerPID)
	if err != nil {
		t.Fatal(err)
	}
	capabilityFile := filepath.Join(filepath.Dir(directory), "capability.json")
	originalFile, err := os.ReadFile(capabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	var usedBefore int64
	if err = first.db.SQL().QueryRow(`SELECT SUM(b.used_ms) FROM budget_runtime b JOIN segment_runtime s ON s.reservation_id=b.id WHERE s.attempt_id=?`, original.AttemptID).Scan(&usedBefore); err != nil || usedBefore <= 0 {
		t.Fatalf("original continuation charge=%d %v", usedBefore, err)
	}
	oldEpoch := first.Epoch()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	<-firstDone
	second, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Serve(secondCtx) }()
	t.Cleanup(func() { _ = second.Close(); cancelSecond(); <-secondDone })
	c02Wait(t, "original Host and resumed report capability in new ready epoch", func() bool {
		var hostEpoch, capEpoch uint64
		var status string
		return second.db.SQL().QueryRow(`SELECT h.coordinator_epoch,h.status,c.execution_epoch FROM runtime_hosts h JOIN report_capabilities c ON c.producer_id=h.id WHERE c.capability_id=?`, cap).Scan(&hostEpoch, &status, &capEpoch) == nil && status == "ready" && hostEpoch == second.Epoch() && capEpoch == hostEpoch
	})
	current, err := os.ReadFile(capabilityFile)
	if err != nil || string(current) != string(originalFile) || second.Epoch() <= oldEpoch || invocations.Load() != 2 {
		t.Fatalf("reconnect changed report path/token/launch count: %v calls=%d", err, invocations.Load())
	}
	if currentBirth, err := process.Birth(workerPID); err != nil || currentBirth != workerBirth {
		t.Fatalf("live trusted worker replaced across epoch: %q %v", currentBirth, err)
	}
	var reboundToken, reboundDir string
	var usedDuring int64
	if err = second.db.SQL().QueryRow(`SELECT c.token_hash,c.capability_dir,(SELECT SUM(b.used_ms) FROM budget_runtime b JOIN segment_runtime s ON s.reservation_id=b.id WHERE s.attempt_id=c.attempt_id) FROM report_capabilities c WHERE c.capability_id=?`, cap).Scan(&reboundToken, &reboundDir, &usedDuring); err != nil || reboundToken != token || reboundDir != directory || usedDuring != usedBefore {
		t.Fatalf("same binding/charge not retained: %d/%d %v", usedDuring, usedBefore, err)
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c02Wait(t, "resumed original worker result and once-only settlement", func() bool {
		var status string
		var exits int
		return second.db.SQL().QueryRow(`SELECT t.status,(SELECT COUNT(*) FROM segment_runtime WHERE status='exited') FROM tasks t WHERE t.id='task'`).Scan(&status, &exits) == nil && status == "result_ready" && exits == 2
	})
	var attempts, segments, decisions, unknowns int
	var finalUsed int64
	if err = second.db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM continuation_decisions WHERE status='claimed'),(SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='unknown'),SUM(b.used_ms) FROM budget_runtime b JOIN segment_runtime s ON s.reservation_id=b.id WHERE s.attempt_id=?`, original.AttemptID).Scan(&attempts, &segments, &decisions, &unknowns, &finalUsed); err != nil || attempts != 1 || segments != 2 || decisions != 1 || unknowns != 0 || finalUsed <= usedBefore || invocations.Load() != 2 {
		t.Fatalf("epoch replay reset/repeated continuation: %d/%d/%d/%d charge=%d/%d calls=%d %v", attempts, segments, decisions, unknowns, finalUsed, usedBefore, invocations.Load(), err)
	}
	var actualPayload string
	if err = second.db.SQL().QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id='task'`).Scan(&actualPayload); err != nil || actualPayload != string(payload) {
		t.Fatalf("trusted payload changed across reconnect: %v", err)
	}
}
