package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestBatchReleasedGlobalSlotWakesAnotherOwnerHost(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state, binary := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "runtime-limits.json"), []byte(`{"version":1,"global":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopCoordinator(t, state) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runBinary(t, ctx, binary, "ensure-running", "--state-dir", state)
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(request, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	controls := map[string]string{}
	release := filepath.Join(root, "release")
	for index := 0; index < 2; index++ {
		name := fmt.Sprintf("owner-%d", index)
		write(map[string]any{"controller_thread": name, "origin_pid": os.Getpid(), "origin_birth": birth})
		var owner map[string]any
		if err := json.Unmarshal(runBinary(t, ctx, binary, "owner-bind", "--state-dir", state, "--request", request), &owner); err != nil {
			t.Fatal(err)
		}
		delete(owner, "version")
		owner["run_id"], owner["plan_revision"], owner["delivery_mode"] = name, 1, "collect"
		command := "echo second-result"
		if index == 0 {
			command = fmt.Sprintf("while [ ! -f %s ]; do sleep 0.02; done; echo first-result", release)
		}
		owner["tasks"] = []any{map[string]any{"id": name, "max_attempts": 1, "max_active_ms": 15000, "completion_policy": "owner_review", "adapter": map[string]any{"kind": "fake", "directory": root, "args": []string{"/bin/sh", "-c", command}}}}
		write(owner)
		var submitted struct {
			ControlFile string `json:"control_file"`
		}
		if err := json.Unmarshal(runBinary(t, ctx, binary, "submit", "--state-dir", state, "--request", request), &submitted); err != nil {
			t.Fatal(err)
		}
		controls[name] = submitted.ControlFile
		for {
			status := recoveryTaskStatus(t, ctx, binary, state, name, submitted.ControlFile)
			if status.HostStatus == "ready" && (index == 1 || status.SegmentStatus == "running") {
				break
			}
			if ctx.Err() != nil {
				t.Fatalf("not ready: %+v", status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	queued := recoveryTaskStatus(t, ctx, binary, state, "owner-1", controls["owner-1"])
	if queued.AttemptID != "" {
		t.Fatalf("global slot was not exclusive: %+v", queued)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(4 * time.Second)
	for {
		first := recoveryTaskStatus(t, ctx, binary, state, "owner-0", controls["owner-0"])
		second := recoveryTaskStatus(t, ctx, binary, state, "owner-1", controls["owner-1"])
		if first.SegmentStatus == "exited" && second.Status == "result_ready" && second.SegmentStatus == "exited" {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("released global slot did not wake other host: first=%+v second=%+v", first, second)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
