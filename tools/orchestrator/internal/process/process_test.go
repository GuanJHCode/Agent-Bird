package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnedProcessCapturesAndStopsRealChild(t *testing.T) {
	if os.Getenv("G1_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("synthetic-result\n")
		time.Sleep(30 * time.Second)
		return
	}
	h, err := Start(context.Background(), Command{Path: func() string { p, _ := filepath.EvalSymlinks(os.Args[0]); return p }(), Args: []string{"-test.run=TestOwnedProcessCapturesAndStopsRealChild"}, Dir: t.TempDir(), Env: []string{"G1_HELPER=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if h.PID() <= 0 || h.PGID() <= 0 {
		t.Fatal("missing process identity")
	}
	id := h.Identity()
	if id.PID != h.PID() || id.PGID != h.PGID() || id.Executable == "" || id.ExecutableSHA256 == "" || !id.BirthKnown || id.Birth == "" {
		t.Fatalf("identity=%#v", id)
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if h.ExitCode() == -1 {
		t.Fatal("missing exit code")
	}
	if h.Output() != "synthetic-result\n" {
		t.Fatalf("output=%q", h.Output())
	}
}
