package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestBridgeBindCancelledDuringMutexWaitReturnsBeforeUnlock(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.mu.Lock()
	locked := true
	defer func() {
		if locked {
			d.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.BindBridge(ctx, contract.BridgeBindRequest{
			SchemaVersion: 1, RequestID: "cancelled", RunID: "run", ProviderID: "claude",
			NativePID: 1, NativeBirth: "birth", NativeExecutableSHA256: strings.Repeat("a", 64),
			TransportMode: "hook_pull", ManifestSHA256: strings.Repeat("b", 64),
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatalf("want deadline, got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("bridge call remained blocked after context deadline")
	}
	d.mu.Unlock()
	locked = false
}
