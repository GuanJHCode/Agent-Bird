package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ownerRuntimeFixture(t *testing.T) (*Server, contract.HostHello) {
	s, hello, _ := ownerRuntimeFixtureWithCancel(t)
	return s, hello
}

func ownerRuntimeFixtureWithCancel(t *testing.T) (*Server, contract.HostHello, context.CancelFunc) {
	t.Helper()
	s, err := NewServer(ownerKeyTestState(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = s.Close() })
	go func() { _ = s.Serve(ctx) }()
	pid, birth, executable := testHostIdentity(t)
	r, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "owner-runtime-run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth}, Host: store.HostLaunchSpec{ExecutionOwnerMode: "supervised-v1", OriginContextID: "origin", HostGeneration: "gen", Executable: executable}, Tasks: []store.TaskSpec{{ID: "owner-runtime-task", RunID: "owner-runtime-run", MaxAttempts: 1, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}, {ID: "owner-runtime-dependent", RunID: "owner-runtime-run", Dependencies: []string{"owner-runtime-task"}, MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "gen", PID: pid, Birth: birth, Executable: executable}, cancel
}

func TestOriginalOwnerSupervisedHostDoesNotExposeLegacyReady(t *testing.T) {
	s, hello := ownerRuntimeFixture(t)
	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	body, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "first-transport", Payload: body}); err != nil {
		t.Fatal(err)
	}
	got, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ipc.Kind("owner_host_pending") {
		t.Fatalf("supervised transport exposed readiness before owner: %s", got.Kind)
	}
	if _, err = s.db.ClaimReady(context.Background(), hello.LaunchID, s.epoch, 1); err != store.ErrHostRejected {
		t.Fatalf("pending transport dispatched: %v", err)
	}
}

func TestOriginalOwnerEscrowPrecedesOwnerAllocation(t *testing.T) {
	state := ownerKeyTestState(t)
	keys, err := loadOriginalOwnerKeys(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := ownerKeyTestScope()
	scope.OwnerID, scope.Incarnation = "", 0
	if err = keys.saveLaunchCredential(state, scope, "launch-secret"); err != nil {
		t.Fatalf("cannot escrow before Prepare allocates owner: %v", err)
	}
	scope.OwnerID, scope.Incarnation = "allocated-owner", 1
	if got, err := keys.readLaunchCredential(state, scope); err != nil || got != "launch-secret" {
		t.Fatalf("allocated identity changed immutable launch escrow: %q %v", got, err)
	}
}

func TestOriginalOwnerInvalidPrepareDoesNotWriteCredential(t *testing.T) {
	s, hello := ownerRuntimeFixture(t)
	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	body, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "pending", Payload: body}); err != nil {
		t.Fatal(err)
	}
	if _, err = ipc.Read(conn); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "invalid", BootstrapTokenSHA256: "invalid-hash", StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "bad-prepare", Epoch: s.epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	response, err := ipc.Read(conn)
	if err != nil || response.Kind != ipc.KindError {
		t.Fatal("invalid Prepare accepted", err)
	}
	credentials, err := filepath.Glob(filepath.Join(s.stateDir, "owner-runtime", "launch-*.json"))
	if err != nil || len(credentials) != 0 {
		t.Fatal("invalid Prepare published credential before validation", credentials, err)
	}
	if _, err = s.db.CurrentOriginalOwnerForLaunch(context.Background(), hello.LaunchID); err != store.ErrNotFound {
		t.Fatal("invalid Prepare allocated owner", err)
	}
}
