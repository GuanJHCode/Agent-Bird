package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestC02AttachedHostLostRetireNoticeUsesEnsureAndHelloFence(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "c02-retire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	server, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close(); cancel(); <-done })
	pid, birth, executable := testHostIdentity(t)
	plan, err := server.db.SubmitPlan(context.Background(), store.PlanSpec{Run: store.RunSpec{ID: "retire-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: executable}, Tasks: []store.TaskSpec{{ID: "retire-task", RunID: "retire-run", MaxAttempts: 1, MaxActiveMS: 1500, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: pid, Birth: birth, Executable: executable}
	barrier := c02StartWireBarrier(t, root, server.SocketPath(), false)
	var ensure, invocations atomic.Int32
	sourceCtx, cancelSource := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSource()
	sourceDone := make(chan error, 1)
	go func() {
		sourceDone <- host.RunSource(sourceCtx, host.SourceConfig{SocketPath: barrier.socket, SpoolRoot: filepath.Join(root, "spool"), Hello: hello, EnsureCoordinator: func(ctx context.Context) error {
			ensure.Add(1)
			if err := CheckRecoveryAllowed(state, hello); err != nil {
				return err
			}
			response, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "existing-server", Payload: json.RawMessage(`{}`)})
			if err != nil {
				return err
			}
			if response.Kind != ipc.KindResponse {
				return errors.New("coordinator_not_ready")
			}
			return nil
		}, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
			invocations.Add(1)
			return c02ReportWorkerInvocation{args: []string{"/usr/bin/true"}}, nil
		}})
	}()
	select {
	case <-barrier.lost:
	case <-time.After(3 * time.Second):
		t.Fatal("attached Host terminal notice loss not reached")
	}
	select {
	case err := <-sourceDone:
		if err != nil {
			t.Fatalf("released same Host failed terminal Hello fence: %v", err)
		}
	case <-sourceCtx.Done():
		t.Fatal("lost retirement notice entered respawn loop")
	}
	if ensure.Load() != 1 || invocations.Load() != 1 {
		t.Fatalf("retirement respawned/relaunched: ensure=%d invocation=%d", ensure.Load(), invocations.Load())
	}
	var status string
	var attempts, segments, exits int
	if err := server.db.SQL().QueryRow(`SELECT l.status,(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='exited') FROM host_launches l WHERE l.id=?`, plan.LaunchID).Scan(&status, &attempts, &segments, &exits); err != nil || status != "released" || attempts != 1 || segments != 1 || exits != 1 {
		t.Fatalf("old released Host revived: %s %d/%d/%d %v", status, attempts, segments, exits, err)
	}
	t.Log("attached Host dropped disposition -> EOF -> Ensure1 -> same Hello -> authenticated retired fence; no new worker/Host")
}
