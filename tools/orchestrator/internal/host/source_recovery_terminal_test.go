package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func c02SourceIdentity(t *testing.T) contract.HostHello {
	t.Helper()
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return contract.HostHello{LaunchID: "c02-launch", LaunchToken: "secret", OriginContextID: "origin", HostGeneration: "generation", OriginPID: os.Getpid(), OriginBirth: birth, PID: os.Getpid(), Birth: birth, Executable: executable}
}
func c02SourceSocket(t *testing.T) (string, net.Listener) {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "c02-source-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "coordinator.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return socket, listener
}

func TestC02SourceTerminalAuthenticationFailureDoesNotRecover(t *testing.T) {
	socket, listener := c02SourceSocket(t)
	peerDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		frame, err := ipc.Read(conn)
		if err != nil {
			peerDone <- err
			return
		}
		peerDone <- ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindError, RequestID: frame.RequestID, Epoch: 1, Payload: json.RawMessage(`{"error":"host_rejected"}`)})
	}()
	var ensure atomic.Int32
	err := RunSource(context.Background(), SourceConfig{SocketPath: socket, SpoolRoot: filepath.Join(t.TempDir(), "spool"), Hello: c02SourceIdentity(t), EnsureCoordinator: func(context.Context) error { ensure.Add(1); return nil }, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		t.Error("auth rejection launched work")
		return nil, errors.New("unexpected_launch")
	}})
	if err == nil || err.Error() != "host_authentication_rejected" || ensure.Load() != 0 {
		t.Fatalf("terminal auth recovered: %v calls=%d", err, ensure.Load())
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

func TestC02SourceExhaustedRecoveryPreservesSpoolAndCannotReset(t *testing.T) {
	hello := c02SourceIdentity(t)
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, hello.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	attempt := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	spool, err := h.spool(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.appendPhaseEvent(spool, "run", "task", attempt, launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}, contract.EventPrepared, hashText("prepared"), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "attempt", "segment", "event-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("spool=%v %v", files, err)
	}
	original, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	scope, err := sourceRecoveryScope(hello)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openHostRecovery(root, scope, hello.PID, hello.Birth)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := state.reserve(time.UnixMilli(100000 + int64(i)*100000)); err != nil {
			t.Fatal(err)
		}
		if err := state.attemptStarted(); err != nil {
			t.Fatal(err)
		}
	}
	state.close()
	var ensure atomic.Int32
	cfg := SourceConfig{SocketPath: filepath.Join(root, "absent.sock"), SpoolRoot: root, Hello: hello, EnsureCoordinator: func(context.Context) error { ensure.Add(1); return nil }, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		return nil, errors.New("unexpected_launch")
	}}
	for i := 0; i < 2; i++ {
		if err := RunSource(context.Background(), cfg); !errors.Is(err, errCoordinatorUnavailable) {
			t.Fatalf("restart %d reset exhausted ledger: %v", i, err)
		}
	}
	if ensure.Load() != 0 {
		t.Fatal("exhausted Source started a coordinator")
	}
	current, err := os.ReadFile(files[0])
	if err != nil || string(current) != string(original) {
		t.Fatalf("exhaustion changed original unacknowledged event: %v", err)
	}
}

func TestC02SourceOfflineWorkerHardDeadlineContinuesDuringBackoff(t *testing.T) {
	socket, listener := c02SourceSocket(t)
	hello := c02SourceIdentity(t)
	root := filepath.Join(t.TempDir(), "spool")
	peerDone := make(chan error, 1)
	workerPID := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		frame, err := ipc.Read(conn)
		if err != nil {
			peerDone <- err
			return
		}
		ready, _ := json.Marshal(contract.HostReady{HostID: hello.LaunchID, CoordinatorEpoch: 1})
		if err := ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: frame.RequestID, Epoch: 1, Payload: ready}); err != nil {
			peerDone <- err
			return
		}
		if frame, err := ipc.Read(conn); err != nil || frame.Kind != ipc.KindHostReconciled {
			peerDone <- errors.New("reconcile_missing")
			return
		}
		grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", GrantedActiveMS: 300}
		body, _ := json.Marshal(grant)
		if err := ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: 1, Payload: body}); err != nil {
			peerDone <- err
			return
		}
		for i := 0; i < 3; i++ {
			frame, err := ipc.Read(conn)
			if err != nil {
				peerDone <- err
				return
			}
			var event contract.Event
			if err := json.Unmarshal(frame.Payload, &event); err != nil {
				peerDone <- err
				return
			}
			if event.Kind == contract.EventSpawned {
				workerPID <- event.Process.PID
			}
			ack, _ := json.Marshal(contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "durable"})
			if err := ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: 1, Payload: ack}); err != nil {
				peerDone <- err
				return
			}
		}
		peerDone <- nil // Transport loss while the 300ms original budget continues.
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ensure atomic.Int32
	sourceDone := make(chan error, 1)
	go func() {
		sourceDone <- RunSource(ctx, SourceConfig{SocketPath: socket, SpoolRoot: root, Hello: hello, EnsureCoordinator: func(context.Context) error { ensure.Add(1); return nil }, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
			return launchInvocation{args: []string{"/bin/sleep", "30"}, env: map[string]string{}}, nil
		}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-sourceDone:
		case <-time.After(12 * time.Second):
			t.Error("Source failed to stop")
		}
	})
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
	pid := <-workerPID
	spool, err := events.Open(filepath.Join(root, "attempt", "segment"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(800 * time.Millisecond)
	exited := false
	for time.Now().Before(deadline) {
		records, err := spool.Read()
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range records {
			if event.Kind == contract.EventExited {
				exited = true
			}
		}
		if exited {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !exited || syscall.Kill(pid, 0) == nil {
		t.Fatalf("offline worker outlived its original deadline: exited=%v pid=%d", exited, pid)
	}
	if ensure.Load() != 0 {
		t.Fatal("deadline test performed recovery before its persisted 1-second backoff")
	}
	cancel()
	if err := <-sourceDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sourceDone <- context.Canceled
}
