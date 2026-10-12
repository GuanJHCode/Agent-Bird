package hookcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

func TestHookFinalWireSecretCanaryBlocksBeforeBeginEmit(t *testing.T) {
	for _, secret := range []string{"hook", "claim"} {
		t.Run(secret, func(t *testing.T) { hookFinalWireSecretCanary(t, secret) })
	}
}

func hookFinalWireSecretCanary(t *testing.T, secret string) {
	root, err := os.MkdirTemp("/tmp", "b04-canary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	token := "hook-" + strings.Repeat("a", 24)
	claimToken := "bridge-claim-" + strings.Repeat("c", 64)
	canary := token
	if secret == "claim" {
		canary = claimToken
	}
	capRaw, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: token})
	if err := os.WriteFile(capPath, capRaw, 0600); err != nil {
		t.Fatal(err)
	}
	providers.Lock()
	old, had := providers.byID["claude"]
	providers.byID["claude"] = func(_ context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}, nil
	}
	providers.Unlock()
	defer func() {
		providers.Lock()
		if had {
			providers.byID["claude"] = old
		} else {
			delete(providers.byID, "claude")
		}
		providers.Unlock()
	}()
	batch := contract.BridgeBatch{SchemaVersion: 1, BatchID: "batch", AttemptID: "attempt-" + strings.Repeat("f", 24), BindingID: "binding", OwnerEpoch: 1, TransportMode: "hook_pull", Events: []contract.BridgeBatchEvent{{EventID: "result", EventRevision: 1, EventHash: strings.Repeat("b", 64), TaskID: "task", WorkRevision: 1, Kind: "result", Summary: "prefix " + canary + " suffix"}}, DeliveryReceipts: []contract.BridgeDeliveryReceipt{}}
	batch.RenderSHA256, err = bridgecodec.RenderHash(batch)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	kinds := make(chan ipc.Kind, 4)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			message, err := ipc.Read(conn)
			if err != nil {
				conn.Close()
				return
			}
			kinds <- message.Kind
			var payload []byte
			switch message.Kind {
			case ipc.KindBridgeIngest:
				var req contract.BridgeIngestRequest
				_ = json.Unmarshal(message.Payload, &req)
				raw, _ := json.Marshal(req.Event)
				sum := sha256.Sum256(raw)
				payload, _ = json.Marshal(contract.BridgeIngressAck{EventID: req.Event.EventID, PayloadHash: hex.EncodeToString(sum[:]), Committed: true})
			case ipc.KindBridgeClaim:
				payload, _ = json.Marshal(contract.BridgeClaimReply{Batch: batch, ClaimToken: claimToken})
			default:
				payload = []byte(`{}`)
			}
			_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Payload: payload})
			conn.Close()
		}
	}()
	var output, diag bytes.Buffer
	if err := Run(context.Background(), "claude", capPath, bytes.NewReader([]byte(`{}`)), &output, &diag); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || !strings.Contains(diag.String(), "bridge_public_secret") {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diag.String())
	}
	for i := 0; i < 2; i++ {
		select {
		case kind := <-kinds:
			if kind != ipc.KindBridgeIngest && kind != ipc.KindBridgeClaim {
				t.Fatalf("unexpected RPC %s", kind)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected Ingest and Claim RPC")
		}
	}
	select {
	case kind := <-kinds:
		t.Fatalf("extra RPC %s", kind)
	default:
	}
}

