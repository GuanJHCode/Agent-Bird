package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

func projectionServer(t *testing.T) (*Server, context.Context, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "owner-projection-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = s.Close() })
	go s.Serve(ctx)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, birth
}

func TestManagedOwnerProjectionNeverClaimsNativeChatBinding(t *testing.T) {
	s, _, birth := projectionServer(t)
	for _, thread := range []string{"managed-claude-process", "00000000-0000-0000-0000-000000000001", "mcp-session-123"} {
		t.Run(thread, func(t *testing.T) {
			response := callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{OwnerProjectionVersion: 1, ControllerThread: thread, OriginPID: os.Getpid(), OriginBirth: birth})
			var owner OwnerBindResponse
			// Also checks the exact response schema used by strict CLI consumers.
			if err := strictJSON(response.Payload, &owner); err != nil {
				t.Fatal(err)
			}
			want := OwnerCapabilities{Version: 1, Delivery: OwnerDeliveryCapabilities{Collect: true, Wait: true}, Continuation: OwnerContinuationCapabilities{FreshInvocation: true, ExplicitRebind: true}, ParentExitPolicy: "stop_and_retain"}
			if owner.Version != 1 || owner.OwnerMode != "local" || owner.OwnerScope != "managed_process" || owner.OwnerCapabilities == nil || !reflect.DeepEqual(*owner.OwnerCapabilities, want) {
				t.Fatalf("unsafe projection: %+v", owner)
			}
			raw, err := os.ReadFile(owner.OwnerCapability)
			if err != nil {
				t.Fatal(err)
			}
			var grant store.OwnerGrant
			if err := strictJSON(raw, &grant); err != nil {
				t.Fatalf("projection changed capability schema: %v", err)
			}
			if strings.Contains(string(raw), "owner_scope") || strings.Contains(string(raw), "owner_capabilities") {
				t.Fatal("projection became authority")
			}
			if grant.HostGeneration != owner.HostGeneration || grant.ControllerThread != thread {
				t.Fatal("projection diverged from authority")
			}
		})
	}
}

func TestLocalOwnerDeliveryCapabilityIsEnforcedAtProtocolBoundary(t *testing.T) {
	s, ctx, birth := projectionServer(t)
	var owner OwnerBindResponse
	decodePayload(t, callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "master", OriginPID: os.Getpid(), OriginBirth: birth}), &owner)
	for _, mode := range []string{"native", "invalid", "", "collect"} {
		name := mode
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			req := SubmitRequest{OwnerMode: "local", OwnerCapability: owner.OwnerCapability, DeliveryMode: mode, RunID: name, ControllerThread: owner.ControllerThread, PlanRevision: 1, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration, HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: name, MaxAttempts: 1}}}
			raw, _ := json.Marshal(req)
			response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindSubmit, RequestID: name, Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "" && mode != "collect" {
				if response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "managed_owner_native_delivery_unavailable") {
					t.Fatalf("native delivery allowed: %s", response.Payload)
				}
				if _, err := s.db.TaskSnapshot(ctx, name); err == nil {
					t.Fatal("denied submit persisted task")
				}
				return
			}
			if response.Kind != ipc.KindResponse {
				t.Fatalf("collect rejected: %s", response.Payload)
			}
			got, err := s.db.DeliveryMode(ctx, name)
			if err != nil || got != "collect" {
				t.Fatalf("delivery mode=%q err=%v", got, err)
			}
		})
	}
}

func TestLocalOwnerProjectionCannotReplaceAuthority(t *testing.T) {
	s, ctx, birth := projectionServer(t)
	var owner OwnerBindResponse
	decodePayload(t, callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "master", OriginPID: os.Getpid(), OriginBirth: birth}), &owner)
	original, err := os.ReadFile(owner.OwnerCapability)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"wrong_birth", "wrong_pid", "cross_owner", "stale_generation", "invalid_token", "projection_authority"} {
		t.Run(kind, func(t *testing.T) {
			defer func() {
				if err := os.WriteFile(owner.OwnerCapability, original, 0600); err != nil {
					t.Error(err)
				}
			}()
			req := SubmitRequest{OwnerMode: "local", OwnerCapability: owner.OwnerCapability, DeliveryMode: "native", RunID: kind, ControllerThread: owner.ControllerThread, PlanRevision: 1, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, HostGeneration: owner.HostGeneration, HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: kind, MaxAttempts: 1}}}
			switch kind {
			case "wrong_birth":
				req.OriginBirth = "old-birth-at-reused-pid"
			case "wrong_pid":
				req.OriginPID++
			case "cross_owner":
				req.ControllerThread = "another-owner"
			case "stale_generation":
				req.HostGeneration = "old-generation"
			case "invalid_token":
				var grant store.OwnerGrant
				if err := json.Unmarshal(original, &grant); err != nil {
					t.Fatal(err)
				}
				grant.Token = "invalidated-token"
				raw, _ := json.Marshal(grant)
				if err := os.WriteFile(owner.OwnerCapability, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "projection_authority":
				var grant map[string]any
				if err := json.Unmarshal(original, &grant); err != nil {
					t.Fatal(err)
				}
				grant["owner_scope"] = "verified_native_session"
				raw, _ := json.Marshal(grant)
				if err := os.WriteFile(owner.OwnerCapability, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(req)
			response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindSubmit, RequestID: kind, Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			// Authority is checked before the informational delivery policy gate.
			if response.Kind != ipc.KindError || (!strings.Contains(string(response.Payload), "owner_capability_mismatch") && !strings.Contains(string(response.Payload), "owner_capability_invalid")) {
				t.Fatalf("invalid authority response: %s", response.Payload)
			}
		})
	}
	// Kernel peer ancestry, not a purported conversation label, authenticates bind.
	for _, req := range []OwnerBindRequest{{ControllerThread: "master", OriginPID: os.Getpid(), OriginBirth: "old-birth-at-reused-pid"}, {ControllerThread: "master", OriginPID: 1, OriginBirth: birth}} {
		raw, _ := json.Marshal(req)
		response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindOwnerBind, RequestID: "wrong-peer", Payload: raw})
		if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_peer_mismatch") {
			t.Fatalf("forged bind accepted: %s %v", response.Payload, err)
		}
	}
}

