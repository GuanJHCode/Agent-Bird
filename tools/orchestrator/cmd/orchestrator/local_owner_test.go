package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCLILocalOwnerCollectWithoutNativeBridge(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "local-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"controller_thread": "master", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", path), &owner); err != nil {
		t.Fatal(err)
	}
	if owner["token"] != nil {
		t.Fatal("token leaked to stdout")
	}
	delete(owner, "version")
	owner["run_id"] = "local"
	owner["plan_revision"] = 1
	owner["delivery_mode"] = "collect"
	owner["tasks"] = []any{map[string]any{"id": "task", "max_attempts": 1, "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "printf result"}, "directory": root}}}
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", path), &submitted); err != nil {
		t.Fatal(err)
	}
	for {
		s := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile)
		if s.Status == "result_ready" {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("status=%s", s.Status)
		}
		time.Sleep(30 * time.Millisecond)
	}
	var collection struct {
		DeliveryID            string           `json:"delivery_id"`
		CollectionProofSHA256 string           `json:"collection_proof_sha256"`
		Events                []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "task", "--control-file", submitted.ControlFile), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.CollectionProofSHA256 == "" || len(collection.Events) != 1 {
		t.Fatalf("missing receipt: %#v", collection)
	}
	event := collection.Events[0]
	write(map[string]any{"version": 1, "task_id": "task", "control_file": submitted.ControlFile,
		"delivery_id": collection.DeliveryID, "collection_proof_sha256": collection.CollectionProofSHA256,
		"decisions": []map[string]any{{"event_id": event.EventID, "event_revision": event.EventRevision,
			"event_hash": event.PayloadHash, "action_slot": event.ActionSlot, "decision": "handled", "command_id": "ack-result"}}})
	for i := 0; i < 2; i++ {
		runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", path)
	}
	if snapshot := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile); snapshot.Status != "result_ready" {
		t.Fatalf("delivery ACK accepted business result: %+v", snapshot)
	}
	for i := 0; i < 2; i++ {
		runBinary(t, ctx, bin, "accept", "--state-dir", state, "--task-id", "task", "--control-file", submitted.ControlFile,
			"--work-revision", "1", "--event-id", event.EventID, "--event-revision", strconv.FormatInt(event.EventRevision, 10),
			"--event-hash", event.PayloadHash, "--action-slot", event.ActionSlot, "--decision", "accept", "--command-id", "accept-result")
	}
	if snapshot := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile); snapshot.Status != "completed" {
		t.Fatalf("result acceptance did not complete task: %+v", snapshot)
	}
}