func TestHookDisconnectedCoordinatorRetainsExactObservation(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-offline-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	token := "hook-" + strings.Repeat("a", 24)
	rawCap, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: token})
	if err := os.WriteFile(capPath, rawCap, 0600); err != nil {
		t.Fatal(err)
	}
	providers.Lock()
	old, had := providers.byID["claude"]
	providers.byID["claude"] = func(_ context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "tool_observed", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "PostToolUse", ToolName: "Read", ToolStatus: "success"}}, nil
	}
	providers.Unlock()
	defer func() {
		providers.Lock()
		if had {
			providers.byID["claude"] = old
		} else {
			delete(providers.byID, "claude")
		}
		providers.Unlock()
	}()
	var output, diag bytes.Buffer
	if err := Run(context.Background(), "claude", capPath, bytes.NewReader([]byte(`{}`)), &output, &diag); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || !strings.Contains(diag.String(), "bridge_coordinator_unavailable") {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diag.String())
	}
	spool, err := openSpool(context.Background(), state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	records, err := spool.pending(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if records[0].Event.EventID == "" || records[0].Hash == "" || !json.Valid(records[0].Bytes) {
		t.Fatalf("record=%+v", records[0])
	}
	before := append([]byte(nil), records[0].Bytes...)
	replayed, err := spool.pending(context.Background())
	if err != nil || len(replayed) != 1 || !bytes.Equal(replayed[0].Bytes, before) || replayed[0].Event.EventID != records[0].Event.EventID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestHookNormalizerObeysSharedDeadline(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	rawCap, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: "hook-" + strings.Repeat("a", 24)})
	if err := os.WriteFile(capPath, rawCap, 0600); err != nil {
		t.Fatal(err)
	}
	provider := "b04-slow-normalizer"
	if err := RegisterProvider(provider, func(ctx context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return contract.BridgeHookEventV1{}, ctx.Err()
		}
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: provider, NativeSessionID: "session", Kind: "tool_observed", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "PostToolUse", ToolName: "Read", ToolStatus: "success"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { providers.Lock(); delete(providers.byID, provider); providers.Unlock() }()
	var output, diagnostic bytes.Buffer
	started := time.Now()
	if err := Run(context.Background(), provider, capPath, bytes.NewReader([]byte(`{}`)), &output, &diagnostic); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if elapsed > 2700*time.Millisecond {
		t.Fatalf("normalizer exceeded shared deadline: %v diagnostic=%q", elapsed, diagnostic.String())
	}
	if output.Len() != 0 || !strings.Contains(diagnostic.String(), "bridge_timeout") {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(state, "bridge-spool")); !os.IsNotExist(err) {
		t.Fatalf("late spool created: %v", err)
	}
}

func TestHookCancelledNormalizerCannotCreateLateSpoolOrPermit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	rawCap, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: "hook-" + strings.Repeat("a", 24)})
	if err := os.WriteFile(capPath, rawCap, 0600); err != nil {
		t.Fatal(err)
	}
	provider := "b04-cancel-normalizer"
	entered := make(chan struct{})
	release := make(chan struct{})
	if err := RegisterProvider(provider, func(_ context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		close(entered)
		<-release
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: provider, NativeSessionID: "session", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { providers.Lock(); delete(providers.byID, provider); providers.Unlock() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output, diagnostic bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, provider, capPath, bytes.NewReader([]byte(`{}`)), &output, &diagnostic)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("normalizer was not entered")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled normalizer did not return")
	}
	if output.Len() != 0 || !strings.Contains(diagnostic.String(), "bridge_cancelled") {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(state, "bridge-spool")); !os.IsNotExist(err) {
		t.Fatalf("late spool created: %v", err)
	}
}

