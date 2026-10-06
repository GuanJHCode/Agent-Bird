package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestBridgeHistoricalRoutesUseCurrentRealSocketOwnerOnly(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b02-history-")
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
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: "collect", ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "stable", OriginPID: 7, OriginBirth: "old"}, Host: store.HostLaunchSpec{OriginContextID: "stable", HostGeneration: "old-gen", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('old','run','claude','stable',7,'old',8,'old-native',?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/old','bind','now','now')`, strings.Repeat("a", 64), strings.Repeat("b", 64), bridgeTestHash("old-hook")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch','old',1,x'01',?,'now')`, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at) VALUES('attempt','batch','old',1,1,?,'EMITTING','later',8,'old-native','now')`, bridgeTestHash("claim")); err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.RebindOwner(ctx, store.RebindOwnerSpec{RunID: "run", OriginContextID: "stable", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "new-gen", AttachmentProofSHA256: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	grant := store.OwnerGrant{Version: 1, Kind: "local-owner", ID: "stable", Token: "new-grant", ControllerThread: "owner", OriginPID: os.Getpid(), OriginBirth: birth, OriginContextID: "stable", HostGeneration: "stable"}
	if _, err = s.db.SQL().Exec(`INSERT INTO owner_grants(id,token_hash,controller_thread,origin_pid,origin_birth,created_at) VALUES('stable',?,'owner',?,?,'now')`, bridgeTestHash(grant.Token), os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	ownerDir := filepath.Join(state, "owners")
	if err = os.Mkdir(ownerDir, 0700); err != nil {
		t.Fatal(err)
	}
	ownerFile := filepath.Join(ownerDir, "stable.json")
	grantRaw, _ := json.Marshal(grant)
	if err = os.WriteFile(ownerFile, grantRaw, 0600); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(state, "control")
	if err = os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	controlFile := filepath.Join(controlDir, "run.json")
	controlRaw, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": "owner", "control_token": receipt.ControlToken, "bootstrap_path": filepath.Join(state, "bootstrap.json"), "host_launch_id": receipt.LaunchID, "host_launch_token": receipt.LaunchToken})
	if err = os.WriteFile(controlFile, controlRaw, 0600); err != nil {
		t.Fatal(err)
	}
	conn := bridgeTestUnixPeer(t, filepath.Join(root, "history.sock"))
	defer conn.Close()
	base := contract.BridgeRequestBase{SchemaVersion: 1, BindingID: "old", OwnerEpoch: 1, RequestID: "historical"}
	owner := contract.BridgeOwnerRequest{BridgeRequestBase: base, OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}
	statusRaw, _ := json.Marshal(contract.BridgeStatusRequest{BridgeOwnerRequest: owner})
	if _, req, err := s.AuthorizeBridgeStatus(ctx, conn, statusRaw); err != nil || req.Peer.PID != os.Getpid() {
		t.Fatalf("historical status %+v %v", req.Peer, err)
	}
	receive := contract.BridgeReceiveRequest{BridgeOwnerRequest: owner, BatchID: "batch", AttemptID: "attempt", RenderSHA256: strings.Repeat("c", 64)}
	receiveRaw, _ := json.Marshal(receive)
	if _, req, err := s.AuthorizeBridgeBatchQuery(ctx, conn, receiveRaw); err != nil || req.Peer.PID != os.Getpid() {
		t.Fatalf("historical query %+v %v", req.Peer, err)
	}
	if _, req, err := s.AuthorizeBridgeReceived(ctx, conn, receiveRaw); err != nil || req.Peer.PID != os.Getpid() {
		t.Fatalf("historical received authorization %+v %v", req.Peer, err)
	}
	owner.OwnerEpoch = 2
	badRaw, _ := json.Marshal(contract.BridgeStatusRequest{BridgeOwnerRequest: owner})
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, badRaw); err == nil {
		t.Fatal("wrong historical epoch admitted")
	}
	owner.OwnerEpoch = 1
	owner.OwnerMode = "native"
	badRaw, _ = json.Marshal(contract.BridgeStatusRequest{BridgeOwnerRequest: owner})
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, badRaw); err == nil {
		t.Fatal("local file accepted as native attachment")
	}
	owner.OwnerMode = "local"
	oldGrant := grant
	oldGrant.Token = "old-grant"
	oldGrant.OriginPID = 7
	oldGrant.OriginBirth = "old"
	oldRaw, _ := json.Marshal(oldGrant)
	if err = os.WriteFile(ownerFile, oldRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, statusRaw); err == nil {
		t.Fatal("old owner token and physical identity admitted")
	}
	if err = os.WriteFile(ownerFile, grantRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`UPDATE bridge_bindings SET owner_context_id='different' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, statusRaw); err == nil {
		t.Fatal("cross-context history admitted")
	}
	if _, err = s.db.SQL().Exec(`UPDATE bridge_bindings SET owner_context_id='stable' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('other','owner',1,'stable',?,?,'queued',?,'collect')`, os.Getpid(), birth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`UPDATE bridge_bindings SET run_id='other' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, statusRaw); err == nil {
		t.Fatal("cross-run history admitted")
	}
	if _, err = s.db.SQL().Exec(`UPDATE bridge_bindings SET run_id='run' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthorizeBridgeActivate(ctx, conn, func() json.RawMessage {
		raw, _ := json.Marshal(contract.BridgeActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: "session", ObservationEventID: "event", ObservationHash: strings.Repeat("e", 64)})
		return raw
	}()); err == nil {
		t.Fatal("historical Activate admitted")
	}
	redeliverRaw, _ := json.Marshal(contract.BridgeRedeliverRequest{BridgeOwnerRequest: owner, BatchID: "batch", AttemptID: "attempt", Reason: "recovered"})
	if _, _, err := s.AuthorizeBridgeRedeliver(ctx, conn, redeliverRaw); err == nil {
		t.Fatal("historical Redeliver admitted")
	}
	unbindRaw, _ := json.Marshal(contract.BridgeUnbindRequest{BridgeOwnerRequest: owner})
	if _, _, err := s.AuthorizeBridgeUnbind(ctx, conn, unbindRaw); err == nil {
		t.Fatal("historical Unbind admitted")
	}
	if _, err = s.db.SQL().Exec(`INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES('worker',?,'stable','new-gen',?,?,'/private/bin/host',1,'online','now')`, receipt.LaunchID, os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthorizeBridgeStatus(ctx, conn, statusRaw); err == nil {
		t.Fatal("worker used current-owner historical status")
	}
	var stateAfter string
	if err = s.db.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id='attempt'`).Scan(&stateAfter); err != nil || stateAfter != "EMITTING" {
		t.Fatalf("authorization changed attempt: %s %v", stateAfter, err)
	}
}