func TestOwnerProjectionRequiresVersionedOptIn(t *testing.T) {
	s, ctx, birth := projectionServer(t)
	legacy := callServer(t, s.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: "legacy", OriginPID: os.Getpid(), OriginBirth: birth})
	// This is the pre-extension strict consumer schema. Default requests must
	// continue to decode without knowing either projection field.
	var old struct {
		Version          int    `json:"version"`
		OwnerMode        string `json:"owner_mode"`
		OwnerCapability  string `json:"owner_capability"`
		ControllerThread string `json:"controller_thread"`
		OriginPID        int    `json:"origin_pid"`
		OriginBirth      string `json:"origin_birth"`
		OriginContextID  string `json:"origin_context_id"`
		HostGeneration   string `json:"host_generation"`
	}
	if err := strictJSON(legacy.Payload, &old); err != nil {
		t.Fatalf("legacy strict consumer broken: %v", err)
	}
	var shared OwnerBindResponse
	if err := strictJSON(legacy.Payload, &shared); err != nil || shared.OwnerScope != "" || shared.OwnerCapabilities != nil {
		t.Fatalf("legacy response gained projection: %s %v", legacy.Payload, err)
	}
	for _, version := range []int{-1, 2} {
		raw, _ := json.Marshal(OwnerBindRequest{OwnerProjectionVersion: version, ControllerThread: "unsupported", OriginPID: os.Getpid(), OriginBirth: birth})
		response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindOwnerBind, RequestID: "unsupported", Payload: raw})
		if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_projection_version_unsupported") {
			t.Fatalf("unsupported version accepted: %s %v", response.Payload, err)
		}
	}
	ready := callServer(t, s.SocketPath(), ipc.KindReady, map[string]any{})
	if !strings.Contains(string(ready.Payload), "managed_owner_projection_v1") {
		t.Fatalf("projection not advertised: %s", ready.Payload)
	}
}

func TestLocalOwnerOriginalControlCanStopAfterOwnerExit(t *testing.T) {
	s, ctx, _ := projectionServer(t)
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "old-run", ControllerThread: "old-owner", PlanRevision: 1, OriginContextID: "old-context", OriginPID: 99999999, OriginBirth: "retired-owner", DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: "old-context", HostGeneration: "old-generation", Executable: "/private/bin/orchestrator"}, Tasks: []store.TaskSpec{{ID: "old-task", RunID: "old-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	// docs/provider-runtime.md preserves query and stop via the original control
	// capability after owner loss. This is termination authority, not a rebind.
	for _, thread := range []string{"different-owner", "old-owner"} {
		raw, _ := json.Marshal(TaskControlRequest{TaskID: "old-task", ControllerThread: thread, ControlToken: receipt.ControlToken, WorkRevision: 1})
		response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindStopTask, RequestID: thread, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		if thread != "old-owner" {
			if response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), "owner_mismatch") {
				t.Fatalf("cross-owner control tuple accepted: %s", response.Payload)
			}
			continue
		}
		var status StatusResponse
		if response.Kind != ipc.KindResponse || json.Unmarshal(response.Payload, &status) != nil || status.Status != "cancelled" {
			t.Fatalf("original control cannot terminate after owner loss: %s", response.Payload)
		}
	}
	binding, err := s.db.ValidateRunOwner(ctx, "old-run", "old-owner", receipt.ControlToken)
	if err != nil || binding.OriginPID != 99999999 || binding.OriginBirth != "retired-owner" || binding.OriginContextID != "old-context" {
		t.Fatalf("stop implicitly rebound owner: %+v %v", binding, err)
	}
}

func TestManagedOwnerDeliveryGateDoesNotChangeLegacyNativeRoute(t *testing.T) {
	s, ctx, birth := projectionServer(t)
	for _, mode := range []string{"", "native"} {
		id := "native-explicit"
		if mode == "" {
			id = "native-default"
		}
		// This exercises only the existing native submission branch and persisted
		// delivery mode. It is not native attachment or live-parent routing proof.
		request := SubmitRequest{OwnerMode: mode, RunID: id, ControllerThread: "legacy-native", PlanRevision: 1, OriginContextID: "legacy-origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "legacy-generation", HostExecutable: "/private/bin/orchestrator", Tasks: []TaskRequest{{ID: id, MaxAttempts: 1}}}
		response := callServer(t, s.SocketPath(), ipc.KindSubmit, request)
		if response.Kind != ipc.KindResponse {
			t.Fatalf("legacy native rejected: %s", response.Payload)
		}
		got, err := s.db.DeliveryMode(ctx, id)
		if err != nil || got != "native" {
			t.Fatalf("legacy delivery changed: %q %v", got, err)
		}
	}
}
