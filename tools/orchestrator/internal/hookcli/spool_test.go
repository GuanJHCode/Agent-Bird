package hookcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestSpoolKeepsRecordUntilMatchingDurableAck(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := openSpool(context.Background(), state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "event", BindingID: "binding", OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: "2026-10-03T00:00:00Z", Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	record, err := spool.put(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.put(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	pending, err := spool.pending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	if len(record.Hash) != 64 {
		t.Fatal("invalid payload hash")
	}
	if err := spool.ack(context.Background(), record, contract.BridgeIngressAck{EventID: "event", PayloadHash: "wrong", Committed: true}); err == nil {
		t.Fatal("mismatched ACK accepted")
	}
	pending, err = spool.pending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("record lost after wrong ACK: %d %v", len(pending), err)
	}
	if err := spool.ack(context.Background(), record, contract.BridgeIngressAck{EventID: record.Event.EventID, PayloadHash: record.Hash, Committed: true}); err != nil {
		t.Fatal(err)
	}
	pending, err = spool.pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("record remained after ACK: %d %v", len(pending), err)
	}
}

func TestSpoolRejectsSymlinkAndLooseModesWithoutTouchingOtherBinding(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openSpool(context.Background(), link, "one"); err == nil {
		t.Fatal("symlink state accepted")
	}
	if err := os.Chmod(state, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openSpool(context.Background(), state, "one"); err == nil {
		t.Fatal("loose state accepted")
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	one, err := openSpool(context.Background(), state, "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := openSpool(context.Background(), state, "two")
	if err != nil {
		t.Fatal(err)
	}
	if one.dir == two.dir {
		t.Fatal("bindings share spool")
	}
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "event", BindingID: "two", OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: "2026-10-03T00:00:00Z", Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	if _, err := two.put(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(two.recordPath(event.EventID), one.recordPath(event.EventID)); err != nil {
		t.Fatal(err)
	}
	if _, err := one.pending(context.Background()); err == nil {
		t.Fatal("symlink record accepted")
	}
	if pending, err := two.pending(context.Background()); err != nil || len(pending) != 1 {
		t.Fatalf("other binding records=%d err=%v", len(pending), err)
	}
	if err := os.Remove(one.recordPath(event.EventID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(one.lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(two.recordPath(event.EventID), one.lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := one.pending(context.Background()); err == nil {
		t.Fatal("symlink lock accepted")
	}
}

func TestSpoolConcurrentAdmissionPreservesUnackedRecordsAtCountLimit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := openSpool(context.Background(), state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	base := contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: "2026-10-03T00:00:00Z", Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	for i := 0; i < 998; i++ {
		event := base
		event.EventID = fmt.Sprintf("old-%d", i)
		raw, _ := json.Marshal(event)
		if err := os.WriteFile(spool.recordPath(event.EventID), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := base
			event.EventID = fmt.Sprintf("new-%d", i)
			_, err := spool.put(context.Background(), event)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success, full := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if err.Error() == "bridge_spool_full" {
			full++
		} else {
			t.Fatal(err)
		}
	}
	if success != 2 || full != 6 {
		t.Fatalf("success=%d full=%d", success, full)
	}
	pending, err := spool.pending(context.Background())
	if err != nil || len(pending) != 1000 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	for _, record := range pending {
		if record.Event.EventID == "old-0" {
			return
		}
	}
	t.Fatal("old unacked event removed")
}

func TestSpoolByteQuotaRejectsNewRecordWithoutDeletingOld(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := openSpool(context.Background(), state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "s00000", BindingID: "binding", OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: strings.Repeat("x", 62000), Kind: "session_started", ObservedAt: "2026-10-03T00:00:00Z", Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	raw, _ := json.Marshal(event)
	if len(raw) >= spoolMaxRecordBytes {
		t.Fatalf("fixture too large: %d", len(raw))
	}
	count := spoolMaxBytes / len(raw)
	if count >= spoolMaxRecords {
		t.Fatal("fixture hit count quota first")
	}
	for i := 0; i < count; i++ {
		event.EventID = fmt.Sprintf("s%05d", i)
		raw, _ = json.Marshal(event)
		if err := os.WriteFile(spool.recordPath(event.EventID), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	event.EventID = "new000"
	if _, err := spool.put(context.Background(), event); err == nil || err.Error() != "bridge_spool_full" {
		t.Fatalf("byte quota error=%v", err)
	}
	pending, err := spool.pending(context.Background())
	if err != nil || len(pending) != count {
		t.Fatalf("pending=%d count=%d err=%v", len(pending), count, err)
	}
}

func TestSpoolReplaysCommittedRecordAfterInterruptedTemporaryWrite(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := openSpool(context.Background(), state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "durable", BindingID: "binding", OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: "2026-10-03T00:00:00Z", Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	if _, err := spool.put(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool.dir, "."+strings.Repeat("a", 64)+".tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := spool.pending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].Event.EventID != "durable" {
		t.Fatalf("committed replay=%v err=%v", pending, err)
	}
}
