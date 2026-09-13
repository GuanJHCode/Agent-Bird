package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
