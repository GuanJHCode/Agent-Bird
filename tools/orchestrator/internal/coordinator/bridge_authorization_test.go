package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestBridgeControlReaderRejectsNoWriterFIFOWithoutBlocking(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "control")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{stateDir: state}
	done := make(chan error, 1)
	go func() { _, err := s.readBridgeControl(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO control accepted")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("FIFO control open blocked without writer")
	}
}

func TestBridgeOwnerAuthorizationUsesRealUnixPeerAndRunControl(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b02-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.db.CreateOwnerGrant(ctx, "controller", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	ownerDir := filepath.Join(state, "owners")
	if err = os.Mkdir(ownerDir, 0700); err != nil {
		t.Fatal(err)
	}
	ownerFile := filepath.Join(ownerDir, grant.ID+".json")
	ownerBytes, _ := json.Marshal(grant)
	if err = os.WriteFile(ownerFile, ownerBytes, 0600); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(state, "control")
	if err = os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	controlFile := filepath.Join(controlDir, "run.json")
	controlBytes, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": "controller", "control_token": "control", "bootstrap_path": filepath.Join(state, "host-bootstrap", "x.json"), "host_launch_id": "launch", "host_launch_token": "token"})
	if err = os.WriteFile(controlFile, controlBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES(?,?,?,?,?,?,'queued',?,'collect')`, "run", "controller", 1, grant.ID, os.Getpid(), birth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO run_control(run_id,token_hash) VALUES('run',?)`, bridgeTestHash("control")); err != nil {
		t.Fatal(err)
	}
	hookListener, err := net.Listen("unix", filepath.Join(root, "hook.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hookListener.Close()
	preSpawnPath := store.BridgeCapabilityPath(state, "run")
	native := exec.Command(os.Args[0], "-test.run=^TestBridgeSocketChild$")
	native.Env = append(os.Environ(), "B02_CHILD_SOCKET="+filepath.Join(root, "hook.sock"), "AGENT_BIRD_BRIDGE_BINDING="+preSpawnPath)
	if err = native.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = native.Process.Kill(); _, _ = native.Process.Wait() }()
	hookConn, err := hookListener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer hookConn.Close()
	ready := make([]byte, 1)
	if _, err = io.ReadFull(hookConn, ready); err != nil || ready[0] != 'R' {
		t.Fatalf("child socket ready: %v", err)
	}
	nativeBirth, err := process.Birth(native.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := process.ExecutablePath(native.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	req := contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "bind", RunID: "run", ProviderID: "claude", NativePID: native.Process.Pid, NativeBirth: nativeBirth, NativeExecutableSHA256: digest, TransportMode: "hook_pull", OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile, ManifestSHA256: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(req)
	conn := bridgeTestUnixPeer(t, filepath.Join(root, "peer.sock"))
	defer conn.Close()
	approved, decoded, err := s.AuthorizeBridgeBind(ctx, conn, raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Peer.PID != os.Getpid() || decoded.Peer.Birth != birth {
		t.Fatalf("not kernel peer: %+v", decoded.Peer)
	}
	view, err := s.db.BindBridge(approved, decoded)
	if err != nil {
		t.Fatalf("authorized context failed DB recheck: %v", err)
	}
	if view.CapabilityFile != preSpawnPath {
		t.Fatal("post-spawn binding path differed from injected path")
	}
	if _, err = hookConn.Write([]byte{'C'}); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(hookConn, ready); err != nil || ready[0] != 'Y' {
		t.Fatalf("child could not read later capability: %v", err)
	}
	cap, err := store.ReadBridgeCapability(view.CapabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "hook-socket", BindingID: view.BindingID, OwnerEpoch: view.OwnerEpoch, ProviderID: "claude", NativeSessionID: "session-socket", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	hookReq := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: view.OwnerEpoch, RequestID: "ingest-socket"}, HookCapability: cap.HookCapability}, Event: event}
	hookRaw, _ := json.Marshal(hookReq)
	hookCtx, decodedHook, err := s.AuthorizeBridgeIngest(ctx, hookConn, hookRaw)
	if err != nil {
		t.Fatal(err)
	}
	if decodedHook.Peer.PID != native.Process.Pid {
		t.Fatalf("hook peer=%+v", decodedHook.Peer)
	}
	ack, err := s.db.IngestHook(hookCtx, decodedHook)
	if err != nil || !ack.Committed {
		t.Fatalf("ingest=%+v err=%v", ack, err)
	}
	activate := contract.BridgeActivateRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: view.OwnerEpoch, RequestID: "activate-socket"}, OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}, NativeSessionID: "session-socket", ObservationEventID: event.EventID, ObservationHash: ack.PayloadHash}
	activateRaw, _ := json.Marshal(activate)
	activateCtx, decodedActivate, err := s.AuthorizeBridgeActivate(ctx, conn, activateRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ActivateBridge(activateCtx, decodedActivate); err != nil {
		t.Fatalf("owner activate: %v", err)
	}
	bad := req
	bad.NativeExecutableSHA256 = strings.Repeat("0", 64)
	badRaw, _ := json.Marshal(bad)
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, badRaw); err == nil {
		t.Fatal("wrong executable admitted")
	}
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, append(raw[:len(raw)-1], []byte(`,"Peer":{"PID":999999}}`)...)); err == nil {
		t.Fatal("JSON peer injected")
	}
	bad = req
	bad.OwnerMode = "native"
	badRaw, _ = json.Marshal(bad)
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, badRaw); err == nil {
		t.Fatal("managed grant accepted as native attachment")
	}
	bad = req
	bad.RunID = "different-run"
	badRaw, _ = json.Marshal(bad)
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, badRaw); err == nil {
		t.Fatal("cross-run control file admitted")
	}
	bad = req
	bad.NativeBirth = "wrong-birth"
	badRaw, _ = json.Marshal(bad)
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, badRaw); err == nil {
		t.Fatal("wrong process birth admitted")
	}
	if err = os.Chmod(controlFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, raw); err == nil {
		t.Fatal("public control file admitted")
	}
	if err = os.Chmod(controlFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(controlDir, "linked.json")
	if err = os.Symlink(controlFile, link); err != nil {
		t.Fatal(err)
	}
	bad = req
	bad.ControlFile = link
	badRaw, _ = json.Marshal(bad)
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, badRaw); err == nil {
		t.Fatal("symlink control file admitted")
	}
	other, err := s.db.CreateOwnerGrant(ctx, "controller", native.Process.Pid, nativeBirth)
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(ownerDir, other.ID+".json")
	otherBytes, _ := json.Marshal(other)
	if err = os.WriteFile(otherFile, otherBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthorizeBridgeOwner(ctx, conn, "run", "local", otherFile, controlFile); err == nil {
		t.Fatal("same-uid unregistered owner ancestry admitted")
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO host_launches(id,token_hash,origin_context_id,host_generation,executable,status,created_at) VALUES('host','h','o','g','/bin/sleep','online','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES('hook-worker','host','o','g',?,?, '/bin/sleep',1,'online','now')`, native.Process.Pid, nativeBirth); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthorizeBridgeIngest(ctx, hookConn, hookRaw); err == nil {
		t.Fatal("worker inherited hook capability")
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO host_launches(id,token_hash,origin_context_id,host_generation,executable,status,created_at) VALUES('host2','h','o','g','/bin/sleep','online','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES('worker','host2','o','g',?,?, '/bin/sleep',1,'online','now')`, os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthorizeBridgeBind(ctx, conn, raw); err == nil {
		t.Fatal("registered worker ancestry admitted")
	}
}

func bridgeTestHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestBridgeSocketChild(t *testing.T) {
	path := os.Getenv("B02_CHILD_SOCKET")
	if path == "" {
		return
	}
	bindingPath := os.Getenv("AGENT_BIRD_BRIDGE_BINDING")
	if bindingPath == "" {
		t.Fatal("launcher omitted explicit binding path")
	}
	if _, err := os.Stat(bindingPath); !os.IsNotExist(err) {
		t.Fatalf("first Hook unexpectedly found capability: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte{'R'}); err != nil {
		t.Fatal(err)
	}
	marker := make([]byte, 1)
	if _, err = io.ReadFull(conn, marker); err != nil || marker[0] != 'C' {
		t.Fatalf("bind signal: %v", err)
	}
	if _, err = store.ReadBridgeCapability(bindingPath); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write([]byte{'Y'}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, conn)
}

func bridgeTestUnixPeer(t *testing.T, path string) net.Conn {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	return server
}
