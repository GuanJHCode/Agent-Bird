package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestLocalOwnerBindingDoesNotRequireNativeReturnTransport(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "local-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	message := callServer(t, server.SocketPath(), ipc.Kind("owner_bind"), map[string]any{"controller_thread": "local-master", "origin_pid": os.Getpid(), "origin_birth": birth})
	var receipt struct {
		OwnerCapability string `json:"owner_capability"`
		OriginContextID string `json:"origin_context_id"`
		HostGeneration  string `json:"host_generation"`
	}
	decodePayload(t, message, &receipt)
	if receipt.OwnerCapability == "" || receipt.OriginContextID == "" || receipt.HostGeneration == "" || strings.Contains(string(message.Payload), "token") {
		t.Fatalf("invalid owner receipt: %s", message.Payload)
	}
	info, err := os.Lstat(receipt.OwnerCapability)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("capability mode: %v", err)
	}
	request := map[string]any{"run_id": "local-run", "controller_thread": "local-master", "plan_revision": 1, "origin_context_id": receipt.OriginContextID, "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": receipt.HostGeneration, "owner_capability": receipt.OwnerCapability, "owner_mode": "local", "delivery_mode": "collect", "host_executable": "/private/bin/orchestrator", "tasks": []any{map[string]any{"id": "local-task", "max_attempts": 1, "adapter": map[string]any{"provider": "fake"}}}}
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, request)
	var submitted SubmitResponse
	decodePayload(t, response, &submitted)
	if submitted.Status != "queued" {
		t.Fatalf("submit=%s", response.Payload)
	}
	request["run_id"] = "spoof"
	request["controller_thread"] = "other"
	raw, _ := json.Marshal(request)
	denied, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindSubmit, RequestID: "denied", Payload: raw})
	if err != nil || denied.Kind != ipc.KindError {
		t.Fatalf("spoof allowed: %#v %v", denied, err)
	}
}

func TestLocalOwnerExplicitRebindPreservesRunContext(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "local-rebind-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	submitted := callServer(t, server.SocketPath(), ipc.KindSubmit, SubmitRequest{RunID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "original", OriginPID: 99999999, OriginBirth: "gone", HostGeneration: "old", HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}})
	var receipt SubmitResponse
	decodePayload(t, submitted, &receipt)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	bound := callServer(t, server.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "master", OriginPID: os.Getpid(), OriginBirth: birth})
	var owner map[string]any
	decodePayload(t, bound, &owner)
	rebound := callServer(t, server.SocketPath(), ipc.KindRebindOwner, map[string]any{"run_id": "run", "controller_thread": "master", "control_token": receipt.ControlToken, "origin_context_id": "original", "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": owner["host_generation"], "owner_mode": "local", "owner_capability": owner["owner_capability"]})
	var result map[string]any
	decodePayload(t, rebound, &result)
	if result["origin_context_id"] != "original" || result["status"] != "owner_rebound" {
		t.Fatalf("context changed: %#v", result)
	}
}

func TestWorkerPeerCannotUseMainAgentControlKinds(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "worker-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	submitted := callServer(t, server.SocketPath(), ipc.KindSubmit, SubmitRequest{RunID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1}}})
	var receipt SubmitResponse
	decodePayload(t, submitted, &receipt)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: os.Getpid(), Birth: birth, Executable: "/private/bin/orchestrator"}, server.epoch); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(TaskControlRequest{TaskID: "task", ControllerThread: "master", ControlToken: receipt.ControlToken})
	for _, kind := range []ipc.Kind{ipc.KindRecoverHost, ipc.KindResume, ipc.KindRetry, ipc.KindRework, ipc.KindAccept, ipc.KindAnswer, ipc.KindStopTask, ipc.KindAck, ipc.KindCollect, ipc.KindWaitEvents, ipc.KindSummary, ipc.KindStatus} {
		response, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: kind, RequestID: "worker", Payload: raw})
		if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "worker_dispatch_forbidden") {
			t.Errorf("worker %s reached controller path: %s %v", kind, response.Payload, err)
		}
	}
}

func TestUnidentifiedLaunchCannotEnrollThroughNativeSubmit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "owner-enroll-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	bind := OwnerBindRequest{ControllerThread: "master", OriginPID: os.Getpid(), OriginBirth: birth}
	bound := callServer(t, server.SocketPath(), ipc.KindOwnerBind, bind)
	var owner struct {
		Path       string `json:"owner_capability"`
		Context    string `json:"origin_context_id"`
		Generation string `json:"host_generation"`
	}
	decodePayload(t, bound, &owner)
	request := SubmitRequest{OwnerMode: "local", OwnerCapability: owner.Path, DeliveryMode: "collect", RunID: "first", ControllerThread: "master", PlanRevision: 1, OriginContextID: owner.Context, OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: owner.Generation, HostExecutable: "/private/bin/host", Tasks: []TaskRequest{{ID: "first", MaxAttempts: 1}}}
	var receipt SubmitResponse
	decodePayload(t, callServer(t, server.SocketPath(), ipc.KindSubmit, request), &receipt)
	host, err := server.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: owner.Context, OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: owner.Generation, PID: 9, Birth: "host", Executable: "/private/bin/host"}, server.epoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.ClaimReady(ctx, host, server.epoch, 2); err != nil {
		t.Fatal(err)
	}
	// Existing authenticated local masters retain dispatch while new enrollment
	// waits for the worker identity. Native submit must not bypass that wait.
	request.RunID, request.Tasks[0].ID = "second", "second"
	callServer(t, server.SocketPath(), ipc.KindSubmit, request)
	request.RunID, request.Tasks[0].ID = "bypass", "bypass"
	request.OwnerMode, request.OwnerCapability = "native", ""
	for _, input := range []struct {
		kind    ipc.Kind
		payload any
	}{{ipc.KindOwnerBind, bind}, {ipc.KindSubmit, request}} {
		raw, _ := json.Marshal(input.payload)
		response, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: input.kind, RequestID: "enroll", Payload: raw})
		if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_registration_deferred") {
			t.Errorf("unidentified launch allowed %s: response_kind=%s err=%v", input.kind, response.Kind, err)
		}
	}
}

func TestUnidentifiedLaunchDefersUnboundPeerDispatchControls(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "owner-controls-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	request := SubmitRequest{RunID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "gone", HostGeneration: "gen", HostExecutable: "/private/bin/host", Tasks: []TaskRequest{{ID: "task", MaxAttempts: 1}}}
	var receipt SubmitResponse
	decodePayload(t, callServer(t, server.SocketPath(), ipc.KindSubmit, request), &receipt)
	host, err := server.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "gone", HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, server.epoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.ClaimReady(ctx, host, server.epoch, 2); err != nil {
		t.Fatal(err)
	}
	control := TaskControlRequest{TaskID: "task", ControllerThread: "master", ControlToken: receipt.ControlToken}
	raw, _ := json.Marshal(control)
	for _, kind := range []ipc.Kind{ipc.KindRecoverHost, ipc.KindRebindOwner, ipc.KindResume, ipc.KindRetry, ipc.KindRework, ipc.KindAccept, ipc.KindAnswer} {
		response, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: kind, RequestID: "control", Payload: raw})
		if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_registration_deferred") {
			t.Errorf("unbound peer reached %s: response_kind=%s err=%v", kind, response.Kind, err)
		}
	}
	// Inspecting state still requires the original token, but never dispatches.
	callServer(t, server.SocketPath(), ipc.KindStatus, control)
}