func TestHookBlockedOutputLeavesBeginEmitWithoutOffered(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-output-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	token := "hook-" + strings.Repeat("a", 24)
	capRaw, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: token})
	if err := os.WriteFile(capPath, capRaw, 0600); err != nil {
		t.Fatal(err)
	}
	providers.Lock()
	old, had := providers.byID["claude"]
	providers.byID["claude"] = func(_ context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}, nil
	}
	providers.Unlock()
	defer func() {
		providers.Lock()
		if had {
			providers.byID["claude"] = old
		} else {
			delete(providers.byID, "claude")
		}
		providers.Unlock()
	}()
	batch := contract.BridgeBatch{SchemaVersion: 1, BatchID: "batch", AttemptID: "attempt-" + strings.Repeat("f", 24), BindingID: "binding", OwnerEpoch: 1, TransportMode: "hook_pull", Events: []contract.BridgeBatchEvent{{EventID: "result", EventRevision: 1, EventHash: strings.Repeat("b", 64), TaskID: "task", WorkRevision: 1, Kind: "result", Summary: "public"}}, DeliveryReceipts: []contract.BridgeDeliveryReceipt{}}
	batch.RenderSHA256, err = bridgecodec.RenderHash(batch)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	kinds := make(chan ipc.Kind, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			message, err := ipc.Read(conn)
			if err != nil {
				conn.Close()
				return
			}
			kinds <- message.Kind
			var payload []byte
			switch message.Kind {
			case ipc.KindBridgeIngest:
				var req contract.BridgeIngestRequest
				_ = json.Unmarshal(message.Payload, &req)
				raw, _ := json.Marshal(req.Event)
				sum := sha256.Sum256(raw)
				payload, _ = json.Marshal(contract.BridgeIngressAck{EventID: req.Event.EventID, PayloadHash: hex.EncodeToString(sum[:]), Committed: true})
			case ipc.KindBridgeClaim:
				payload, _ = json.Marshal(contract.BridgeClaimReply{Batch: batch, ClaimToken: "bridge-claim-" + strings.Repeat("c", 64)})
			case ipc.KindBridgeBeginEmit:
				payload = []byte(`{"status":"committed"}`)
			default:
				payload = []byte(`{}`)
			}
			_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Payload: payload})
			conn.Close()
		}
	}()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	fill, err := openBoundedFD(context.Background(), writeEnd, false)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := unix.Write(fill.fd, bytes.Repeat([]byte("x"), 4096))
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := fill.Close(); err != nil {
		t.Fatal(err)
	}
	var diag bytes.Buffer
	started := time.Now()
	if err := Run(context.Background(), "claude", capPath, bytes.NewReader([]byte(`{}`)), writeEnd, &diag); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 1500*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("elapsed=%v", elapsed)
	}
	if !strings.Contains(diag.String(), "bridge_timeout") && !strings.Contains(diag.String(), "bridge_output_incomplete") {
		t.Fatalf("diagnostic=%q", diag.String())
	}
	listener.Close()
	<-done
	var got []ipc.Kind
	for len(kinds) > 0 {
		got = append(got, <-kinds)
	}
	if len(got) != 3 || got[0] != ipc.KindBridgeIngest || got[1] != ipc.KindBridgeClaim || got[2] != ipc.KindBridgeBeginEmit {
		t.Fatalf("RPC sequence=%v", got)
	}
}

func TestHookFullStdoutHelper(t *testing.T) {
	if os.Getenv("B04_FULL_STDOUT_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := openBoundedFD(ctx, os.Stdout, false)
	if err != nil {
		os.Exit(3)
	}
	defer b.Close()
	fill := bytes.Repeat([]byte("x"), 4096)
	for {
		_, err := unix.Write(b.fd, fill)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			break
		}
		if err != nil {
			os.Exit(4)
		}
	}
	_, err = b.Write([]byte(`{"hookSpecificOutput":"wire"}`))
	fmt.Fprintf(os.Stderr, "B04_RETURN_NS=%d\n", time.Now().UnixNano())
	if err != context.DeadlineExceeded && err != context.Canceled {
		os.Exit(5)
	}
	os.Exit(0)
}

func TestHookInheritedFullStdoutPipeStopsAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHookFullStdoutHelper$")
	cmd.Env = append(os.Environ(), "B04_FULL_STDOUT_HELPER=1")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	returned, err := hookReturnElapsed(started, stderr.String())
	if err != nil || returned < 1500*time.Millisecond || returned > 3*time.Second || elapsed > 3500*time.Millisecond {
		t.Fatalf("run_return=%v process_exit=%v stderr=%q err=%v", returned, elapsed, stderr.String(), err)
	}
	t.Logf("run_return=%v process_exit=%v", returned, elapsed)
}

