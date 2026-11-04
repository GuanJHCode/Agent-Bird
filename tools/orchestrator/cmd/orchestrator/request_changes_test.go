package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
)

func TestOwnerRequestChangesRequiresTaskIDForMultipleTasks(t *testing.T) {
	root := privateTaskTemp(t)
	handle := filepath.Join(root, "handle.json")
	path := filepath.Join(root, "request.json")
	h := taskHandle{Version: 1, RunID: "run", StateDir: root, ControlFile: "/missing", TaskIDs: []string{"one", "two"}}
	if err := writeExclusiveJSON(handle, h); err != nil {
		t.Fatal(err)
	}
	b := ownerChangeBrief{WorkRevision: 1, CandidateOID: strings.Repeat("a", 40), Feedback: "fix", Acceptance: "test", CommandID: "command"}
	if err := writeExclusiveJSON(path, b); err != nil {
		t.Fatal(err)
	}
	if err := taskRequestChanges(context.Background(), []string{"--handle", handle, "--request", path}, io.Discard); err == nil || err.Error() != "task_id_required" {
		t.Fatalf("got %v", err)
	}
	b.TaskID = "other"
	if err := replacePrivateJSON(path, b); err != nil {
		t.Fatal(err)
	}
	if err := taskRequestChanges(context.Background(), []string{"--handle", handle, "--request", path}, io.Discard); err == nil || err.Error() != "task_handle_scope_mismatch" {
		t.Fatalf("got %v", err)
	}
}

func TestOwnerRequestChangesRejectsOldCoordinatorBeforeMutation(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "old-owner-coord-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	kinds := make(chan ipc.Kind, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			message, err := ipc.Read(conn)
			if err != nil {
				conn.Close()
				continue
			}
			kinds <- message.Kind
			payload := json.RawMessage(`{"status":"ready","epoch":1,"capabilities":["interruption_feedback_v1","grok_readonly_v1","session_provider_lifecycle_v1"]}`)
			_ = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: 1, Payload: payload})
			conn.Close()
		}
	}()
	bootstrap := hostBootstrap{Version: 2, ProducerID: "launch", SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "spool"), Hello: contract.HostHello{LaunchID: "launch", LaunchToken: "token"}}
	bootstrapPath, err := writeBootstrap(state, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(state, "run", "owner", "control-token", bootstrapPath, "launch", "token")
	if err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: control, TaskIDs: []string{"task"}}); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	if err = writeExclusiveJSON(request, ownerChangeBrief{WorkRevision: 1, CandidateOID: strings.Repeat("a", 40), Feedback: "fix", Acceptance: "test", CommandID: "command"}); err != nil {
		t.Fatal(err)
	}
	if err = taskRequestChanges(context.Background(), []string{"--handle", handle, "--request", request}, io.Discard); err == nil || err.Error() != "coordinator_upgrade_required" {
		t.Fatalf("got %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "owner-decisions")); !os.IsNotExist(err) {
		t.Fatalf("old coordinator allocated owner decision: %v", err)
	}
	if kind := <-kinds; kind != ipc.KindReady {
		t.Fatalf("sent mutation before negotiation: %s", kind)
	}
	select {
	case kind := <-kinds:
		t.Fatalf("unexpected RPC %s", kind)
	default:
	}
	caps := []string{"interruption_feedback_v1"}
	if coordinatorHasOwnerChanges(caps) || checkCoordinatorTasks(caps, nil) != nil {
		t.Fatal("old coordinator no longer supports old tasks")
	}
	hello := contract.HostHello{LaunchID: "old"}
	hello.OwnerRequestChangesV2 = coordinatorHasOwnerChanges(caps)
	body, err := json.Marshal(hello)
	if err != nil || strings.Contains(string(body), "owner_request_changes_v2") {
		t.Fatalf("new hello field leaks to strict old coordinator: %s %v", body, err)
	}
}
