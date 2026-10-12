package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func newACPRPCServer(t *testing.T) *Server {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "acp-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Only transport handlers run: this fixture does not start scheduler actors.
	go func() {
		defer close(done)
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.handleConnection(ctx, conn)
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("RPC accept loop did not exit")
		}
	})
	return s
}
func TestACPBridgeKindsRejectMalformedPayloadThroughRealSocket(t *testing.T) {
	s := newACPRPCServer(t)
	for _, kind := range []ipc.Kind{"acp_bridge_bind", "acp_bridge_activate", "acp_bridge_claim", "acp_bridge_begin_emit", "acp_bridge_received"} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reply, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: "malformed", Payload: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			var failure ErrorResponse
			if err = json.Unmarshal(reply.Payload, &failure); err != nil {
				t.Fatal(err)
			}
			if reply.Kind != ipc.KindError || failure.Error != "bridge_invalid_input" {
				t.Fatalf("%s response=%s error=%s", kind, reply.Kind, failure.Error)
			}
			if bridgeHookRole(kind) {
				t.Fatal("ACP inherited Hook role timeout")
			}
		})
	}
}

type acpRPCFixture struct {
	s                      *Server
	ownerFile, controlFile string
	bind                   contract.ACPBindRequest
	stop                   func()
}

func newACPRPCFixture(t *testing.T) acpRPCFixture {
	t.Helper()
	s := newACPRPCServer(t)
	f := acpRPCFixture{s: s}
	ctx := context.Background()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.CreateOwnerGrant(ctx, "controller", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth, DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: grant.ID, HostGeneration: "fixture-gen", Executable: "/private/fixture-host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"owners", "control"} {
		if err = os.Mkdir(filepath.Join(s.stateDir, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.ownerFile = filepath.Join(s.stateDir, "owners", grant.ID+".json")
	f.controlFile = filepath.Join(s.stateDir, "control", "run.json")
	ownerRaw, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.ownerFile, ownerRaw, 0600); err != nil {
		t.Fatal(err)
	}
	controlRaw, err := json.Marshal(bridgeControlCapability{Version: 2, RunID: "run", ControllerThread: "controller", ControlToken: receipt.ControlToken, BootstrapPath: filepath.Join(s.stateDir, "host-bootstrap", "fixture.json"), HostLaunchID: receipt.LaunchID, HostLaunchToken: receipt.LaunchToken})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.controlFile, controlRaw, 0600); err != nil {
		t.Fatal(err)
	}
	host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "fixture-gen", PID: 999999999, Birth: "fixture-host", Executable: "/private/fixture-host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := s.db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, EventID: "result-1", RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("b", 64)}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,1,'task',?,?,?, ?,1,'slot','pending','now')`, event.EventID, host, launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES('result-1','task')`); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/cat")
	child.Env = []string{}
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	f.stop = func() {
		once.Do(func() {
			_ = in.Close()
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case e := <-done:
				if e != nil {
					t.Errorf("original fixture child Wait: %v", e)
				}
			case <-time.After(3 * time.Second):
				_ = child.Process.Kill()
				<-done
				t.Error("fixture child did not stop")
			}
		})
	}
	t.Cleanup(f.stop)
	nativeBirth, err := process.Birth(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := process.ExecutablePath(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	f.bind = contract.ACPBindRequest{SchemaVersion: 1, RequestID: "bind", RunID: "run", ProviderID: "grok", NativePID: child.Process.Pid, NativeBirth: nativeBirth, NativeExecutableSHA256: digest, TransportMode: contract.BridgeTransportGrokACP, OwnerMode: "local", OwnerCapabilityFile: f.ownerFile, ControlFile: f.controlFile, ManifestSHA256: contract.GrokACPManifestSHA256()}
	return f
}
func acpRPCEnvelope(t *testing.T, s *Server, kind ipc.Kind, req any) ipc.Envelope {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: "rpc-fixture", Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	return reply
}
func acpRPCSuccess(t *testing.T, s *Server, kind ipc.Kind, req, out any) {
	t.Helper()
	reply := acpRPCEnvelope(t, s, kind, req)
	if reply.Kind != ipc.KindResponse {
		t.Fatalf("%s failed: %s", kind, reply.Payload)
	}
	if out != nil {
		if err := json.Unmarshal(reply.Payload, out); err != nil {
			t.Fatal(err)
		}
	}
}
func TestACPBridgeFullFlowThroughOriginalUnixPeer(t *testing.T) {
	f := newACPRPCFixture(t)
	var binding contract.BridgeBindingView
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeBind, f.bind, &binding)
	if binding.CapabilityFile != "" || binding.Status != "PENDING_SESSION" {
		t.Fatalf("unexpected binding=%+v", binding)
	}
	owner := contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: binding.BindingID, OwnerEpoch: 1, RequestID: "claim"}, OwnerMode: "local", OwnerCapabilityFile: f.ownerFile, ControlFile: f.controlFile}
	activate := contract.ACPActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: "rpc-session"}
	activate.RequestID = contract.ACPActivateRequestID(activate)
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeActivate, activate, &binding)
	var claim contract.ACPClaimReply
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeClaim, contract.ACPClaimRequest{BridgeOwnerRequest: owner}, &claim)
	if len(claim.Batch.Events) != 1 || len(claim.Batch.DeliveryReceipts) != 1 || claim.NativeSessionID != "rpc-session" || claim.TurnOrdinal != 1 {
		t.Fatalf("claim=%+v", claim)
	}
	var replay contract.ACPClaimReply
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeClaim, contract.ACPClaimRequest{BridgeOwnerRequest: owner}, &replay)
	a, _ := json.Marshal(claim)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatal("claim ACK-loss replay changed")
	}
	begin := contract.ACPBeginRequest{BridgeOwnerRequest: owner, NativeSessionID: claim.NativeSessionID, BatchID: claim.Batch.BatchID, AttemptID: claim.Batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: claim.Batch.RenderSHA256, PromptSHA256: claim.PromptSHA256, TurnOrdinal: claim.TurnOrdinal}
	begin.RequestID = contract.ACPBeginRequestID(begin)
	receive := contract.ACPReceiveRequest{ACPBeginRequest: begin, RPCRequestID: 4, ResponseSHA256: strings.Repeat("c", 64), StopReason: "end_turn"}
	receive.RequestID = contract.ACPReceiveRequestID(receive)
	early := acpRPCEnvelope(t, f.s, ipc.KindACPBridgeReceived, receive)
	if early.Kind != ipc.KindError {
		t.Fatal("Receive committed before Begin")
	}
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeBeginEmit, begin, nil)
	var receipt contract.BridgeReceiveReceipt
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeReceived, receive, &receipt)
	f.stop()
	var again contract.BridgeReceiveReceipt
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeReceived, receive, &again)
	if receipt != again {
		t.Fatal("committed receipt replay changed after child exit")
	}
	var eventState string
	var decisions int
	if err := f.s.db.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&eventState); err != nil || eventState != "pending" {
		t.Fatalf("business event state=%s err=%v", eventState, err)
	}
	if err := f.s.db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_decisions`).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("business decisions=%d err=%v", decisions, err)
	}
}
func TestACPRPCChild(t *testing.T) {
	if os.Getenv("ACP_RPC_CHILD") != "1" {
		t.Skip("owned child helper")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil || len(raw) > 65536 {
		os.Exit(2)
	}
	var envelope ipc.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := ipc.Call(ctx, os.Getenv("ACP_RPC_SOCKET"), envelope)
	if err != nil {
		os.Exit(4)
	}
	if json.NewEncoder(os.Stdout).Encode(reply) != nil {
		os.Exit(5)
	}
	os.Exit(0)
}
func TestACPBridgeOwnerCapabilityDoesNotAuthorizeDescendant(t *testing.T) {
	f := newACPRPCFixture(t)
	raw, err := json.Marshal(f.bind)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindACPBridgeBind, RequestID: "descendant", Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	reply := acpRPCDescendant(t, f.s, envelope)
	var failure ErrorResponse
	if err = json.Unmarshal(reply.Payload, &failure); err != nil {
		t.Fatal(err)
	}
	if reply.Kind != ipc.KindError || failure.Error != "owner_peer_mismatch" {
		t.Fatalf("descendant response=%s code=%s", reply.Kind, failure.Error)
	}
	var rows int
	if err = f.s.db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_bindings`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("descendant binding rows=%d err=%v", rows, err)
	}
	// The original process still succeeds with the exact same capability and tuple.
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeBind, f.bind, nil)
	forged := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"peer":{"pid":1}}`)...)
	response := acpRPCEnvelope(t, f.s, ipc.KindACPBridgeBind, json.RawMessage(forged))
	if response.Kind != ipc.KindError {
		t.Fatal("JSON peer accepted")
	}
	if err = json.Unmarshal(response.Payload, &failure); err != nil || failure.Error != "bridge_invalid_input" {
		t.Fatalf("forged peer=%s err=%v", failure.Error, err)
	}
}

func acpRPCDescendant(t *testing.T, s *Server, envelope []byte) ipc.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestACPRPCChild$")
	child.Env = []string{"ACP_RPC_CHILD=1", "ACP_RPC_SOCKET=" + s.SocketPath()}
	child.Stdin = strings.NewReader(string(envelope))
	out, err := child.Output()
	if err != nil {
		t.Fatalf("descendant original Wait: %v", err)
	}
	var reply ipc.Envelope
	if err = json.Unmarshal(out, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func TestACPBridgeEveryBoundRPCRejectsDescendantAndPrivateFileErrors(t *testing.T) {
	f := newACPRPCFixture(t)
	var binding contract.BridgeBindingView
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeBind, f.bind, &binding)
	owner := contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: binding.BindingID, OwnerEpoch: 1, RequestID: "claim"}, OwnerMode: "local", OwnerCapabilityFile: f.ownerFile, ControlFile: f.controlFile}
	activate := contract.ACPActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: "rpc-session"}
	activate.RequestID = contract.ACPActivateRequestID(activate)
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeActivate, activate, nil)
	var claim contract.ACPClaimReply
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeClaim, contract.ACPClaimRequest{BridgeOwnerRequest: owner}, &claim)
	begin := contract.ACPBeginRequest{BridgeOwnerRequest: owner, NativeSessionID: claim.NativeSessionID, BatchID: claim.Batch.BatchID, AttemptID: claim.Batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: claim.Batch.RenderSHA256, PromptSHA256: claim.PromptSHA256, TurnOrdinal: claim.TurnOrdinal}
	begin.RequestID = contract.ACPBeginRequestID(begin)
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeBeginEmit, begin, nil)
	receive := contract.ACPReceiveRequest{ACPBeginRequest: begin, RPCRequestID: 4, ResponseSHA256: strings.Repeat("c", 64), StopReason: "end_turn"}
	receive.RequestID = contract.ACPReceiveRequestID(receive)
	entries := map[ipc.Kind]any{ipc.KindACPBridgeActivate: activate, ipc.KindACPBridgeClaim: contract.ACPClaimRequest{BridgeOwnerRequest: owner}, ipc.KindACPBridgeBeginEmit: begin, ipc.KindACPBridgeReceived: receive}
	for kind, req := range entries {
		t.Run(string(kind), func(t *testing.T) {
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: "child", Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			reply := acpRPCDescendant(t, f.s, envelope)
			var failure ErrorResponse
			if err = json.Unmarshal(reply.Payload, &failure); err != nil {
				t.Fatal(err)
			}
			if reply.Kind != ipc.KindError || failure.Error != "owner_peer_mismatch" {
				t.Fatalf("descendant code=%s", failure.Error)
			}
			// Change only the private proof path; canonical mutation ID remains valid.
			bad := strings.Replace(string(raw), f.controlFile, f.controlFile+"-missing", 1)
			reply = acpRPCEnvelope(t, f.s, kind, json.RawMessage(bad))
			if err = json.Unmarshal(reply.Payload, &failure); err != nil {
				t.Fatal(err)
			}
			if reply.Kind != ipc.KindError || failure.Error != "control_file_invalid" || strings.Contains(string(reply.Payload), f.s.stateDir) || strings.Contains(string(reply.Payload), claim.ClaimToken) {
				t.Fatalf("private file error was not stable: %s", reply.Payload)
			}
		})
	}
	var state string
	var count int
	if err := f.s.db.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, begin.AttemptID).Scan(&state); err != nil || state != "EMITTING" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	if err := f.s.db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_delivery_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attempts=%d err=%v", count, err)
	}
	acpRPCSuccess(t, f.s, ipc.KindACPBridgeReceived, receive, nil)
}
func TestACPBridgeSocketCancelReleasesAdmissionWhileStoreIsLocked(t *testing.T) {
	f := newACPRPCFixture(t)
	hold, err := f.s.db.SQL().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	raw, err := json.Marshal(f.bind)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", f.s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindACPBridgeBind, RequestID: "cancel", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(f.s.bridgeAdmission) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(f.s.bridgeAdmission) != 1 {
		t.Fatal("ACP request was never admitted")
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	active := 1
	for time.Now().Before(deadline) {
		f.s.bridgeMu.Lock()
		active = len(f.s.bridgeConnections)
		f.s.bridgeMu.Unlock()
		if len(f.s.bridgeAdmission) == 0 && active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(f.s.bridgeAdmission) != 0 || active != 0 {
		t.Fatalf("cancel failed while SQL transaction remains held: admission=%d connections=%d", len(f.s.bridgeAdmission), active)
	}
	if err = hold.Rollback(); err != nil {
		t.Fatal(err)
	}
	var bindings int
	if err = f.s.db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_bindings`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("late Bind after cancellation: rows=%d err=%v", bindings, err)
	}
}
