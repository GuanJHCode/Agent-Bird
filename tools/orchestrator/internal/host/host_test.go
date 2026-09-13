package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"os"
	"path/filepath"
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
	if err = db.CreateRun(ctx, store.RunSpec{ID: "run", ControllerThread: "thread", PlanRevision: 1}); err != nil {
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
	cmd := process.Command{Path: os.Args[0], Args: []string{"-test.run=TestSubmitHostCollectAckAndExplicitResume"}, Env: []string{"G1_HOST_HELPER=1"}}
	result, err := h.Execute(ctx, attempt, "run", "task", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "result_ready" || result.EventCount < 2 {
		t.Fatalf("result=%#v", result)
	}
	if _, err = h.Collect(ctx, attempt); err != nil {
		t.Fatal(err)
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
