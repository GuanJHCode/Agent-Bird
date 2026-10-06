package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestBridgeCapabilityReaderRejectsNoWriterFIFOWithoutBlocking(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	path := BridgeCapabilityPath(state, "fifo-run")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadBridgeCapability(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO capability accepted")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("FIFO capability open blocked without writer")
	}
}

func TestBridgeBindObserveActivateAndDurableDedup(t *testing.T) {
	ctx := context.Background()
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.CreateOwnerGrant(ctx, "controller", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES(?,?,?,?,?,?,'queued',?,'collect')`, "run", "controller", 1, grant.ID, os.Getpid(), birth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO run_control(run_id,token_hash) VALUES('run',?)`, runtimeHash("control")); err != nil {
		t.Fatal(err)
	}
	native := exec.Command("/bin/sleep", "30")
	if err := native.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = native.Process.Kill(); _, _ = native.Process.Wait() }()
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
	owner := WithBridgeOwnerProof(ctx, BridgeOwnerProof{RunID: "run", OwnerMode: "local", ControllerThread: "controller", ControlToken: "control", OwnerContextID: grant.ID, OwnerPID: os.Getpid(), OwnerBirth: birth, OwnerGrantID: grant.ID, OwnerGrantToken: grant.Token, PeerPID: os.Getpid(), PeerBirth: birth})
	req := contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "bind-1", RunID: "run", ProviderID: "claude", NativePID: native.Process.Pid, NativeBirth: nativeBirth, NativeExecutableSHA256: digest, TransportMode: "hook_pull", OwnerMode: "local", ManifestSHA256: strings.Repeat("a", 64)}
	if _, err := db.BindBridge(ctx, req); err == nil {
		t.Fatal("forged request peer alone bound a run")
	}
	if _, err := db.sql.Exec(`UPDATE runs SET delivery_mode='native' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindBridge(owner, req); err == nil {
		t.Fatal("native run converted in place")
	}
	if _, err := db.sql.Exec(`UPDATE runs SET delivery_mode='collect',created_at='2026-01-01T00:00:00Z' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindBridge(owner, req); err == nil {
		t.Fatal("pre-migration collect run converted in place")
	}
	if _, err := db.sql.Exec(`UPDATE runs SET created_at=? WHERE id='run'`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	wrong := WithBridgeOwnerProof(ctx, BridgeOwnerProof{RunID: "run", OwnerMode: "local", ControllerThread: "controller", ControlToken: "wrong", OwnerContextID: grant.ID, OwnerPID: os.Getpid(), OwnerBirth: birth, OwnerGrantID: grant.ID, OwnerGrantToken: grant.Token, PeerPID: os.Getpid(), PeerBirth: birth})
	if _, err := db.BindBridge(wrong, req); err == nil {
		t.Fatal("wrong run control admitted")
	}
	view, err := db.BindBridge(owner, req)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "PENDING_SESSION" || view.OwnerEpoch != 1 || view.CapabilityFile != BridgeCapabilityPath(filepath.Dir(db.path), "run") {
		t.Fatalf("view=%+v", view)
	}
	if BridgeCapabilityPath(filepath.Dir(db.path), "run") == BridgeCapabilityPath(filepath.Dir(db.path), "other-run") {
		t.Fatal("pre-spawn path did not bind explicit run")
	}
	if _, err := ReadBridgeCapability(view.CapabilityFile); err != nil {
		t.Fatalf("valid private capability: %v", err)
	}
	link := filepath.Join(filepath.Dir(view.CapabilityFile), strings.Repeat("d", 64)+".json")
	if err := os.Symlink(view.CapabilityFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBridgeCapability(link); err == nil {
		t.Fatal("symlink capability accepted")
	}
	if err := os.Chmod(view.CapabilityFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBridgeCapability(view.CapabilityFile); err == nil {
		t.Fatal("public capability accepted")
	}
	if err := os.Chmod(view.CapabilityFile, 0600); err != nil {
		t.Fatal(err)
	}
	var cap struct {
		HookCapability string `json:"hook_capability"`
		BindingID      string `json:"binding_id"`
		OwnerEpoch     uint64 `json:"owner_epoch"`
	}
	raw, err := os.ReadFile(view.CapabilityFile)
	if err != nil || json.Unmarshal(raw, &cap) != nil || cap.HookCapability == "" || cap.BindingID != view.BindingID {
		t.Fatalf("capability file: %v", err)
	}
	if strings.Contains(string(raw), "control") || strings.Contains(string(raw), grant.Token) {
		t.Fatal("owner secret in binding file")
	}
	hook := WithBridgeHookProof(ctx, BridgeHookProof{BindingID: view.BindingID, HookToken: cap.HookCapability, PeerPID: native.Process.Pid, PeerBirth: nativeBirth})
	event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "hook-one", BindingID: view.BindingID, OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session-a", Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
	ingest := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "ingest-1"}, HookCapability: cap.HookCapability}, Event: event}
	for _, field := range []string{"event_id", "native_session_id"} {
		canary := ingest
		if field == "event_id" {
			canary.Event.EventID = "hook-" + cap.HookCapability
		} else {
			canary.Event.NativeSessionID = "session-" + cap.HookCapability
		}
		if leakedAck, err := db.IngestHook(hook, canary); err == nil || leakedAck.Committed {
			t.Fatalf("%s secret stored: %+v %v", field, leakedAck, err)
		}
	}
	var secretRows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_ingress WHERE CAST(normalized_json AS TEXT) LIKE ?`, "%"+cap.HookCapability+"%").Scan(&secretRows); err != nil || secretRows != 0 {
		t.Fatalf("secret observation persisted: %d %v", secretRows, err)
	}
	if visible, _ := json.Marshal(view); strings.Contains(string(visible), cap.HookCapability) {
		t.Fatal("binding view leaked hook capability")
	}
	available := db.storageAvailable
	db.storageAvailable = func(string) (uint64, error) { return 0, nil }
	if ack, err := db.IngestHook(hook, ingest); err == nil || ack.Committed {
		t.Fatalf("disk failure ACK=%+v err=%v", ack, err)
	}
	db.storageAvailable = available
	progressLimit := db.runProgressLimit
	db.runProgressLimit = 0
	if ack, err := db.IngestHook(hook, ingest); err == nil || ack.Committed {
		t.Fatalf("quota failure ACK=%+v err=%v", ack, err)
	}
	db.runProgressLimit = progressLimit
	var beforeCount int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_ingress`).Scan(&beforeCount); err != nil || beforeCount != 0 {
		t.Fatalf("failed ingress half-written: %d %v", beforeCount, err)
	}
	stop := ingest
	stop.RequestID = "stop"
	stop.Event.EventID = "hook-stop"
	stop.Event.Kind = "tool_observed"
	stop.Event.Metadata.NativeHook = "Stop"
	if stopAck, err := db.IngestHook(hook, stop); err != nil || !stopAck.Committed {
		t.Fatalf("Stop observation=%+v err=%v", stopAck, err)
	}
	if pending, err := db.BridgeBindingIdentity(ctx, view.BindingID); err != nil || pending.Status != "PENDING_SESSION" {
		t.Fatalf("Stop activated=%+v err=%v", pending, err)
	}
	ack, err := db.IngestHook(hook, ingest)
	if err != nil || !ack.Committed || ack.Duplicate {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	if _, err := db.ActivateBridge(hook, contract.BridgeActivateRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: ingest.BridgeRequestBase}, NativeSessionID: "session-a", ObservationEventID: "hook-one", ObservationHash: ack.PayloadHash}); err == nil {
		t.Fatal("Hook activated itself")
	}
	activate := contract.BridgeActivateRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: ingest.BridgeRequestBase, OwnerMode: "local"}, NativeSessionID: "session-a", ObservationEventID: "hook-one", ObservationHash: ack.PayloadHash}
	active, err := db.ActivateBridge(owner, activate)
	if err != nil || active.Status != "ACTIVE" {
		t.Fatalf("activate=%+v err=%v", active, err)
	}
	if _, err := db.ActivateBridge(owner, activate); err != nil {
		t.Fatalf("idempotent activation: %v", err)
	}
	var candidateCreated, candidateUpdated string
	if err := db.sql.QueryRow(`SELECT i.created_at,b.updated_at FROM bridge_ingress i JOIN bridge_bindings b ON b.binding_id=i.binding_id WHERE i.binding_id=? AND i.event_id=?`, view.BindingID, event.EventID).Scan(&candidateCreated, &candidateUpdated); err != nil {
		t.Fatal(err)
	}
	if len(candidateCreated) != 30 || candidateCreated != candidateUpdated {
		t.Fatalf("candidate timestamps created=%q updated=%q", candidateCreated, candidateUpdated)
	}
	if _, err := time.Parse(time.RFC3339Nano, candidateCreated); err != nil {
		t.Fatalf("candidate timestamp not RFC3339Nano: %v", err)
	}
	// Revocation must remain available with no control headroom. ACTIVE to
	// DETACHED grows status by two bytes but clears the 64-byte token hash.
	if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES(?,?,?,?,?,?,'queued',?,'collect')`, "unbind-run", "controller", 1, grant.ID, os.Getpid(), birth, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO run_control(run_id,token_hash) VALUES('unbind-run',?)`, runtimeHash("control")); err != nil {
		t.Fatal(err)
	}
	unbindProof, ok := BridgeOwnerProofFromContext(owner)
	if !ok {
		t.Fatal("owner proof missing")
	}
	unbindProof.RunID = "unbind-run"
	unbindOwner := WithBridgeOwnerProof(ctx, unbindProof)
	unbindBind := req
	unbindBind.RunID, unbindBind.RequestID = "unbind-run", "bind-unbind"
	unbindView, err := db.BindBridge(unbindOwner, unbindBind)
	if err != nil {
		t.Fatal(err)
	}
	unbindCap, err := ReadBridgeCapability(unbindView.CapabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	unbindHook := WithBridgeHookProof(ctx, BridgeHookProof{BindingID: unbindView.BindingID, HookToken: unbindCap.HookCapability, PeerPID: native.Process.Pid, PeerBirth: nativeBirth})
	unbindEvent := event
	unbindEvent.BindingID, unbindEvent.EventID, unbindEvent.NativeSessionID = unbindView.BindingID, "unbind-observation", "unbind-session"
	unbindIngest := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: unbindView.BindingID, OwnerEpoch: 1, RequestID: "unbind-observe"}, HookCapability: unbindCap.HookCapability}, Event: unbindEvent}
	unbindAck, err := db.IngestHook(unbindHook, unbindIngest)
	if err != nil || !unbindAck.Committed {
		t.Fatalf("unbind candidate ACK=%+v err=%v", unbindAck, err)
	}
	unbindActivate := contract.BridgeActivateRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: unbindIngest.BridgeRequestBase, OwnerMode: "local"}, NativeSessionID: "unbind-session", ObservationEventID: "unbind-observation", ObservationHash: unbindAck.PayloadHash}
	if _, err := db.ActivateBridge(unbindOwner, unbindActivate); err != nil {
		t.Fatal(err)
	}
	beforeUnbindRun := bridgeUsage(t, db.sql, "run", "unbind-run")
	beforeUnbindGlobal := bridgeUsage(t, db.sql, "global", "")
	oldRunLimit, oldGlobalLimit := db.runControlLimit, db.userControlLimit
	db.runControlLimit, db.userControlLimit = beforeUnbindRun, beforeUnbindGlobal
	unbindActive := contract.BridgeUnbindRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: unbindView.BindingID, OwnerEpoch: 1, RequestID: "unbind-full"}, OwnerMode: "local"}}
	if err := db.UnbindBridge(unbindOwner, unbindActive); err != nil {
		t.Fatalf("full-quota Unbind: %v", err)
	}
	db.runControlLimit, db.userControlLimit = oldRunLimit, oldGlobalLimit
	if got := bridgeUsage(t, db.sql, "run", "unbind-run"); got != beforeUnbindRun-62 {
		t.Fatalf("Unbind run refund=%d before=%d", got, beforeUnbindRun)
	}
	if got := bridgeUsage(t, db.sql, "global", ""); got != beforeUnbindGlobal-62 {
		t.Fatalf("Unbind global refund=%d before=%d", got, beforeUnbindGlobal)
	}
	conflict := ingest
	conflict.RequestID = "other-session"
	conflict.Event.EventID = "hook-other"
	conflict.Event.NativeSessionID = "session-b"
	conflict.Event.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.IngestHook(hook, conflict); err == nil {
		t.Fatal("different native session admitted")
	}
	stale := ingest
	stale.OwnerEpoch = 2
	stale.Event.OwnerEpoch = 2
	if _, err := db.IngestHook(hook, stale); err == nil {
		t.Fatal("old binding token used for new epoch")
	}
	duplicate, err := db.IngestHook(hook, ingest)
	if err != nil || !duplicate.Duplicate || duplicate.PayloadHash != ack.PayloadHash {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	ingest.Event.Kind = "tool_observed"
	ingest.Event.Metadata.NativeHook = "PostToolUse"
	if _, err := db.IngestHook(hook, ingest); err == nil {
		t.Fatal("same event ID with different bytes accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(filepath.Join(filepath.Dir(view.CapabilityFile), "..", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if again, err := db.IngestHook(hook, contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "replay"}, HookCapability: cap.HookCapability}, Event: event}); err != nil || !again.Duplicate {
		t.Fatalf("restart replay=%+v err=%v", again, err)
	}
	end := event
	end.EventID = "hook-end"
	end.Kind = "session_ended"
	end.Metadata.NativeHook = "SessionEnd"
	end.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	endReq := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "end"}, HookCapability: cap.HookCapability}, Event: end}
	endRaw, err := json.Marshal(end)
	if err != nil {
		t.Fatal(err)
	}
	// The internal created_at format has a fixed width. Exactly the ingress
	// bytes fit; ACTIVE -> DETACHED needs two additional control bytes.
	endIngressBytes := int64(len(view.BindingID) + len(end.EventID) + 64 + len(endRaw) + len(end.Kind) + len(end.NativeSessionID) + 30)
	beforeEndRun := bridgeUsage(t, db.sql, "run", "run")
	beforeEndGlobal := bridgeUsage(t, db.sql, "global", "")
	for _, scope := range []string{"run", "global"} {
		baselineRun := beforeEndRun
		baselineGlobal := beforeEndGlobal
		limit := baselineRun + endIngressBytes
		oldRunLimit, oldGlobalLimit := db.runControlLimit, db.userControlLimit
		if scope == "run" {
			db.runControlLimit = limit
		} else {
			limit = baselineGlobal + endIngressBytes
			db.userControlLimit = limit
		}
		endAck, endErr := db.IngestHook(hook, endReq)
		current := bridgeUsage(t, db.sql, scope, map[string]string{"run": "run", "global": ""}[scope])
		if endErr == nil || endAck.Committed || current != map[string]int64{"run": baselineRun, "global": baselineGlobal}[scope] {
			t.Fatalf("%s SessionEnd boundary: ACK=%+v usage=%d limit=%d err=%v", scope, endAck, current, limit, endErr)
		}
		var count int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_ingress WHERE binding_id=? AND event_id=?`, view.BindingID, end.EventID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rejected SessionEnd ingress=%d err=%v", scope, count, err)
		}
		if status, err := db.BridgeBindingIdentity(ctx, view.BindingID); err != nil || status.Status != "ACTIVE" {
			t.Fatalf("%s rejected SessionEnd changed binding=%+v err=%v", scope, status, err)
		}
		if got := bridgeUsage(t, db.sql, "run", "run"); got != baselineRun {
			t.Fatalf("%s rejected SessionEnd changed run usage=%d before=%d", scope, got, baselineRun)
		}
		if got := bridgeUsage(t, db.sql, "global", ""); got != baselineGlobal {
			t.Fatalf("%s rejected SessionEnd changed global usage=%d before=%d", scope, got, baselineGlobal)
		}
		db.runControlLimit, db.userControlLimit = oldRunLimit, oldGlobalLimit
	}
	if endAck, err := db.IngestHook(hook, endReq); err != nil || !endAck.Committed {
		t.Fatalf("end ACK=%+v err=%v", endAck, err)
	}
	if got := bridgeUsage(t, db.sql, "run", "run"); got != beforeEndRun+endIngressBytes+2 {
		t.Fatalf("committed SessionEnd run usage=%d want=%d", got, beforeEndRun+endIngressBytes+2)
	}
	if got := bridgeUsage(t, db.sql, "global", ""); got != beforeEndGlobal+endIngressBytes+2 {
		t.Fatalf("committed SessionEnd global usage=%d want=%d", got, beforeEndGlobal+endIngressBytes+2)
	}
	var endCreated string
	if err := db.sql.QueryRow(`SELECT created_at FROM bridge_ingress WHERE binding_id=? AND event_id=?`, view.BindingID, end.EventID).Scan(&endCreated); err != nil || len(endCreated) != 30 {
		t.Fatalf("SessionEnd created_at=%q err=%v", endCreated, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, endCreated); err != nil {
		t.Fatalf("SessionEnd timestamp not RFC3339Nano: %v", err)
	}
	if replay, err := db.IngestHook(hook, endReq); err != nil || !replay.Duplicate {
		t.Fatalf("end replay=%+v err=%v", replay, err)
	}
	if _, err := db.IngestHook(hook, contract.BridgeIngestRequest{BridgeHookRequest: endReq.BridgeHookRequest, Event: contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "after-end", BindingID: view.BindingID, OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: "session-a", Kind: "tool_observed", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "Stop"}}}); err == nil {
		t.Fatal("detached hook admitted new observation")
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch',?,1,x'01',?,'now')`, view.BindingID, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at) VALUES('attempt','batch',?,1,1,?,'EMITTING','later',?,?,'now')`, view.BindingID, runtimeHash("claim"), native.Process.Pid, nativeBirth); err != nil {
		t.Fatal(err)
	}
	next := exec.Command("/bin/sleep", "30")
	if err := next.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Process.Kill(); _, _ = next.Process.Wait() }()
	nextBirth, err := process.Birth(next.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	rebind := req
	rebind.RequestID = "bind-2"
	rebind.NativePID = next.Process.Pid
	rebind.NativeBirth = nextBirth
	if _, err := db.BindBridge(owner, rebind); err == nil {
		t.Fatal("live old sender was rebound")
	}
	if err := native.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := native.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BindBridge(owner, rebind); err == nil {
		t.Fatal("EMITTING history bypassed by rebind")
	}
	unbind := contract.BridgeUnbindRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "unbind"}, OwnerMode: "local"}}
	if err := db.UnbindBridge(owner, unbind); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id='attempt'`).Scan(&state); err != nil || state != "EMITTING" {
		t.Fatalf("uncertain history rewritten: %s %v", state, err)
	}
	if _, err := db.BindBridge(owner, rebind); err == nil {
		t.Fatal("Unbind laundered unresolved attempt")
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET state='RECEIVED' WHERE attempt_id='attempt'`); err != nil {
		t.Fatal(err)
	}
	second, err := db.BindBridge(owner, rebind)
	if err != nil || second.OwnerEpoch != 2 {
		t.Fatalf("safe rebind=%+v err=%v", second, err)
	}
	oldReplay := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: 1, RequestID: "old-replay"}, HookCapability: cap.HookCapability}, Event: event}
	if oldAck, err := db.IngestHook(hook, oldReplay); err == nil || oldAck.Committed {
		t.Fatalf("old token advanced new epoch: %+v %v", oldAck, err)
	}
}
