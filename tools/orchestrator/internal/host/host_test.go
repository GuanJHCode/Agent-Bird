package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNormalLeaderExitWithSurvivingProcessGroupStaysUnknown(t *testing.T) {
	if os.Getenv("G1_ORPHAN_LEADER") == "1" {
		child := exec.Command(os.Args[0], "-test.run=TestNormalLeaderExitWithSurvivingProcessGroupStaysUnknown")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "G1_ORPHAN_LEADER=") {
				child.Env = append(child.Env, value)
			}
		}
		child.Env = append(child.Env, "G1_ORPHAN_SURVIVOR=1")
		child.Stdout, child.Stderr = nil, nil
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("G1_ORPHAN_PID_FILE"), []byte(fmt.Sprint(child.Process.Pid)), 0600)
		return
	}
	if os.Getenv("G1_ORPHAN_SURVIVOR") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "survivor.pid")
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "orphan-producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "orphan-command", ReservationID: "orphan-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", GrantedActiveMS: 2000}
	inv := launchInvocation{args: []string{binary, "-test.run=TestNormalLeaderExitWithSurvivingProcessGroupStaysUnknown"}, env: map[string]string{"G1_ORPHAN_LEADER": "1", "G1_ORPHAN_PID_FILE": pidFile}}
	result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
	childBody, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	childPID, parseErr := strconv.Atoi(string(childBody))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	defer syscall.Kill(childPID, syscall.SIGKILL)
	if runErr == nil || result.Status != "unknown" {
		t.Fatalf("result=%#v err=%v", result, runErr)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	for _, event := range sink.events {
		if event.Kind == contract.EventResult || event.Kind == contract.EventQuestion || event.Kind == contract.EventExited {
			t.Fatalf("unsettled group published terminal event: %#v", sink.events)
		}
	}
	if len(sink.events) == 0 || sink.events[len(sink.events)-1].Kind != contract.EventUnknown {
		t.Fatalf("events=%#v", sink.events)
	}
}

