package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestOpenMigratesAndCommitsRunTaskAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database mode=%v", info.Mode().Perm())
	}
	if err := db.CreateRun(context.Background(), RunSpec{ID: "run-1", ControllerThread: "thread-1", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), TaskSpec{ID: "task-a", RunID: "run-1", Dependencies: nil, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.StartAttempt(context.Background(), "task-a", "segment-1")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.AttemptNo != 1 || attempt.Status != "running" {
		t.Fatalf("unexpected attempt: %#v", attempt)
	}
	if err := db.FinishAttempt(context.Background(), attempt.ID, "result_ready"); err != nil {
		t.Fatal(err)
	}
	got, err := db.Attempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "result_ready" {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestStartAttemptRejectsDependencyAndThirdAttempt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateRun(ctx, RunSpec{ID: "run-1", ControllerThread: "thread-1", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, TaskSpec{ID: "upstream", RunID: "run-1", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, TaskSpec{ID: "downstream", RunID: "run-1", Dependencies: []string{"upstream"}, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartAttempt(ctx, "downstream", "segment-1"); err == nil {
		t.Fatal("expected dependency_blocked")
	}
	for i := 1; i <= 3; i++ {
		a, err := db.StartAttempt(ctx, "upstream", "segment-"+strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.FinishAttempt(ctx, a.ID, "failed"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.StartAttempt(ctx, "upstream", "segment-4"); err == nil {
		t.Fatal("expected attempt_limit")
	}
}
