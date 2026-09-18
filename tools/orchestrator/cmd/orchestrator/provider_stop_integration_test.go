package main

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Real owned child processes and source Hosts; no model, login or provider
// credentials. Two owner scopes exercise the same provider concurrently.
func TestProviderDisableStopsOnlyItsOwnersRealProcess(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "provider-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, _ = filepath.EvalSymlinks(root)
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]map[string]any{}
	controls := map[string]string{}
	request := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(request, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Bind both before any launch intent exists; no enrollment-guard bypass.
	for _, id := range []string{"a", "b"} {
		write(coordinator.OwnerBindRequest{ControllerThread: id, OriginPID: os.Getpid(), OriginBirth: birth})
		var owner map[string]any
		if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", request), &owner); err != nil {
			t.Fatal(err)
		}
		delete(owner, "version")
		owners[id] = owner
	}
	snapshot := func(id string) store.TaskSnapshot {
		t.Helper()
		var s store.TaskSnapshot
		if err := json.Unmarshal(runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", id, "--control-file", controls[id]), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, id := range []string{"a", "b"} {
		owner := owners[id]
		owner["run_id"] = id
		owner["plan_revision"] = 1
		owner["delivery_mode"] = "collect"
		owner["tasks"] = []any{map[string]any{"id": id, "max_attempts": 1, "max_active_ms": 25000, "adapter": map[string]any{"kind": "fake", "provider": "claude-code", "args": []string{"/bin/sleep", "25"}, "directory": root}}}
		write(owner)
		var submitted struct {
			ControlFile string `json:"control_file"`
		}
		if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", request), &submitted); err != nil {
			t.Fatal(err)
		}
		controls[id] = submitted.ControlFile
		for {
			s := snapshot(id)
			if s.Process != nil && s.SegmentStatus == "running" {
				break
			}
			if ctx.Err() != nil {
				t.Fatalf("not running: %+v", s)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	a, b := snapshot("a"), snapshot("b")
	control := func(id, action string) store.ProviderState {
		t.Helper()
		raw, _ := json.Marshal(coordinator.ProviderControlRequest{OwnerCapability: owners[id]["owner_capability"].(string), Provider: "claude-code", Action: action})
		response, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: 1, Kind: ipc.KindProviderControl, RequestID: id + action, Payload: raw})
		if err != nil || response.Kind == ipc.KindError {
			t.Fatalf("control %s %s: %s %v", id, action, response.Payload, err)
		}
		var result store.ProviderState
		if err = json.Unmarshal(response.Payload, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	control("a", "disable")
	for {
		r := control("a", "status")
		if r.Status == "disabled" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("stop not confirmed", r)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err = syscall.Kill(a.Process.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("closed worker still exists: %v", err)
	}
	still := snapshot("b")
	if still.Process == nil || still.Process.PID != b.Process.PID || still.SegmentStatus != "running" {
		t.Fatalf("other owner's worker affected: %+v", still)
	}
	if actual, err := process.Birth(b.Process.PID); err != nil || actual != b.Process.Birth {
		t.Fatalf("other process not alive: %s %v", actual, err)
	}
	control("a", "enable")
	if snapshot("a").SegmentStatus != "exited" {
		t.Fatal("enable restarted old task")
	}
	control("b", "disable")
	for {
		if control("b", "status").Status == "disabled" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("cleanup stop not confirmed")
		}
		time.Sleep(30 * time.Millisecond)
	}
}
