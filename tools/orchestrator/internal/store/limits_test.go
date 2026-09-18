package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestConcurrencyLimitsSkipBlockedKeysAndRetainUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	configure, ok := any(db).(interface {
		ConfigureConcurrency(context.Context, json.RawMessage) error
	})
	if !ok {
		t.Fatal("missing concurrency policy")
	}
	if err = configure.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":3,"providers":{"claude-code":1},"workspaces":{"/private/work/a":1}}`)); err != nil {
		t.Fatal(err)
	}
	tasks := []TaskSpec{}
	for i, payload := range []string{`{"provider":"claude-code","directory":"/private/work/a"}`, `{"provider":"claude-code","directory":"/private/work/b"}`, `{"provider":"grok-build","directory":"/private/work/a"}`, `{"provider":"grok-build","directory":"/private/work/b"}`} {
		tasks = append(tasks, TaskSpec{ID: string(rune('a' + i)), RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(payload)})
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 9, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, host, 1, 0)
	if err != nil || first.TaskID != "a" {
		t.Fatalf("first=%s %v", first.TaskID, err)
	}
	second, err := db.ClaimReady(ctx, host, 1, 0)
	if err != nil || second.TaskID != "d" {
		t.Fatalf("eligible task starved: %s %v", second.TaskID, err)
	}
	if _, err = db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, first.SegmentID); err != nil {
		t.Fatal(err)
	}
	if third, err := db.ClaimReady(ctx, host, 1, 0); err == nil {
		t.Fatalf("unknown released resources: %s", third.TaskID)
	}
	if err = configure.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimReady(ctx, host, 1, 0); err != ErrNoSlot {
		t.Fatalf("lowering limit did not retain live slots: %v", err)
	}
	if err = configure.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":0}`)); err == nil {
		t.Fatal("invalid limit accepted")
	}
}

func TestRegisteredHostAncestryBlocksDispatchBeforeWorkerEvent(t *testing.T) {
	db, launch := quotaFixture(t)
	// quotaFixture registered PID 9/birth host; no worker event has arrived yet.
	denied, err := db.IsWorkerAncestry(context.Background(), map[int]string{9: "host", 100: "new-worker"})
	if err != nil || !denied {
		t.Fatalf("pre-event worker authorized: %t %v", denied, err)
	}
	if _, err = db.sql.Exec(`UPDATE runtime_hosts SET status='offline' WHERE id=?`, launch.HostID); err != nil {
		t.Fatal(err)
	}
	denied, err = db.IsWorkerAncestry(context.Background(), map[int]string{9: "host", 100: "new-worker"})
	if err != nil || !denied {
		t.Fatal("offline host descendants authorized")
	}
	denied, err = db.IsWorkerAncestry(context.Background(), map[int]string{9: "reused-pid-new-birth"})
	if err != nil || denied {
		t.Fatal("PID reuse confused with known host")
	}
}