func TestSubmitHostCollectAckAndExplicitResume(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("synthetic-result\n")
		return
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateRun(ctx, store.RunSpec{ID: "run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(ctx, store.TaskSpec{ID: "task", RunID: "run", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.StartAttempt(ctx, "task", "segment-1")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := process.Command{Path: func() string { p, _ := filepath.EvalSymlinks(os.Args[0]); return p }(), Args: []string{"-test.run=TestSubmitHostCollectAckAndExplicitResume"}, Env: []string{"G1_HOST_HELPER=1"}}
	result, err := h.Execute(ctx, attempt, "run", "task", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "result_ready" || result.EventCount < 2 || result.ArtifactPath == "" {
		t.Fatalf("result=%#v", result)
	}
	artifact, err := os.ReadFile(result.ArtifactPath)
	if err != nil || !strings.Contains(string(artifact), "synthetic-result\n") {
		t.Fatalf("artifact=%q err=%v", artifact, err)
	}
	if _, err = h.Collect(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	sp, err := events.Open(filepath.Join(filepath.Dir(result.ArtifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	var launch map[string]any
	if err = sp.ReadMetadata("launch.json", &launch); err != nil {
		t.Fatal(err)
	}
	if launch["version"] != float64(1) || launch["phase"] != "exited" || launch["attempt_id"] != attempt.ID || launch["segment_id"] != attempt.SegmentID {
		t.Fatalf("launch metadata=%#v", launch)
	}
	proc, ok := launch["process"].(map[string]any)
	if !ok || proc["pid"] == nil || proc["pgid"] == nil || proc["executable_sha256"] == "" || proc["birth_known"] != true || proc["birth"] == "" {
		t.Fatalf("process metadata=%#v", launch["process"])
	}
	if err = h.Ack(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	next, err := db.StartSegment(ctx, attempt.ID, "segment-2")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err = h.Execute(ctx2, next, "run", "task", cmd); err != nil {
		t.Fatal(err)
	}
	if err = h.Ack(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestAckKeepsSpoolWhenDatabaseCommitFails(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		return
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.CreateRun(ctx, store.RunSpec{ID: "run-ack", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(ctx, store.TaskSpec{ID: "task-ack", RunID: "run-ack", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	a, err := db.StartAttempt(ctx, "task-ack", "segment-1")
	if err != nil {
		t.Fatal(err)
	}
	spoolRoot := filepath.Join(t.TempDir(), "spool")
	h, err := New(db, spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	cmd := process.Command{Path: func() string { p, _ := filepath.EvalSymlinks(os.Args[0]); return p }(), Args: []string{"-test.run=TestAckKeepsSpoolWhenDatabaseCommitFails"}, Env: []string{"G1_HOST_HELPER=1"}}
	if _, err = h.Execute(ctx, a, "run-ack", "task-ack", cmd); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = h.Ack(ctx, a); err == nil {
		t.Fatal("expected database failure")
	}
	s, err := events.Open(filepath.Join(spoolRoot, a.ID, a.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	acked, err := s.AckedThrough()
	if err != nil {
		t.Fatal(err)
	}
	if acked != 0 {
		t.Fatalf("spool ack advanced to %d", acked)
	}
}

type launchInvocation struct {
	args []string
	dir  string
	env  map[string]string
}

func (i launchInvocation) Args() []string                 { return append([]string(nil), i.args...) }
func (i launchInvocation) WorkingDirectory() string       { return i.dir }
func (i launchInvocation) Stdin() []byte                  { return nil }
func (i launchInvocation) Environment() map[string]string { return i.env }

type providerInvocation struct {
	launchInvocation
	provider string
}

func (i providerInvocation) OutputProvider() string { return i.provider }

func TestStructuredProviderTerminalControlsOutcome(t *testing.T) {
	if mode := os.Getenv("G1_STRUCTURED_HELPER"); mode != "" {
		switch mode {
		case "success":
			_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"status","session_id":"claude-session"}` + "\n")
			_, _ = os.Stderr.WriteString("bounded diagnostic\n")
			_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"claude-session"}` + "\n")
			_, _ = os.Stdout.WriteString(`{"type":"result","subtype":"success","session_id":"claude-session","result":"structured answer"}` + "\n")
		case "provider-error":
			_, _ = os.Stdout.WriteString(`{"type":"result","subtype":"error_during_execution","session_id":"claude-session","result":"provider rejected the task"}` + "\n")
		case "waiting":
			_, _ = os.Stdout.WriteString(`{"event":"init","conversation_id":"agy-session"}` + "\n")
			_, _ = os.Stdout.WriteString(`{"event":"result","result":{"conversation_id":"agy-session","status":"WAITING","response":"Choose the package"}}` + "\n")
		case "missing-terminal":
			_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"claude-session"}` + "\n")
		case "nonzero":
			_, _ = os.Stdout.WriteString(`{"type":"result","subtype":"success","session_id":"claude-session","result":"structured answer"}` + "\n")
			os.Exit(7)
		case "grok-stream":
			_, _ = os.Stdout.WriteString(`{"type":"text","sessionId":"grok-session","data":"hello "}` + "\n")
			_, _ = os.Stdout.WriteString(`{"type":"text","sessionId":"grok-session","data":"world"}` + "\n")
			_, _ = os.Stdout.WriteString(`{"type":"end","sessionId":"grok-session","stopReason":"end_turn"}` + "\n")
		}
		os.Exit(0)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, mode, provider, status, terminal, sessionKind, sessionID, artifact string
	}{
		{"success", "success", "claude-code", "result_ready", contract.EventResult, "session-id", "claude-session", "structured answer"},
		{"provider error exits zero", "provider-error", "claude-code", "failed", contract.EventFailed, "session-id", "claude-session", "provider rejected the task"},
		{"waiting", "waiting", "antigravity-cli", "waiting_user", contract.EventQuestion, "conversation-id", "agy-session", "Choose the package"},
		{"missing terminal", "missing-terminal", "claude-code", "failed", contract.EventFailed, "session-id", "claude-session", ""},
		{"nonzero", "nonzero", "claude-code", "failed", contract.EventFailed, "session-id", "claude-session", "structured answer"},
		{"grok stream", "grok-stream", "grok-build", "result_ready", contract.EventResult, "session-id", "grok-session", "hello world"},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, newErr := NewIPC(root, "structured-producer-"+tc.mode)
			if newErr != nil {
				t.Fatal(newErr)
			}
			grant := contract.LaunchCommand{CommandID: "structured-command-" + tc.mode, ReservationID: "structured-reservation-" + tc.mode, RunID: "run", TaskID: "task-" + tc.mode, AttemptID: "attempt-" + tc.mode, SegmentID: "segment-" + tc.mode, WorkRevision: index + 1, GrantedActiveMS: 2000}
			inv := providerInvocation{launchInvocation: launchInvocation{args: []string{path, "-test.run=TestStructuredProviderTerminalControlsOutcome"}, env: map[string]string{"G1_STRUCTURED_HELPER": tc.mode}}, provider: tc.provider}
			result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if result.Status != tc.status {
				t.Fatalf("status=%q result=%#v", result.Status, result)
			}
			sink := &durableSink{}
			if publishErr := h.PublishAll(context.Background(), sink, 1); publishErr != nil {
				t.Fatal(publishErr)
			}
			var terminal *contract.Event
			for eventIndex := range sink.events {
				if sink.events[eventIndex].Kind == tc.terminal {
					terminal = &sink.events[eventIndex]
				}
			}
			if terminal == nil {
				t.Fatalf("terminal %q missing: %#v", tc.terminal, sink.events)
			}
			body, _ := json.Marshal(terminal)
			if !strings.Contains(string(body), `"session_kind":"`+tc.sessionKind+`"`) || !strings.Contains(string(body), `"session_id":"`+tc.sessionID+`"`) {
				t.Fatalf("session metadata missing: %s", body)
			}
			if tc.terminal == contract.EventQuestion && (!strings.Contains(string(body), `"question_id":`) || !strings.Contains(string(body), `"question_revision":1`)) {
				t.Fatalf("question metadata missing: %s", body)
			}
			if tc.artifact != "" {
				if terminal.Artifact == nil {
					t.Fatalf("artifact missing: %#v", terminal)
				}
				artifact, readErr := os.ReadFile(terminal.Artifact.Path)
				if readErr != nil || string(artifact) != tc.artifact {
					t.Fatalf("artifact=%q err=%v", artifact, readErr)
				}
			}
		})
	}
}

func TestProviderSessionIsPublishedBeforeTerminalAndBindsResume(t *testing.T) {
	if os.Getenv("G1_SESSION_HELPER") == "1" {
		_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"claude-live-session"}` + "\n")
		release := os.Getenv("G1_SESSION_RELEASE")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(release); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = os.Stdout.WriteString(`{"type":"result","subtype":"success","session_id":"claude-live-session","result":"done"}` + "\n")
		os.Exit(0)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "session-producer")
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(t.TempDir(), "release")
	grant := contract.LaunchCommand{CommandID: "session-command", ReservationID: "session-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment-1", WorkRevision: 1, GrantedActiveMS: 3000}
	inv := providerInvocation{launchInvocation: launchInvocation{args: []string{path, "-test.run=TestProviderSessionIsPublishedBeforeTerminalAndBindsResume"}, env: map[string]string{"G1_SESSION_HELPER": "1", "G1_SESSION_RELEASE": release}}, provider: "claude-code"}
	type execution struct {
		result contract.Result
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
		done <- execution{result: result, err: runErr}
	}()
	sink := &durableSink{}
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		if err = h.PublishAll(context.Background(), sink, 1); err != nil {
			t.Fatal(err)
		}
		sink.mu.Lock()
		for _, event := range sink.events {
			if event.Kind == "session" && event.SessionKind == "session-id" && event.SessionID == "claude-live-session" {
				found = true
			}
		}
		sink.mu.Unlock()
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("provider session was not durably published while the segment was running")
	}
	if err = os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	executed := <-done
	if executed.err != nil || executed.result.Status != "result_ready" {
		t.Fatalf("result=%#v err=%v", executed.result, executed.err)
	}
	resume := grant
	resume.CommandID = "resume-command"
	resume.ReservationID = "resume-reservation"
	resume.SegmentID = "segment-2"
	resume.QuestionID = "report-question"
	resume.QuestionRevision = 1
	resume.Answer = "continue"
	resume.SessionKind = "session-id"
	resume.SessionID = "forged-session"
	if _, err = h.ExecuteLaunch(context.Background(), resume, inv); err == nil || err.Error() != "resume_session_mismatch" {
		t.Fatalf("mismatched resume err=%v", err)
	}
}

func TestExecuteLaunchConsumesReservationAndUsesBoundedGrant(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		return
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.CreateRun(ctx, store.RunSpec{ID: "grant-run", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(ctx, store.TaskSpec{ID: "grant-task", RunID: "grant-run", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.StartAttempt(ctx, "grant-task", "grant-segment")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	inv := launchInvocation{args: []string{path, "-test.run=TestExecuteLaunchConsumesReservationAndUsesBoundedGrant"}, env: map[string]string{"G1_HOST_HELPER": "1"}}
	grant := contract.LaunchCommand{ReservationID: "reservation-1", RunID: "grant-run", TaskID: "grant-task", AttemptID: attempt.ID, SegmentID: attempt.SegmentID, GrantedActiveMS: 1500, DeadlineUnixMS: time.Now().Add(2 * time.Second).UnixMilli()}
	result, err := h.ExecuteLaunch(ctx, grant, inv)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "result_ready" {
		t.Fatalf("result=%#v", result)
	}
	if _, err = h.ExecuteLaunch(ctx, grant, inv); err == nil || err.Error() != "reservation_reused" {
		t.Fatalf("duplicate grant err=%v", err)
	}
}

func TestStoppedLaunchPublishesStoppedThenExitedWithoutUnknown(t *testing.T) {
	if os.Getenv("G1_STOP_HELPER") == "1" {
		time.Sleep(5 * time.Second)
		return
	}
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "stop-producer")
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "stop-command", ReservationID: "stop-reservation", RunID: "stop-run", TaskID: "stop-task", AttemptID: "stop-attempt", SegmentID: "stop-segment", GrantedActiveMS: 10_000, WorkRevision: 1, ExecutionEpoch: 4}
	inv := launchInvocation{args: []string{path, "-test.run=TestStoppedLaunchPublishesStoppedThenExitedWithoutUnknown"}, env: map[string]string{"G1_STOP_HELPER": "1"}}
	type execution struct {
		result contract.Result
		err    error
	}
	done := make(chan execution, 1)
	go func() {
		result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
		done <- execution{result: result, err: runErr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		active := h.activeBySeg[grant.SegmentID] != nil
		h.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not become active")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = h.StopSegment(stopCtx, grant.SegmentID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	executed := <-done
	if executed.result.Status != "interrupted" || executed.err == nil {
		t.Fatalf("result=%#v err=%v", executed.result, executed.err)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 4); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) < 2 || sink.events[len(sink.events)-2].Kind != contract.EventStopped || sink.events[len(sink.events)-1].Kind != contract.EventExited {
		t.Fatalf("events=%#v", sink.events)
	}
	for _, event := range sink.events {
		if event.Kind == contract.EventUnknown {
			t.Fatalf("stopped launch appended unknown: %#v", sink.events)
		}
	}
}

type durableSink struct {
	mu     sync.Mutex
	events []contract.Event
	bad    bool
}

func (s *durableSink) SendEvent(_ context.Context, _ uint64, event contract.Event) (contract.DurableAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	if s.bad {
		return contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "pending"}, nil
	}
	return contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "durable"}, nil
}

func TestIPCSourcePublishesOnlyAfterDurableAck(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		return
	}
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "producer-1")
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	inv := launchInvocation{args: []string{path, "-test.run=TestIPCSourcePublishesOnlyAfterDurableAck"}, env: map[string]string{"G1_HOST_HELPER": "1"}}
	grant := contract.LaunchCommand{ReservationID: "ipc-reservation", RunID: "ipc-run", TaskID: "ipc-task", AttemptID: "ipc-attempt", SegmentID: "ipc-segment", GrantedActiveMS: 1500}
	if _, err = h.ExecuteLaunch(context.Background(), grant, inv); err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	bad := &durableSink{bad: true}
	if err = h.Publish(context.Background(), a, bad, 1); err == nil {
		t.Fatal("expected non-durable ack rejection")
	}
	s, err := events.Open(filepath.Join(root, grant.AttemptID, grant.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	if acked, err := s.AckedThrough(); err != nil || acked != 0 {
		t.Fatalf("acked=%d err=%v", acked, err)
	}
	good := &durableSink{}
	if err = h.Publish(context.Background(), a, good, 2); err != nil {
		t.Fatal(err)
	}
	if len(good.events) != 5 {
		t.Fatalf("events=%d", len(good.events))
	}
	if acked, err := s.AckedThrough(); err != nil || acked != 5 {
		t.Fatalf("acked=%d err=%v", acked, err)
	}
}

func TestParallelLaunchesPublishOneProducerSequence(t *testing.T) {
	if os.Getenv("G1_PARALLEL_HELPER") == "1" {
		time.Sleep(150 * time.Millisecond)
		return
	}
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "launch-parallel")
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	inv := launchInvocation{args: []string{path, "-test.run=TestParallelLaunchesPublishOneProducerSequence"}, env: map[string]string{"G1_PARALLEL_HELPER": "1"}}
	grants := []contract.LaunchCommand{
		{CommandID: "parallel-a", ReservationID: "reservation-a", RunID: "run", TaskID: "task-a", AttemptID: "attempt-a", SegmentID: "segment-a", GrantedActiveMS: 2000, WorkRevision: 4},
		{CommandID: "parallel-b", ReservationID: "reservation-b", RunID: "run", TaskID: "task-b", AttemptID: "attempt-b", SegmentID: "segment-b", GrantedActiveMS: 2000, WorkRevision: 5},
	}
	results := make(chan error, len(grants))
	for _, grant := range grants {
		grant := grant
		go func() {
			_, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
			results <- runErr
		}()
	}
	for range grants {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 9); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 10 {
		t.Fatalf("published events=%d", len(sink.events))
	}
	for index, event := range sink.events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event[%d] sequence=%d", index, event.Sequence)
		}
		if event.ExecutionEpoch != 9 {
			t.Fatalf("event[%d] epoch=%d", index, event.ExecutionEpoch)
		}
	}
}

func TestUnspawnedInvocationFailurePublishesFailedThenExited(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "launch-failure")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt-failure", TaskID: "task-failure", SegmentID: "segment-failure"}
	if err = h.recordLaunchFailed(a, "run-failure", a.TaskID, launchMetadata{commandID: "command-failure", executionEpoch: 3}, errors.New("provider body must not persist")); err != nil {
		t.Fatal(err)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 3); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].Kind != contract.EventFailed || sink.events[1].Kind != contract.EventExited || sink.events[0].CommandID != "command-failure" || sink.events[0].ExecutionEpoch != 3 || sink.events[0].Process != nil {
		t.Fatalf("events=%#v", sink.events)
	}
}

func TestProducerSequenceRecoversOnlyFromDurableEvents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	first, err := NewIPC(root, "launch-recover")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt-a", TaskID: "task-a", SegmentID: "segment-a"}
	if err = first.recordLaunchFailed(a, "run", a.TaskID, launchMetadata{commandID: "command-a", executionEpoch: 1}, errors.New("unspawned")); err != nil {
		t.Fatal(err)
	}
	// This is the unsafe legacy counter shape. It must not reserve sequence
	// numbers because no corresponding event was durably appended.
	if err = os.WriteFile(filepath.Join(root, ".producer-sequence"), []byte("99\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := NewIPC(root, "launch-recover")
	if err != nil {
		t.Fatal(err)
	}
	b := store.Attempt{ID: "attempt-b", TaskID: "task-b", SegmentID: "segment-b"}
	if err = second.recordLaunchFailed(b, "run", b.TaskID, launchMetadata{commandID: "command-b", executionEpoch: 2}, errors.New("unspawned")); err != nil {
		t.Fatal(err)
	}
	sink := &durableSink{}
	if err = second.PublishAll(context.Background(), sink, 2); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 4 {
		t.Fatalf("events=%#v", sink.events)
	}
	for index, event := range sink.events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event[%d] sequence=%d", index, event.Sequence)
		}
	}
}

func TestGrantLedgerPreventsDuplicateExecutionAndClosesUnspawnedRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	grant := contract.LaunchCommand{CommandID: "command-ledger", ReservationID: "reservation-ledger", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 3, GrantedActiveMS: 1000}
	first, err := NewIPC(root, "ledger-producer")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := first.acceptGrant(grant); err != nil || duplicate {
		t.Fatalf("first accept duplicate=%v err=%v", duplicate, err)
	}
	recovered, err := NewIPC(root, "ledger-producer")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := recovered.acceptGrant(grant); err != nil || !duplicate {
		t.Fatalf("recovered accept duplicate=%v err=%v", duplicate, err)
	}
	sink := &durableSink{}
	if err = recovered.recoverDuplicateGrant(grant, 4); err != nil {
		t.Fatal(err)
	}
	if err = recovered.PublishAll(context.Background(), sink, 4); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].Kind != contract.EventFailed || sink.events[1].Kind != contract.EventExited {
		t.Fatalf("events=%#v", sink.events)
	}
	conflict := grant
	conflict.ReservationID = "different-reservation"
	if _, err = recovered.acceptGrant(conflict); err == nil || err.Error() != "grant_identity_conflict" {
		t.Fatalf("conflicting grant err=%v", err)
	}
}