func hookReturnElapsed(started time.Time, diagnostic string) (time.Duration, error) {
	i := strings.LastIndex(diagnostic, "B04_RETURN_NS=")
	if i < 0 {
		return 0, fmt.Errorf("missing return marker")
	}
	line := strings.SplitN(diagnostic[i+len("B04_RETURN_NS="):], "\n", 2)[0]
	ns, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Unix(0, ns).Sub(started), nil
}

func TestHookInheritedPipeHelper(t *testing.T) {
	if os.Getenv("B04_HOOK_HELPER") != "1" {
		return
	}
	_ = RegisterProvider("claude", func(_ context.Context, _ []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}, nil
	})
	_ = Run(context.Background(), "claude", os.Getenv("B04_BINDING_FILE"), os.Stdin, os.Stdout, os.Stderr)
	fmt.Fprintf(os.Stderr, "B04_RETURN_NS=%d\n", time.Now().UnixNano())
	os.Exit(0)
}

func TestHookInheritedStdinWithoutEOFStopsAtApplicationDeadline(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-stdin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	capRaw, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: "hook-" + strings.Repeat("a", 24)})
	if err := os.WriteFile(capPath, capRaw, 0600); err != nil {
		t.Fatal(err)
	}
	limitCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(limitCtx, os.Args[0], "-test.run=^TestHookInheritedPipeHelper$")
	cmd.Env = append(os.Environ(), "B04_HOOK_HELPER=1", "B04_BINDING_FILE="+capPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	returned, err := hookReturnElapsed(started, stderr.String())
	if err != nil || returned > 3*time.Second || returned < 1500*time.Millisecond || elapsed > 3500*time.Millisecond {
		t.Fatalf("run_return=%v process_exit=%v stderr=%q err=%v", returned, elapsed, stderr.String(), err)
	}
	t.Logf("run_return=%v process_exit=%v", returned, elapsed)
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "bridge_timeout") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHookObservationUsesRealSocketAndClearsOnlyCommittedSpool(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-hook-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := process.ExecutablePath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.CreateOwnerGrant(ctx, "owner", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: "collect", ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: grant.ID, HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	token := "hook-" + strings.Repeat("a", 24)
	hash := sha256.Sum256([]byte(token))
	_, err = db.SQL().Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('binding','run','claude','session',?,?,?,?,?,?,1,'owner','hook_pull',?,'ACTIVE',?,?,'bind',?,?)`, grant.ID, os.Getpid(), birth, os.Getpid(), birth, digest, strings.Repeat("b", 64), hex.EncodeToString(hash[:]), store.BridgeCapabilityPath(state, "run"), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "run")
	if err := os.Mkdir(filepath.Dir(capPath), 0700); err != nil {
		t.Fatal(err)
	}
	capRaw, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, HookCapability: token})
	if err := os.WriteFile(capPath, capRaw, 0600); err != nil {
		t.Fatal(err)
	}
	ownerDir := filepath.Join(state, "owners")
	if err := os.Mkdir(ownerDir, 0700); err != nil {
		t.Fatal(err)
	}
	ownerFile := filepath.Join(ownerDir, grant.ID+".json")
	grantRaw, _ := json.Marshal(grant)
	if err := os.WriteFile(ownerFile, grantRaw, 0600); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(state, "control")
	if err := os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	controlFile := filepath.Join(controlDir, "run.json")
	controlRaw, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": "owner", "control_token": receipt.ControlToken, "bootstrap_path": filepath.Join(state, "bootstrap.json"), "host_launch_id": receipt.LaunchID, "host_launch_token": receipt.LaunchToken})
	if err := os.WriteFile(controlFile, controlRaw, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = server.Serve(serveCtx) }()
	if err := RegisterProvider("claude", func(_ context.Context, raw []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
		if bytes.Equal(raw, []byte("emit")) {
			return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}, nil
		}
		return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session", Kind: "tool_observed", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "PostToolUse", ToolName: "Read", ToolStatus: "success"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var output, diag bytes.Buffer
	if err := Run(ctx, "claude", capPath, bytes.NewReader([]byte(`{"ignored":"raw"}`)), &output, &diag); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || diag.Len() != 0 {
		t.Fatalf("stdout=%q diag=%q", output.String(), diag.String())
	}
	check, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var ingress, actions int
	if err := check.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_ingress WHERE binding_id='binding'`).Scan(&ingress); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT COUNT(*) FROM review_decisions`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if ingress != 1 || actions != 0 {
		t.Fatalf("ingress=%d business=%d", ingress, actions)
	}
	spool, err := openSpool(ctx, state, "binding")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := spool.pending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%d err=%v", len(pending), err)
	}
	var revision int
	if err := check.SQL().QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='task'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	result := contract.Event{Version: 1, EventID: "result-1", RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: revision, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("c", 64)}
	body, _ := json.Marshal(result)
	_, err = check.SQL().Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'action-1','pending',?)`, result.EventID, launch.HostID, 1, "task", launch.AttemptID, launch.SegmentID, result.PayloadHash, string(body), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := check.SQL().Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES('result-1','task')`); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diag.Reset()
	if err := Run(ctx, "claude", capPath, bytes.NewReader([]byte("emit")), &output, &diag); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(output.Bytes()) || diag.Len() != 0 || bytes.Contains(output.Bytes(), []byte(token)) {
		t.Fatalf("wire=%q diagnostic=%q", output.String(), diag.String())
	}
	var attemptState, deliveryState string
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts ORDER BY rowid DESC LIMIT 1`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&deliveryState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "OFFERED" || deliveryState != "pending" {
		t.Fatalf("attempt=%s business=%s", attemptState, deliveryState)
	}
	var batchID, attemptID, renderHash string
	if err := check.SQL().QueryRow(`SELECT a.batch_id,a.attempt_id,b.render_sha256 FROM bridge_delivery_attempts a JOIN bridge_batches b ON b.batch_id=a.batch_id ORDER BY a.rowid DESC LIMIT 1`).Scan(&batchID, &attemptID, &renderHash); err != nil {
		t.Fatal(err)
	}
	ownerReq := contract.BridgeReceiveRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, RequestID: "owner-query"}, OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}, BatchID: batchID, AttemptID: attemptID, RenderSHA256: renderHash}
	ownerRaw, _ := json.Marshal(ownerReq)
	query, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindBridgeQueryBatch, RequestID: "query", Payload: ownerRaw})
	if err != nil || query.Kind != ipc.KindResponse {
		t.Fatalf("query=%s %s %v", query.Kind, query.Payload, err)
	}
	var saved contract.BridgeBatch
	if err := json.Unmarshal(query.Payload, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.BatchID != batchID || saved.AttemptID != attemptID || len(saved.Events) != 1 || saved.Events[0].EventID != "result-1" {
		t.Fatalf("saved batch=%+v", saved)
	}
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, attemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "OFFERED" {
		t.Fatalf("query mutated state=%s", attemptState)
	}
	ownerReq.RequestID = "owner-received"
	ownerRaw, _ = json.Marshal(ownerReq)
	received, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindBridgeReceived, RequestID: "received", Payload: ownerRaw})
	if err != nil || received.Kind != ipc.KindResponse {
		t.Fatalf("received=%s %s %v", received.Kind, received.Payload, err)
	}
	var ack contract.BridgeReceiveReceipt
	if err := json.Unmarshal(received.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.BatchID != batchID || ack.AttemptID != attemptID {
		t.Fatalf("receipt=%+v", ack)
	}
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, attemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&deliveryState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECEIVED" || deliveryState != "pending" {
		t.Fatalf("received state=%s business=%s", attemptState, deliveryState)
	}
}
