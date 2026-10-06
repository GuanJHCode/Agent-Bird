package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type c02CommitThenLoseACK struct {
	db   *store.DB
	lose bool
}

func (s *c02CommitThenLoseACK) SendEvent(ctx context.Context, epoch uint64, event contract.Event) (contract.DurableAck, error) {
	if event.ExecutionEpoch != epoch {
		return contract.DurableAck{}, errors.New("wire_epoch_mismatch")
	}
	ack, err := s.db.CommitHostEvent(ctx, event)
	if err == nil && s.lose {
		return contract.DurableAck{}, io.EOF
	}
	return ack, err
}

func TestSourceCommittedEventLostACKReplaysAtNewEpochWithoutChangingSpool(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "r", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "g", Executable: "/pinned/orchestrator"}, Tasks: []store.TaskSpec{{ID: "t", RunID: "r", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "g", PID: 10, Birth: "host", Executable: "/pinned/orchestrator"}
	id, err := db.RegisterHost(ctx, hello, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, id, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), id)
	if err != nil {
		t.Fatal(err)
	}
	attempt := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	spool, err := h.spool(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendPhaseEvent(spool, "r", "t", attempt, launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: 1}, contract.EventPrepared, hashText("prepared"), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "event-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("event files=%v err=%v", files, err)
	}
	original, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = h.PublishAll(ctx, &c02CommitThenLoseACK{db: db, lose: true}, 1); !errors.Is(err, io.EOF) {
		t.Fatalf("lost committed ACK=%v", err)
	}
	if published, err := h.readPublished(); err != nil || published != 0 {
		t.Fatalf("lost ACK advanced cursor=%d err=%v", published, err)
	}
	if err = db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RegisterHost(ctx, hello, 2); err != nil {
		t.Fatal(err)
	}
	if err = db.BeginHostReconcile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = h.PublishAll(ctx, &c02CommitThenLoseACK{db: db}, 2); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishHostReconcile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if published, err := h.readPublished(); err != nil || published != 1 {
		t.Fatalf("replayed cursor=%d err=%v", published, err)
	}
	archived, err := os.ReadFile(filepath.Join(filepath.Dir(files[0]), "archive", filepath.Base(files[0])))
	if err != nil || string(archived) != string(original) {
		t.Fatalf("original spool bytes changed: %v", err)
	}
	var events, attempts, segments, budgets int
	if err = db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM runtime_events),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime)`).Scan(&events, &attempts, &segments, &budgets); err != nil || events != 1 || attempts != 1 || segments != 1 || budgets != 1 {
		t.Fatalf("replay duplicated durable rows=%d/%d/%d/%d err=%v", events, attempts, segments, budgets, err)
	}
	var originalEpoch int
	if err = db.SQL().QueryRow(`SELECT json_extract(body_json,'$.execution_epoch') FROM runtime_events`).Scan(&originalEpoch); err != nil || originalEpoch != 1 {
		t.Fatalf("canonical event rewritten: epoch=%d err=%v", originalEpoch, err)
	}
}
