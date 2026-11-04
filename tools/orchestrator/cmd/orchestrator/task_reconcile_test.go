package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func TestReconcileFailurePreservesReceiptAndNeverStartsCoordinator(t *testing.T) {
	for _, scenario := range []string{"offline", "foreign-run", "foreign-task", "foreign-reply"} {
		t.Run(scenario, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "bird-reconcile-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			path := filepath.Join(root, "handle.json")
			h := taskHandle{Version: 1, RunID: "run", StateDir: root, TaskIDs: []string{"task"}, Status: "submitting", ControlFile: "original-control"}
			if err := writeExclusiveJSON(path, h); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if scenario != "offline" {
				listener, err := net.Listen("unix", filepath.Join(root, "coordinator.sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					req, err := ipc.Read(conn)
					if err != nil {
						return
					}
					run, task, reply := "run", "task", req.RequestID
					switch scenario {
					case "foreign-run":
						run = "foreign"
					case "foreign-task":
						task = "foreign"
					case "foreign-reply":
						reply = "foreign"
					}
					raw, _ := json.Marshal(map[string]any{"version": 1, "run_id": run, "tasks": []any{map[string]any{"task_id": task}}})
					_ = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindResponse, RequestID: reply, Payload: raw})
				}()
			}
			if err := reconcileTaskHandle(context.Background(), path, h, controlCapability{}, io.Discard); err == nil {
				t.Fatal("unverified receipt reconciled")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("failure changed original handle")
			}
			for _, name := range []string{"coordinator.pid", "host-spool"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatalf("unexpected runtime mutation: %s", name)
				}
			}
		})
	}
}

func TestSubmitUncertainReconcileLostReplyPreservesCheckpointThenReplays(t *testing.T) {
	root, tempErr := os.MkdirTemp("/private/tmp", "bird-uncertain-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "handle.json")
	h := taskHandle{Version: 1, RunID: "run", StateDir: root, TaskIDs: []string{"task"}, Status: "submit_uncertain", ControlFile: "original-control"}
	if err := writeExclusiveJSON(path, h); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	listener, err := net.Listen("unix", filepath.Join(root, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	served := make(chan int, 1)
	go func() {
		count := 0
		for count < 3 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			req, err := ipc.Read(conn)
			if err != nil {
				conn.Close()
				return
			}
			if req.Kind != ipc.KindSummary {
				conn.Close()
				return
			}
			count++
			if count > 1 {
				raw := []byte(`{"version":1,"run_id":"run","tasks":[{"task_id":"task"}]}`)
				_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: req.RequestID, Payload: raw})
			}
			// The first committed observation loses its response on the transport.
			conn.Close()
		}
		served <- count
	}()
	if err := reconcileTaskHandle(context.Background(), path, h, controlCapability{}, io.Discard); err == nil || err.Error() != "task_reconciliation_unavailable" {
		t.Fatalf("lost response=%v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("lost response changed original capability/handle")
	}
	if err := reconcileTaskHandle(context.Background(), path, h, controlCapability{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var recovered taskHandle
	if err := readPrivateJSON(path, &recovered); err != nil || recovered.Status != "reconciled" || recovered.ControlFile != h.ControlFile {
		t.Fatalf("recovered=%+v %v", recovered, err)
	}
	if err := reconcileTaskHandle(context.Background(), path, recovered, controlCapability{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count := <-served; count != 3 {
		t.Fatalf("requests=%d", count)
	}
	for _, name := range []string{"coordinator.pid", "host-spool"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected launch mutation %s", name)
		}
	}
}

func TestQueuedHandleDoesNotRequireReconciliation(t *testing.T) {
	h := taskHandle{Status: "queued"}
	if err := reconcileTaskHandle(context.Background(), "unused", h, controlCapability{}, io.Discard); err == nil || err.Error() != "task_reconciliation_not_required" {
		t.Fatalf("queued reconciliation=%v", err)
	}
}

func TestLostSubmitReplyRetainsActualUncertainReceiptAndOriginalCapability(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	root, tempErr := os.MkdirTemp("/private/tmp", "bird-uncertain-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := net.Listen("unix", filepath.Join(root, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	submitted := make(chan coordinator.SubmitRequest, 2)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			req, err := ipc.Read(conn)
			if err != nil {
				conn.Close()
				continue
			}
			var body []byte
			switch req.Kind {
			case ipc.KindReady:
				body = []byte(`{"status":"ready","epoch":1,"capabilities":["interruption_feedback_v1"]}`)
			case ipc.KindSubmit:
				var value coordinator.SubmitRequest
				if json.Unmarshal(req.Payload, &value) == nil {
					submitted <- value
				}
				conn.Close()
				continue // Persisted submission, deliberately lost reply.
			case ipc.KindSummary:
				body = []byte(`{"version":1,"run_id":"run","tasks":[{"task_id":"work"}]}`)
			default:
				conn.Close()
				continue
			}
			_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: req.RequestID, Epoch: 1, Payload: body})
			conn.Close()
		}
	}()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "submit.json")
	req := coordinator.SubmitRequest{RunID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation", Tasks: []coordinator.TaskRequest{{ID: "work", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}}
	if err := writeExclusiveJSON(request, req); err != nil {
		t.Fatal(err)
	}
	h := taskHandle{Version: 1, RunID: "run", StateDir: root, TaskIDs: []string{"work"}, Status: "submitting"}
	handle := filepath.Join(root, "handle.json")
	if err := writeExclusiveJSON(handle, h); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var receipt bytes.Buffer
	err = submitWithPreparedControl(ctx, []string{"--state-dir", root, "--request", request}, &receipt, func(control string) error { h.ControlFile = control; return replacePrivateJSON(handle, h) })
	if err == nil || err.Error() != "submit_uncertain" {
		t.Fatalf("submit=%s %v", receipt.Bytes(), err)
	}
	var result struct {
		Status      string `json:"status"`
		ControlFile string `json:"control_file"`
	}
	if err := json.Unmarshal(receipt.Bytes(), &result); err != nil || result.Status != "submit_uncertain" || result.ControlFile != h.ControlFile {
		t.Fatalf("receipt=%s %v", receipt.Bytes(), err)
	}
	// This is the exact status/control projection persisted by taskSubmit.
	h.Status = result.Status
	if err := replacePrivateJSON(handle, h); err != nil {
		t.Fatal(err)
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(h.ControlFile)
	for i := 0; i < 2; i++ {
		if err := reconcileTaskHandle(ctx, handle, h, cap, io.Discard); err != nil {
			t.Fatal(err)
		}
		if err := readPrivateJSON(handle, &h); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(h.ControlFile)
	if string(before) != string(after) {
		t.Fatal("reconciliation replaced original capability")
	}
	select {
	case original := <-submitted:
		if original.ControlToken != cap.ControlToken || original.RunID != h.RunID {
			t.Fatal("original submission binding changed")
		}
	default:
		t.Fatal("no actual submit received")
	}
	select {
	case <-submitted:
		t.Fatal("reconciliation repeated submit")
	default:
	}
}
