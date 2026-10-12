//go:build darwin

package process

import (
	"context"
	"os"
	"testing"
)

func TestNativeProcessArgumentSnapshot(t *testing.T) {
	snapshot, err := ObserveArguments(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PID != os.Getpid() || snapshot.ParentPID != os.Getppid() || snapshot.UID != os.Geteuid() || snapshot.Birth == "" || len(snapshot.Args) != len(os.Args) {
		t.Fatalf("invalid snapshot: pid=%d ppid=%d args=%d", snapshot.PID, snapshot.ParentPID, len(snapshot.Args))
	}
	for i, arg := range os.Args {
		if snapshot.Args[i] != arg {
			t.Fatalf("argv mismatch at %d", i)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveArguments(ctx, os.Getpid()); err != context.Canceled {
		t.Fatal(err)
	}
}
