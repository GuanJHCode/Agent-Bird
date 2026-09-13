package coordinator

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSubmitExecuteCollectAckAndExplicitResume(t *testing.T) {
	if os.Getenv("G1_COORD_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("coordinator-result\n")
		return
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := New(db, filepath.Join(t.TempDir(), "spool"), 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Submit(ctx, "run", "thread", "task", contract.HostIdentity{OriginContextID: "test", PID: os.Getpid(), Birth: "birth"}, nil, 3); err != nil {
		t.Fatal(err)
	}
	cmd := process.Command{Path: os.Args[0], Args: []string{"-test.run=TestSubmitExecuteCollectAckAndExplicitResume"}, Env: []string{"G1_COORD_HELPER=1"}}
	r, err := c.Execute(ctx, "run", "task", "segment-1", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "result_ready" {
		t.Fatalf("result=%#v", r)
	}
	if err = c.Ack(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	r, err = c.Resume(ctx, "task", "segment-2", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "result_ready" {
		t.Fatalf("resume=%#v", r)
	}
}
