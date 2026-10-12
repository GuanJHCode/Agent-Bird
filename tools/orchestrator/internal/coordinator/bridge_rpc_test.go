package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestBridgeCancelledSocketDrainsAdmissionAndWatchers(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-admit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.bridgeAdmission = make(chan struct{}, 1)
	hold, err := s.db.SQL().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			go s.handleConnection(ctx, conn)
		}
	}()
	request := json.RawMessage(`{"schema_version":1,"binding_id":"missing","owner_epoch":1,"request_id":"claim","hook_capability":"hook-aaaaaaaaaaaaaaaaaaaaaaaa","native_hook":"SessionStart"}`)
	first, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := ipc.Write(first, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindBridgeClaim, RequestID: "first", Payload: request}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.bridgeAdmission) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.bridgeAdmission) != 1 {
		t.Fatal("first bridge handler never admitted")
	}
	second, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := ipc.Write(second, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindBridgeClaim, RequestID: "second", Payload: request}); err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	busy, err := ipc.Read(second)
	if err != nil {
		t.Fatal(err)
	}
	var failure ErrorResponse
	if err := json.Unmarshal(busy.Payload, &failure); err != nil {
		t.Fatal(err)
	}
	if busy.Kind != ipc.KindError || failure.Error != "bridge_busy" {
		t.Fatalf("overflow=%s %q", busy.Kind, failure.Error)
	}
	_ = first.Close()
	_ = hold.Rollback()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.bridgeMu.Lock()
		active := len(s.bridgeConnections)
		s.bridgeMu.Unlock()
		if len(s.bridgeAdmission) == 0 && active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.bridgeMu.Lock()
	active := len(s.bridgeConnections)
	s.bridgeMu.Unlock()
	if len(s.bridgeAdmission) != 0 || active != 0 {
		t.Fatalf("bridge handlers=%d connections=%d", len(s.bridgeAdmission), active)
	}
	var attempts int
	if err := s.db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_delivery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("late permit count=%d", attempts)
	}
}

func TestBridgeRecoveryRunsBeforeListenAndReadyDoesNotRevokeLiveEmit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-recover-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('run','owner',1,'context',7,'old','queued',?,'collect')`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('binding','run','claude','context',7,'old',8,'native',?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/cap','bind','now','now')`, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch','binding',1,x'01',?,'now')`, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at,updated_at) VALUES('attempt','batch','binding',1,1,?,'EMITTING','later',8,'native','now','now')`, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var status, hash string
	if err := s.db.SQL().QueryRow(`SELECT state,claim_token_hash FROM bridge_delivery_attempts WHERE attempt_id='attempt'`).Scan(&status, &hash); err != nil {
		t.Fatal(err)
	}
	if status != "UNCERTAIN" || hash != "" {
		t.Fatalf("startup state=%s hash=%q", status, hash)
	}
	if _, err := s.db.SQL().Exec(`UPDATE bridge_delivery_attempts SET state='EMITTING',claim_token_hash=? WHERE attempt_id='attempt'`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx) }()
	for i := 0; i < 2; i++ {
		response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "ready", Payload: json.RawMessage(`{}`)})
		if err != nil || response.Kind != ipc.KindResponse {
			t.Fatalf("ready=%s err=%v", response.Kind, err)
		}
	}
	if err := s.db.SQL().QueryRow(`SELECT state,claim_token_hash FROM bridge_delivery_attempts WHERE attempt_id='attempt'`).Scan(&status, &hash); err != nil {
		t.Fatal(err)
	}
	if status != "EMITTING" || hash != strings.Repeat("f", 64) {
		t.Fatalf("ready revoked live permit: %s %q", status, hash)
	}
}

func TestBridgeKindsRejectMalformedPayloadThroughRealSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-route-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx) }()
	for _, kind := range []ipc.Kind{
		"bridge_bind", "bridge_activate", "bridge_ingest", "bridge_claim",
		"bridge_begin_emit", "bridge_offered", "bridge_received",
		"bridge_query_batch", "bridge_redeliver", "bridge_status", "bridge_unbind",
	} {
		t.Run(string(kind), func(t *testing.T) {
			response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: "malformed", Payload: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if response.Kind != ipc.KindError {
				t.Fatalf("kind=%s", response.Kind)
			}
			var failure ErrorResponse
			if err := json.Unmarshal(response.Payload, &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error != "bridge_invalid_input" {
				t.Fatalf("%s: %s", kind, failure.Error)
			}
		})
	}
	spoof := json.RawMessage(`{"schema_version":1,"binding_id":"binding","owner_epoch":1,"request_id":"spoof","hook_capability":"hook-aaaaaaaaaaaaaaaaaaaaaaaa","native_hook":"SessionStart","peer":{"pid":1}}`)
	response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindBridgeClaim, RequestID: "spoof", Payload: spoof})
	if err != nil {
		t.Fatal(err)
	}
	var failure ErrorResponse
	if err := json.Unmarshal(response.Payload, &failure); err != nil {
		t.Fatal(err)
	}
	if response.Kind != ipc.KindError || failure.Error != "bridge_invalid_input" {
		t.Fatalf("forged peer=%s %q", response.Kind, failure.Error)
	}
}
