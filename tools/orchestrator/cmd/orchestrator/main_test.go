package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func TestCLIStartsServiceSourceHostAndCollectsDurableResult(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real multi-process entrypoint")
	}
	root, err := os.MkdirTemp("/tmp", "g1-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	t.Cleanup(func() {
		if data, readErr := os.ReadFile(filepath.Join(state, "coordinator.pid")); readErr == nil {
			var pid int
			_, _ = fmt.Sscanf(string(data), "%d", &pid)
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGTERM)
				time.Sleep(100 * time.Millisecond)
			}
		}
	})
	requestPath := filepath.Join(root, "request.json")
	bin := filepath.Join(root, "orchestrator")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = os.Environ()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build: %v\n%s", buildErr, output)
	}
	originBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"run_id": "cli-run", "controller_thread": "cli-thread", "plan_revision": 1,
		"origin_context_id": "cli-origin", "origin_pid": os.Getpid(), "origin_birth": originBirth,
		"host_generation": "generation-1",
		"tasks": []any{
			map[string]any{"id": "task-a", "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sleep", "0.2"}, "directory": root}},
			map[string]any{"id": "task-b", "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sleep", "0.2"}, "directory": root}},
			map[string]any{"id": "task-c", "dependencies": []string{"task-a", "task-b"}, "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sleep", "0.2"}, "directory": root}},
			map[string]any{"id": "task-d", "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "if [ -e " + filepath.Join(root, "resume-marker") + " ]; then exit 0; else : > " + filepath.Join(root, "resume-marker") + "; sleep 30; fi"}, "directory": root}},
		},
	}
	data, _ := json.Marshal(request)
	if err = os.WriteFile(requestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	submitOutput := runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath)
	var submitted struct {
		Status      string `json:"status"`
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(submitOutput, &submitted); err != nil || submitted.Status != "queued" || submitted.ControlFile == "" {
		t.Fatalf("submit=%s err=%v", submitOutput, err)
	}

	for _, taskID := range []string{"task-a", "task-b", "task-c"} {
		for {
			statusOutput := runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", taskID, "--control-file", submitted.ControlFile)
			var status struct {
				Status string `json:"status"`
			}
			if err = json.Unmarshal(statusOutput, &status); err != nil {
				t.Fatalf("status=%s err=%v", statusOutput, err)
			}
			if status.Status == "completed" {
				break
			}
			select {
			case <-ctx.Done():
				for _, name := range []string{"coordinator.log", "source-host.log"} {
					if log, readErr := os.ReadFile(filepath.Join(state, name)); readErr == nil {
						t.Logf("%s:\n%s", name, log)
					}
				}
				t.Fatalf("task %s did not complete: %s", taskID, statusOutput)
			case <-time.After(25 * time.Millisecond):
			}
		}
		collected := runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", taskID, "--control-file", submitted.ControlFile)
		var result struct {
			Events []struct {
				EventID       string `json:"event_id"`
				EventRevision int64  `json:"event_revision"`
				PayloadHash   string `json:"payload_hash"`
				ActionSlot    string `json:"action_slot"`
				Kind          string `json:"kind"`
			} `json:"events"`
		}
		if err = json.Unmarshal(collected, &result); err != nil || len(result.Events) < 1 {
			t.Fatalf("collect %s: %s err=%v", taskID, collected, err)
		}
		decisions := make([]map[string]any, 0, len(result.Events))
		for index, event := range result.Events {
			decisions = append(decisions, map[string]any{"event_id": event.EventID, "event_revision": event.EventRevision, "event_hash": event.PayloadHash, "action_slot": event.ActionSlot, "decision": "handled", "command_id": fmt.Sprintf("ack-%s-%d", taskID, index)})
		}
		ackPath := filepath.Join(root, "ack-"+taskID+".json")
		ackBody, _ := json.Marshal(map[string]any{"version": 1, "task_id": taskID, "control_file": submitted.ControlFile, "delivery_id": "delivery-" + taskID, "history_proof_sha256": strings.Repeat("a", 64), "decisions": decisions})
		if err = os.WriteFile(ackPath, ackBody, 0600); err != nil {
			t.Fatal(err)
		}
		runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", ackPath)
	}

	beforeRestart := taskStatus(t, ctx, bin, state, "task-d", submitted.ControlFile)
	if beforeRestart.Status != "running" {
		t.Fatalf("task-d before restart=%#v", beforeRestart)
	}
	stopCoordinator(t, state)
	afterRestart := taskStatus(t, ctx, bin, state, "task-d", submitted.ControlFile)
	if afterRestart.Status != "interrupted" {
		t.Fatalf("task-d after restart=%#v", afterRestart)
	}
	time.Sleep(150 * time.Millisecond)
	stillSilent := taskStatus(t, ctx, bin, state, "task-d", submitted.ControlFile)
	if stillSilent.Status != "interrupted" || stillSilent.SegmentID != afterRestart.SegmentID {
		t.Fatalf("restart dispatched without explicit resume: before=%#v after=%#v", afterRestart, stillSilent)
	}
	runBinary(t, ctx, bin, "resume", "--state-dir", state, "--task-id", "task-d", "--work-revision", "1", "--control-file", submitted.ControlFile)
	for {
		resumed := taskStatus(t, ctx, bin, state, "task-d", submitted.ControlFile)
		if resumed.Status == "completed" {
			if resumed.SegmentID == afterRestart.SegmentID {
				t.Fatalf("resume reused segment: %#v", resumed)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("explicit resume did not complete: %#v", resumed)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

type cliTaskStatus struct {
	Status        string `json:"status"`
	SegmentID     string `json:"segment_id"`
	SegmentStatus string `json:"segment_status"`
}

func taskStatus(t *testing.T, ctx context.Context, bin, state, taskID, controlFile string) cliTaskStatus {
	t.Helper()
	output := runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", taskID, "--control-file", controlFile)
	var status cliTaskStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatalf("status=%s err=%v", output, err)
	}
	return status
}

func stopCoordinator(t *testing.T, state string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, "coordinator.pid"))
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	_, _ = fmt.Sscanf(string(data), "%d", &pid)
	if pid <= 0 {
		t.Fatalf("invalid coordinator pid %q", data)
	}
	if err = syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(12 * time.Second); ; {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("coordinator %d did not exit", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInvocationRejectsChangedPinBeforeExecutingVersionProbe(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "canary")
	binary := filepath.Join(root, "provider")
	script := []byte("#!/bin/sh\ntouch \"" + canary + "\"\necho v1\n")
	if err := os.WriteFile(binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"provider": "claude-code", "binary_path": binary, "binary_version": "v1",
		"binary_sha256": strings.Repeat("0", 64), "directory": root, "prompt": "test",
		"permission_mode": "default",
	})
	if _, err := invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: payload}); err == nil {
		t.Fatal("expected pin rejection")
	}
	if _, err := os.Stat(canary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted binary executed, canary err=%v", err)
	}
}

