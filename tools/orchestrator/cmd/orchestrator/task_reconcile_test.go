package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
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
