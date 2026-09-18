package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestSubmissionCannotReachCoordinatorBeforeDurableHandle(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	root, err := os.MkdirTemp("/tmp", "bird-receipt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer server.Close()
	defer cancel()
	go server.Serve(ctx)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "submit.json")
	if err = writeExclusiveJSON(request, map[string]any{"run_id": "receipt-run", "plan_revision": 1, "controller_thread": "receipt-owner", "origin_context_id": "receipt-context", "origin_pid": os.Getpid(), "origin_birth": birth, "host_generation": "receipt-generation", "delivery_mode": "collect", "tasks": []any{map[string]any{"id": "receipt-task", "max_attempts": 1, "max_active_ms": 1000, "adapter": map[string]any{"kind": "fake", "args": []string{"/usr/bin/false"}, "directory": root}}}}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("receipt storage unavailable")
	called := 0
	var held controlCapability
	err = submitWithPreparedControl(ctx, []string{"--state-dir", state, "--request", request}, &bytes.Buffer{}, func(path string) error {
		called++
		var e error
		held, e = loadControlCapability(path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = loadHostBootstrap(held); e != nil {
			t.Fatal(e)
		}
		return stop
	})
	if !errors.Is(err, stop) || called != 1 {
		t.Fatalf("receipt barrier bypassed: %d %v", called, err)
	}
	raw, _ := json.Marshal(coordinator.TaskControlRequest{TaskID: "receipt-task", ControllerThread: held.ControllerThread, ControlToken: held.ControlToken})
	response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindSummary, RequestID: "verify-not-submitted", Payload: raw})
	if err != nil || response.Kind != ipc.KindError {
		t.Fatalf("task reached coordinator: %+v %v", response, err)
	}
	if _, err = os.Stat(filepath.Join(state, "host-spool")); !os.IsNotExist(err) {
		t.Fatalf("host started despite receipt failure: %v", err)
	}
}