func TestCLIForceRestartReplaysSpoolBeforeExplicitResume(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-force-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	t.Cleanup(func() {
		if _, statErr := os.Stat(filepath.Join(state, "coordinator.pid")); statErr == nil {
			stopCoordinator(t, state)
		}
	})
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = os.Environ()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build: %v\n%s", buildErr, output)
	}
	originBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "marker")
	request := map[string]any{
		"run_id": "force-run", "controller_thread": "force-thread", "plan_revision": 1,
		"origin_context_id": "force-origin", "origin_pid": os.Getpid(), "origin_birth": originBirth,
		"host_generation": "force-generation",
		"tasks":           []any{map[string]any{"id": "force-task", "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "if [ -e " + marker + " ]; then sleep 0.2; exit 0; else : > " + marker + "; sleep 30; fi"}, "directory": root}}},
	}
	requestPath := filepath.Join(root, "request.json")
	data, _ := json.Marshal(request)
	if err = os.WriteFile(requestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil || submitted.ControlFile == "" {
		t.Fatalf("submit err=%v", err)
	}
	var before cliTaskStatus
	for runningDeadline := time.Now().Add(5 * time.Second); ; {
		before = taskStatus(t, ctx, bin, state, "force-task", submitted.ControlFile)
		if before.SegmentStatus == "running" {
			break
		}
		if time.Now().After(runningDeadline) {
			for _, name := range []string{"coordinator.log", "source-host.log"} {
				if log, readErr := os.ReadFile(filepath.Join(state, name)); readErr == nil {
					t.Logf("%s:\n%s", name, log)
				}
			}
			t.Fatalf("force task never reached running: %#v", before)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("force task context expired: %#v", before)
		case <-time.After(20 * time.Millisecond):
		}
	}
	data, err = os.ReadFile(filepath.Join(state, "coordinator.pid"))
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	_, _ = fmt.Sscanf(string(data), "%d", &pid)
	if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("killed coordinator %d remained", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	after := taskStatus(t, ctx, bin, state, "force-task", submitted.ControlFile)
	if after.SegmentID != before.SegmentID || after.Status == "completed" {
		t.Fatalf("force restart dispatched work: before=%#v after=%#v", before, after)
	}
	runBinary(t, ctx, bin, "resume", "--state-dir", state, "--task-id", "force-task", "--work-revision", "1", "--control-file", submitted.ControlFile)
	for {
		resumed := taskStatus(t, ctx, bin, state, "force-task", submitted.ControlFile)
		if resumed.Status == "completed" {
			if resumed.SegmentID == before.SegmentID {
				t.Fatalf("force resume reused segment: %#v", resumed)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("force resume did not complete: %#v", resumed)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func runBinary(t *testing.T, ctx context.Context, bin string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, bin, args...)
	command.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		for index, arg := range args {
			if arg == "--state-dir" && index+1 < len(args) {
				for _, name := range []string{"coordinator.log", "source-host.log"} {
					if log, readErr := os.ReadFile(filepath.Join(args[index+1], name)); readErr == nil {
						t.Logf("%s:\n%s", name, log)
					}
				}
			}
		}
		t.Fatalf("%s %v: %v\n%s", bin, args, err, output)
	}
	return output
}
