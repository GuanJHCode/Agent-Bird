package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestBridgeCLISubprocessHelper(t *testing.T) {
	if os.Getenv("B04_CLI_HELPER") != "1" {
		return
	}
	op := os.Getenv("B04_CLI_OP")
	args := []string{"bridge", op, "--request", os.Getenv("B04_CLI_REQUEST")}
	if op == "collect" {
		args = []string{"collect", "--state-dir", os.Getenv("B04_CLI_STATE"), "--task-id", "task", "--control-file", os.Getenv("B04_CLI_CONTROL")}
	} else if op == "ack" {
		args = []string{"ack", "--state-dir", os.Getenv("B04_CLI_STATE"), "--request", os.Getenv("B04_CLI_REQUEST")}
	}
	err := run(context.Background(), args, os.Stdout, os.Stderr)
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": errorCode(err)})
		os.Exit(2)
	}
	os.Exit(0)
}

// This process remains a real descendant of the owner process throughout
// Bind, observation and Claim/BeginEmit. It never substitutes a JSON peer.
func TestBridgeNativeRouteChildHelper(t *testing.T) {
	if os.Getenv("B04_NATIVE_ROUTE_HELPER") != "1" {
		return
	}
	capPath := os.Getenv("B04_NATIVE_CAP")
	socket := os.Getenv("B04_NATIVE_SOCKET")
	if _, err := os.Stat(capPath); !os.IsNotExist(err) {
		t.Fatalf("capability existed before Bind: %v", err)
	}
	if _, err := os.Stdout.Write([]byte("READY\n")); err != nil {
		t.Fatal(err)
	}
	call := func(kind ipc.Kind, request any) []byte {
		t.Helper()
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		response, err := ipc.Call(ctx, socket, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: string(kind), Payload: raw})
		if err != nil || response.Kind != ipc.KindResponse {
			t.Fatalf("%s response=%s payload=%s err=%v", kind, response.Kind, response.Payload, err)
		}
		return response.Payload
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		capability, err := store.ReadBridgeCapability(capPath)
		if err != nil {
			t.Fatal(err)
		}
		base := contract.BridgeRequestBase{SchemaVersion: 1, BindingID: capability.BindingID, OwnerEpoch: capability.OwnerEpoch, RequestID: scanner.Text()}
		hook := contract.BridgeHookRequest{BridgeRequestBase: base, HookCapability: capability.HookCapability}
		switch scanner.Text() {
		case "INGEST":
			session := os.Getenv("B04_NATIVE_SESSION")
			if session == "" {
				session = "session-r7"
			}
			event := contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "observation-r7", BindingID: capability.BindingID, OwnerEpoch: capability.OwnerEpoch, ProviderID: "claude", NativeSessionID: session, Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}
			payload := call(ipc.KindBridgeIngest, contract.BridgeIngestRequest{BridgeHookRequest: hook, Event: event})
			var ack contract.BridgeIngressAck
			if err := json.Unmarshal(payload, &ack); err != nil || !ack.Committed {
				t.Fatalf("ingest ack=%s err=%v", payload, err)
			}
			if err := json.NewEncoder(os.Stdout).Encode(ack); err != nil {
				t.Fatal(err)
			}
		case "CLAIM_EMIT":
			payload := call(ipc.KindBridgeClaim, contract.BridgeClaimRequest{BridgeHookRequest: hook, NativeHook: "SessionStart"})
			var reply contract.BridgeClaimReply
			if err := json.Unmarshal(payload, &reply); err != nil || reply.ClaimToken == "" {
				t.Fatalf("claim response invalid: %v", err)
			}
			batch := reply.PublicBatch()
			wire, err := bridgecodec.WireBytes(batch, batch.AttemptID, "claude", "SessionStart")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(wire)
			wireHash := hex.EncodeToString(sum[:])
			emit := contract.BridgeEmitRequest{BridgeRequestBase: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: reply.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wireHash}
			if payload := call(ipc.KindBridgeBeginEmit, emit); string(payload) != `{"status":"committed"}` {
				t.Fatalf("BeginEmit=%s", payload)
			}
			result := map[string]string{"batch_id": batch.BatchID, "attempt_id": batch.AttemptID, "render_sha256": batch.RenderSHA256, "wire_sha256": wireHash}
			if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown native command %q", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeOwnerCLISubprocessQueriesThenReceivesExactBatch(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "b04-cli-")
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
	exe, err := resolvedExecutable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.CreateOwnerGrant(ctx, "managed-claude-test", os.Getpid(), birth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: "collect", ID: "run", ControllerThread: "managed-claude-test", PlanRevision: 1, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: grant.ID, HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
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
	hookToken := "hook-" + strings.Repeat("a", 24)
	hookHash := sha256.Sum256([]byte(hookToken))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.SQL().Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('binding','run','claude','session',?,?,?,?,?,?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/cap','bind',?,?)`, grant.ID, os.Getpid(), birth, os.Getpid(), birth, digest, strings.Repeat("b", 64), hex.EncodeToString(hookHash[:]), now, now)
	if err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := db.SQL().QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='task'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, EventID: "result-1", RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: revision, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("c", 64)}
	body, _ := json.Marshal(event)
	_, err = db.SQL().Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'action-1','pending',?)`, event.EventID, launch.HostID, 1, "task", launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES('result-1','task')`); err != nil {
		t.Fatal(err)
	}
	claimToken, err := store.NewBridgeClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	hookCtx := store.WithBridgeHookProof(ctx, store.BridgeHookProof{BindingID: "binding", HookToken: hookToken, PeerPID: os.Getpid(), PeerBirth: birth})
	batch, err := db.ClaimBridge(hookCtx, contract.BridgeClaimRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, RequestID: "claim"}, HookCapability: hookToken}, NativeHook: "SessionStart", ClaimToken: claimToken})
	if err != nil {
		t.Fatal(err)
	}
	var wireHash string
	if err := db.SQL().QueryRow(`SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&wireHash); err != nil {
		t.Fatal(err)
	}
	sender := store.WithBridgeSenderProof(ctx, store.BridgeSenderProof{BindingID: "binding", PeerPID: os.Getpid(), PeerBirth: birth})
	base := contract.BridgeRequestBase{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, RequestID: "emit"}
	if err := db.BeginBridgeEmit(sender, contract.BridgeEmitRequest{BridgeRequestBase: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wireHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkBridgeOffered(sender, contract.BridgeOfferedRequest{BridgeRequestBase: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claimToken, WireSHA256: wireHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
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
	controlRaw, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": "managed-claude-test", "control_token": receipt.ControlToken, "bootstrap_path": filepath.Join(state, "bootstrap.json"), "host_launch_id": receipt.LaunchID, "host_launch_token": receipt.LaunchToken})
	if err := os.WriteFile(controlFile, controlRaw, 0600); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(root, "managed.json")
	managedRaw, _ := json.Marshal(managedOwnerRequest{Version: 1, Provider: "claude", Scope: "managed_process", ControllerThread: "managed-claude-test", LauncherPID: os.Getpid(), LauncherBirth: birth, LauncherPath: exe, LauncherDigest: digest})
	if err := os.WriteFile(managedPath, managedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = s.Serve(serveCtx) }()
	owner := contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: "binding", OwnerEpoch: 1, RequestID: "query"}, OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}
	request := contract.BridgeReceiveRequest{BridgeOwnerRequest: owner, BatchID: batch.BatchID, AttemptID: batch.AttemptID, RenderSHA256: batch.RenderSHA256}
	runCLI := func(op string, payload any) []byte {
		t.Helper()
		requestPath := filepath.Join(root, op+".json")
		raw, _ := json.Marshal(payload)
		if err := os.WriteFile(requestPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestBridgeCLISubprocessHelper$")
		cmd.Env = append(os.Environ(), "B04_CLI_HELPER=1", "B04_CLI_OP="+op, "B04_CLI_REQUEST="+requestPath, "B04_CLI_STATE="+state, "B04_CLI_CONTROL="+controlFile, managedOwnerRequestEnv+"="+managedPath)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s failed: %v stderr=%q", op, err, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("%s stderr=%q", op, stderr.String())
		}
		return stdout.Bytes()
	}
	owner.RequestID = "status"
	var status contract.BridgeStatusView
	if err := json.Unmarshal(runCLI("status", contract.BridgeStatusRequest{BridgeOwnerRequest: owner}), &status); err != nil {
		t.Fatal(err)
	}
	if status.BindingID != "binding" || len(status.Batches) != 1 || status.Batches[0].AttemptID != batch.AttemptID || status.Batches[0].State != "OFFERED" {
		t.Fatalf("status=%+v", status)
	}
	request.RequestID = "batch"
	var queried contract.BridgeBatch
	if err := json.Unmarshal(runCLI("batch", request), &queried); err != nil {
		t.Fatal(err)
	}
	if queried.BatchID != batch.BatchID || queried.AttemptID != batch.AttemptID || len(queried.Events) != 1 {
		t.Fatalf("query=%+v", queried)
	}
	check, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var bridgeState, businessState string
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&bridgeState); err != nil {
		t.Fatal(err)
	}
	if bridgeState != "OFFERED" {
		t.Fatalf("query changed attempt=%s", bridgeState)
	}
	request.RequestID = "received"
	var received contract.BridgeReceiveReceipt
	if err := json.Unmarshal(runCLI("received", request), &received); err != nil {
		t.Fatal(err)
	}
	if received.BatchID != batch.BatchID || received.AttemptID != batch.AttemptID {
		t.Fatalf("received=%+v", received)
	}
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&bridgeState); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&businessState); err != nil {
		t.Fatal(err)
	}
	if bridgeState != "RECEIVED" || businessState != "pending" {
		t.Fatalf("bridge=%s business=%s", bridgeState, businessState)
	}
	var collected coordinator.CollectResponse
	if err := json.Unmarshal(runCLI("collect", map[string]string{"marker": "original-command"}), &collected); err != nil {
		t.Fatal(err)
	}
	if len(collected.Events) != 1 || collected.Events[0].EventID != "result-1" || collected.DeliveryID == "" || collected.CollectionProofSHA256 == "" {
		t.Fatalf("original collect=%+v", collected)
	}
	item := collected.Events[0]
	ackRequest := ackFile{Version: 1, TaskID: "task", ControlFile: controlFile, DeliveryID: collected.DeliveryID, CollectionProofSHA256: collected.CollectionProofSHA256, Decisions: []store.AckDecision{{EventID: item.EventID, EventRevision: item.EventRevision, EventHash: item.PayloadHash, ActionSlot: item.ActionSlot, Decision: "handled", CommandID: "original-ack"}}}
	var acknowledged map[string]any
	if err := json.Unmarshal(runCLI("ack", ackRequest), &acknowledged); err != nil {
		t.Fatal(err)
	}
	if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&businessState); err != nil {
		t.Fatal(err)
	}
	if businessState != "acked" {
		t.Fatalf("original ACK status=%s response=%v", businessState, acknowledged)
	}
	owner.RequestID = "unbind"
	var unbound map[string]string
	if err := json.Unmarshal(runCLI("unbind", contract.BridgeUnbindRequest{BridgeOwnerRequest: owner}), &unbound); err != nil {
		t.Fatal(err)
	}
	if unbound["status"] != "unbound" {
		t.Fatalf("unbind=%v", unbound)
	}
	if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&businessState); err != nil {
		t.Fatal(err)
	}
	if businessState != "acked" {
		t.Fatalf("unbind changed business=%s", businessState)
	}
}

func TestBridgeCLIBindActivateRedeliverThroughRealCoordinatorRoute(t *testing.T) {
	runBridgeCLIRouteFixture(t, "")
}

func TestBridgeCLIPublicBindingViewsRejectKnownOwnerSecret(t *testing.T) {
	for _, kind := range []string{"control", "owner", "report"} {
		t.Run("idempotent_bind_"+kind, func(t *testing.T) { runBridgeCLIRouteFixture(t, "bind_"+kind) })
		t.Run("activate_before_commit_"+kind, func(t *testing.T) { runBridgeCLIRouteFixture(t, "activate_"+kind) })
	}
}

func runBridgeCLIRouteFixture(t *testing.T, secretMode string) {
	root, err := os.MkdirTemp("/tmp", "b04-route-")
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
	ownerBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ownerExe, err := resolvedExecutable()
	if err != nil {
		t.Fatal(err)
	}
	ownerDigest, err := process.ExecutableDigest(ctx, ownerExe)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.CreateOwnerGrant(ctx, "managed-claude-test", os.Getpid(), ownerBirth)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{DeliveryMode: "collect", ID: "run", ControllerThread: "managed-claude-test", PlanRevision: 1, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: ownerBirth}, Host: store.HostLaunchSpec{OriginContextID: grant.ID, HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: grant.ID, OriginPID: os.Getpid(), OriginBirth: ownerBirth, HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	secret := ""
	if strings.HasSuffix(secretMode, "control") {
		secret = receipt.ControlToken
	} else if strings.HasSuffix(secretMode, "owner") {
		secret = grant.Token
	} else if strings.HasSuffix(secretMode, "report") {
		secret = "report-token-" + strings.Repeat("e", 64)
		hash := sha256.Sum256([]byte(secret))
		result, err := db.SQL().Exec(`UPDATE report_capabilities SET token_hash=? WHERE run_id='run'`, hex.EncodeToString(hash[:]))
		if err != nil {
			t.Fatal(err)
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			t.Fatalf("report token fixture missing: affected=%d err=%v", count, err)
		}
	}
	var revision int
	if err := db.SQL().QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='task'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, EventID: "route-result", RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: revision, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("c", 64)}
	body, _ := json.Marshal(event)
	_, err = db.SQL().Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'route-action','pending',?)`, event.EventID, launch.HostID, 1, "task", launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES('route-result','task')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
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
	controlRaw, _ := json.Marshal(map[string]any{"version": 2, "run_id": "run", "controller_thread": "managed-claude-test", "control_token": receipt.ControlToken, "bootstrap_path": filepath.Join(state, "bootstrap.json"), "host_launch_id": receipt.LaunchID, "host_launch_token": receipt.LaunchToken})
	if err := os.WriteFile(controlFile, controlRaw, 0600); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(root, "managed.json")
	managedRaw, _ := json.Marshal(managedOwnerRequest{Version: 1, Provider: "claude", Scope: "managed_process", ControllerThread: "managed-claude-test", LauncherPID: os.Getpid(), LauncherBirth: ownerBirth, LauncherPath: ownerExe, LauncherDigest: ownerDigest})
	if err := os.WriteFile(managedPath, managedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancelServe := context.WithCancel(ctx)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = server.Serve(serveCtx) }()
	defer func() { cancelServe(); _ = server.Close(); <-serveDone }()
	capPath := store.BridgeCapabilityPath(state, "run")
	native := exec.Command(os.Args[0], "-test.run=^TestBridgeNativeRouteChildHelper$")
	native.Env = append(os.Environ(), "B04_NATIVE_ROUTE_HELPER=1", "B04_NATIVE_CAP="+capPath, "B04_NATIVE_SOCKET="+server.SocketPath())
	if secretMode != "" {
		native.Env = append(native.Env, "B04_NATIVE_SESSION="+secret)
	}
	nativeIn, err := native.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	nativeOut, err := native.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var nativeErr bytes.Buffer
	native.Stderr = &nativeErr
	if err := native.Start(); err != nil {
		t.Fatal(err)
	}
	nativeExited := false
	defer func() {
		if !nativeExited {
			_ = native.Process.Kill()
			_, _ = native.Process.Wait()
		}
	}()
	nativeReader := bufio.NewReader(nativeOut)
	ready, err := nativeReader.ReadString('\n')
	if err != nil || ready != "READY\n" {
		t.Fatalf("native ready=%q err=%v stderr=%q", ready, err, nativeErr.String())
	}
	nativeBirth, err := process.Birth(native.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	nativeExe, err := process.ExecutablePath(native.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	nativeDigest, err := process.ExecutableDigest(ctx, nativeExe)
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	runCLI := func(op string, request any, wantCode string) []byte {
		t.Helper()
		sequence++
		path := filepath.Join(root, fmt.Sprintf("%s-%d.json", op, sequence))
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestBridgeCLISubprocessHelper$")
		cmd.Env = append(os.Environ(), "B04_CLI_HELPER=1", "B04_CLI_OP="+op, "B04_CLI_REQUEST="+path, managedOwnerRequestEnv+"="+managedPath)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err = cmd.Run()
		if wantCode == "" {
			if err != nil || diagnostic.Len() != 0 {
				t.Fatalf("%s failed: %v stderr=%q", op, err, diagnostic.String())
			}
			return out.Bytes()
		}
		var failure map[string]string
		if secret != "" && (bytes.Contains(out.Bytes(), []byte(secret)) || bytes.Contains(diagnostic.Bytes(), []byte(secret))) {
			t.Fatalf("%s exposed actual control capability: stdout_len=%d stderr_len=%d", op, out.Len(), diagnostic.Len())
		}
		if err == nil || out.Len() != 0 || json.Unmarshal(diagnostic.Bytes(), &failure) != nil || failure["error"] != wantCode {
			t.Fatalf("%s expected %s: exit_failure=%t stdout_len=%d stdout_sha256=%x stderr_len=%d stderr_sha256=%x", op, wantCode, err != nil, out.Len(), sha256.Sum256(out.Bytes()), diagnostic.Len(), sha256.Sum256(diagnostic.Bytes()))
		}
		return nil
	}
	bind := contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "route-bind", RunID: "run", ProviderID: "claude", NativePID: native.Process.Pid, NativeBirth: nativeBirth, NativeExecutableSHA256: nativeDigest, TransportMode: "hook_pull", OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile, ManifestSHA256: strings.Repeat("a", 64)}
	badBind := bind
	badBind.NativeBirth = "wrong-birth"
	runCLI("bind", badBind, "bridge_sender_unknown")
	check, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var bindings int
	if err := check.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_bindings`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("invalid bind left %d rows: %v", bindings, err)
	}
	var view contract.BridgeBindingView
	if err := json.Unmarshal(runCLI("bind", bind, ""), &view); err != nil {
		t.Fatal(err)
	}
	if view.BindingID == "" || view.Status != "PENDING_SESSION" || view.CapabilityFile != capPath || view.NativePID != native.Process.Pid {
		t.Fatalf("bind view=%+v", view)
	}
	t.Logf("bind owner_pid=%d native_pid=%d binding=%s epoch=%d status=%s", os.Getpid(), native.Process.Pid, view.BindingID, view.OwnerEpoch, view.Status)
	if err := check.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_bindings WHERE binding_id=? AND status='PENDING_SESSION'`, view.BindingID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("bind DB rows=%d err=%v", bindings, err)
	}
	if _, err := nativeIn.Write([]byte("INGEST\n")); err != nil {
		t.Fatal(err)
	}
	ackLine, err := nativeReader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("native ingest: %v stderr=%q", err, nativeErr.String())
	}
	var ack contract.BridgeIngressAck
	if err := json.Unmarshal(ackLine, &ack); err != nil || !ack.Committed || ack.EventID != "observation-r7" {
		t.Fatalf("ingest ack=%q err=%v", ackLine, err)
	}
	if strings.HasPrefix(secretMode, "bind_") {
		runCLI("bind", bind, "bridge_public_secret")
		var bindingStatus, businessStatus string
		if err := check.SQL().QueryRow(`SELECT status FROM bridge_bindings WHERE binding_id=?`, view.BindingID).Scan(&bindingStatus); err != nil {
			t.Fatal(err)
		}
		if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='route-result'`).Scan(&businessStatus); err != nil {
			t.Fatal(err)
		}
		if bindingStatus != "SESSION_OBSERVED" || businessStatus != "pending" {
			t.Fatalf("idempotent Bind changed state: binding=%s business=%s", bindingStatus, businessStatus)
		}
		return
	}
	owner := contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: view.BindingID, OwnerEpoch: view.OwnerEpoch, RequestID: "route-activate"}, OwnerMode: "local", OwnerCapabilityFile: ownerFile, ControlFile: controlFile}
	nativeSession := "session-r7"
	if secretMode != "" {
		nativeSession = secret
	}
	activate := contract.BridgeActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: nativeSession, ObservationEventID: ack.EventID, ObservationHash: ack.PayloadHash}
	badActivate := activate
	badActivate.ObservationHash = strings.Repeat("0", 64)
	runCLI("activate", badActivate, "bridge_session_mismatch")
	var status string
	if err := check.SQL().QueryRow(`SELECT status FROM bridge_bindings WHERE binding_id=?`, view.BindingID).Scan(&status); err != nil || status != "SESSION_OBSERVED" {
		t.Fatalf("invalid activate status=%s err=%v", status, err)
	}
	if strings.HasPrefix(secretMode, "activate_") {
		runCLI("activate", activate, "bridge_public_secret")
		var businessStatus string
		if err := check.SQL().QueryRow(`SELECT status FROM bridge_bindings WHERE binding_id=?`, view.BindingID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := check.SQL().QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='route-result'`).Scan(&businessStatus); err != nil {
			t.Fatal(err)
		}
		if status != "SESSION_OBSERVED" || businessStatus != "pending" {
			t.Fatalf("Activate half-committed: binding=%s business=%s", status, businessStatus)
		}
		return
	}
	var active contract.BridgeBindingView
	if err := json.Unmarshal(runCLI("activate", activate, ""), &active); err != nil || active.Status != "ACTIVE" {
		t.Fatalf("activate=%+v err=%v", active, err)
	}
	t.Logf("activate binding=%s observation=%s status=%s", active.BindingID, ack.EventID, active.Status)
	if _, err := nativeIn.Write([]byte("CLAIM_EMIT\n")); err != nil {
		t.Fatal(err)
	}
	emitLine, err := nativeReader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("native emit: %v stderr=%q", err, nativeErr.String())
	}
	var emitted struct {
		BatchID      string `json:"batch_id"`
		AttemptID    string `json:"attempt_id"`
		RenderSHA256 string `json:"render_sha256"`
	}
	if err := json.Unmarshal(emitLine, &emitted); err != nil || emitted.BatchID == "" || emitted.AttemptID == "" {
		t.Fatalf("emit=%q err=%v", emitLine, err)
	}
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: owner, BatchID: emitted.BatchID, AttemptID: emitted.AttemptID, Reason: "owner_explicit_retry"}
	runCLI("redeliver", redeliver, "bridge_redeliver_state")
	if err := nativeIn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := native.Wait(); err != nil {
		t.Fatalf("native exit=%v stderr=%q", err, nativeErr.String())
	}
	nativeExited = true
	cancelServe()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	<-serveDone
	restarted, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	restartCtx, cancelRestart := context.WithCancel(ctx)
	restartDone := make(chan struct{})
	go func() { defer close(restartDone); _ = restarted.Serve(restartCtx) }()
	defer func() { cancelRestart(); _ = restarted.Close(); <-restartDone }()
	var attemptState string
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, emitted.AttemptID).Scan(&attemptState); err != nil || attemptState != "UNCERTAIN" {
		t.Fatalf("restart attempt=%s err=%v", attemptState, err)
	}
	t.Logf("recovery batch=%s attempt=%s state=%s", emitted.BatchID, emitted.AttemptID, attemptState)
	wrongEpoch := redeliver
	wrongEpoch.OwnerEpoch++
	runCLI("redeliver", wrongEpoch, "bridge_no_active_binding")
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, emitted.AttemptID).Scan(&attemptState); err != nil || attemptState != "UNCERTAIN" {
		t.Fatalf("wrong epoch advanced=%s err=%v", attemptState, err)
	}
	wrongAttempt := redeliver
	wrongAttempt.AttemptID = "foreign-attempt"
	runCLI("redeliver", wrongAttempt, "bridge_attempt_mismatch")
	var reserved contract.BridgeAttemptView
	rawReserved := runCLI("redeliver", redeliver, "")
	if err := json.Unmarshal(rawReserved, &reserved); err != nil || reserved.State != "CLAIMED" || reserved.AttemptID == "" || reserved.AttemptID == emitted.AttemptID || bytes.Contains(rawReserved, []byte("claim_token")) {
		t.Fatalf("redeliver=%q err=%v", rawReserved, err)
	}
	if err := check.SQL().QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, emitted.AttemptID).Scan(&attemptState); err != nil || attemptState != "SUPERSEDED_UNCERTAIN" {
		t.Fatalf("old attempt=%s err=%v", attemptState, err)
	}
	var newState, newHash string
	if err := check.SQL().QueryRow(`SELECT state,claim_token_hash FROM bridge_delivery_attempts WHERE attempt_id=?`, reserved.AttemptID).Scan(&newState, &newHash); err != nil || newState != "CLAIMED" || newHash != "" {
		t.Fatalf("new attempt=%s token_hash=%q err=%v", newState, newHash, err)
	}
	t.Logf("redeliver old=%s old_state=%s new=%s new_state=%s private_token_hash_empty=%t", emitted.AttemptID, attemptState, reserved.AttemptID, newState, newHash == "")
}

func TestBridgeRequestFileRejectsSymlinkAndLooseMode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "request.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBridgeRequestFile(path); err == nil {
		t.Fatal("loose file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBridgeRequestFile(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if raw, err := readBridgeRequestFile(path); err != nil || string(raw) != `{"schema_version":1}` {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}
