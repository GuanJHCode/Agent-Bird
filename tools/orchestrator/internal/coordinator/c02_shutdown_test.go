package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
)

func TestC02RecoveryServerRechecksIntentUnderInstanceLock(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := instance.PrepareStateDir(state); err != nil {
		t.Fatal(err)
	}
	if err := CheckRecoveryAllowed(state, contract.HostHello{}); err != nil {
		t.Fatal(err)
	}
	lock, err := instance.Acquire(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRestartIntent(state, restartIntent{Version: 1, Epoch: lock.Epoch(), Blocked: true, TerminatedHosts: []string{}}); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	recovered, err := NewServerForRecovery(state)
	if err == nil {
		_ = recovered.Close()
		t.Fatal("recovery ignored newly durable shutdown intent")
	}
	if err.Error() != "coordinator_shutdown" {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("blocked recovery touched DB: %v", err)
	}
}

func TestC02ShutdownBlocksRecoveryAndExplicitNewOperationClearsGlobalBlock(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "c02-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	first, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if err := CheckRecoveryAllowed(state, contract.HostHello{}); err == nil {
		t.Fatal("Shutdown lost its durable restart fence")
	}
	if server, err := NewServerForRecovery(state); err == nil {
		_ = server.Close()
		t.Fatal("automatic restart cleared Shutdown intent")
	}
	explicit, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	if err := CheckRecoveryAllowed(state, contract.HostHello{}); err != nil {
		t.Fatalf("explicit operation remained globally blocked: %v", err)
	}
}
