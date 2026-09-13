package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
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
			map[string]any{"id": "task-e", "max_attempts": 2, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "sleep 0.1; exit 7"}, "directory": root}, "fallbacks": []any{map[string]any{"kind": "fake", "args": []string{"/bin/sleep", "0.1"}, "directory": root}}},
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
				SegmentID     string `json:"segment_id"`
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
	for {
		status := taskStatus(t, ctx, bin, state, "task-e", submitted.ControlFile)
		if status.Status == "failed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("task-e primary did not fail: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	failedOutput := runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "task-e", "--control-file", submitted.ControlFile)
	var failed struct {
		Events []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(failedOutput, &failed); err != nil || len(failed.Events) != 1 || failed.Events[0].Kind != contract.EventFailed {
		t.Fatalf("failed collect=%s err=%v", failedOutput, err)
	}
	failure := failed.Events[0]
	runBinary(t, ctx, bin, "retry", "--state-dir", state, "--task-id", "task-e", "--work-revision", "1", "--control-file", submitted.ControlFile,
		"--event-id", failure.EventID, "--event-revision", fmt.Sprintf("%d", failure.EventRevision), "--event-hash", failure.PayloadHash,
		"--action-slot", failure.ActionSlot, "--segment-id", failure.SegmentID, "--next-attempt", "2", "--command-id", "retry-task-e-1", "--use-next-fallback")
	for {
		status := taskStatus(t, ctx, bin, state, "task-e", submitted.ControlFile)
		if status.Status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("task-e fallback did not complete: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
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

func TestCodexTrialInvocationIsBlockedBeforePinReadOrVersionProbe(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "must-not-exist")
	cases := []map[string]any{
		{"provider": string(adapter.ProviderCodex), "binary_version": "alias-version", "binary_sha256": strings.Repeat("a", 64)},
		{"provider": string(adapter.ProviderClaude), "binary_version": adapter.CodexVersion, "binary_sha256": strings.Repeat("a", 64)},
		{"provider": string(adapter.ProviderClaude), "binary_version": "alias-version", "binary_sha256": strings.ToUpper(adapter.CodexSHA256)},
	}
	for i, fields := range cases {
		fields["binary_path"] = canary
		fields["directory"] = root
		fields["prompt"] = "inspect"
		fields["permission_mode"] = "plan"
		payload, _ := json.Marshal(fields)
		_, err := invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: payload})
		if err == nil || err.Error() != "codex_trial_guard_not_ready" {
			t.Fatalf("case %d: err=%v", i, err)
		}
	}
	if _, statErr := os.Stat(canary); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("blocked Codex path was touched: %v", statErr)
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

func TestCLIProviderQuestionAnswerAndOwnerAccept(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real multi-process entrypoint")
	}
	root, err := os.MkdirTemp("/tmp", "g1-question-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	answerCapture := filepath.Join(root, "answer-input")
	provider := filepath.Join(root, "agy-provider")
	script := "#!/bin/sh\n" +
		"if [ \"${1-}\" = --version ]; then echo 1.0.0; exit 0; fi\n" +
		"case \" $* \" in\n" +
		"  *\" --conversation session-1 \"*) IFS= read -r answer; printf '%s\\n' \"$answer\" > \"" + answerCapture + "\"; printf '%s\\n' '{\"event\":\"init\",\"conversation_id\":\"session-1\"}' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"session-1\",\"status\":\"SUCCESS\",\"response\":\"done\"}}' ;;\n" +
		"  *) printf '%s\\n' '{\"event\":\"init\",\"conversation_id\":\"session-1\"}' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"session-1\",\"status\":\"WAITING\",\"response\":\"Choose the narrow API\"}}' ;;\n" +
		"esac\n"
	if err = os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	provider, err = filepath.EvalSymlinks(provider)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(script))
	originBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "request.json")
	request := map[string]any{
		"run_id": "question-run", "controller_thread": "question-thread", "plan_revision": 1,
		"origin_context_id": "question-origin", "origin_pid": os.Getpid(), "origin_birth": originBirth,
		"host_generation": "question-generation",
		"tasks":           []any{map[string]any{"id": "question-task", "max_attempts": 3, "completion_policy": "owner_review", "adapter": map[string]any{"provider": "antigravity-cli", "binary_path": provider, "binary_version": "1.0.0", "binary_sha256": fmt.Sprintf("%x", digest), "directory": root, "prompt": "Start", "permission_mode": "default"}}},
	}
	body, _ := json.Marshal(request)
	if err = os.WriteFile(requestPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	for {
		statusBody := runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", "question-task", "--control-file", submitted.ControlFile)
		var status struct {
			Status           string `json:"status"`
			SegmentID        string `json:"segment_id"`
			QuestionID       string `json:"question_id"`
			QuestionRevision int    `json:"question_revision"`
		}
		if err = json.Unmarshal(statusBody, &status); err != nil {
			t.Fatal(err)
		}
		if status.Status == "waiting_question" {
			answerPath := filepath.Join(root, "answer.json")
			answerBody, _ := json.Marshal(map[string]any{"task_id": "question-task", "control_file": submitted.ControlFile, "work_revision": 1, "question_id": status.QuestionID, "question_revision": status.QuestionRevision, "answer": "Use the narrow API."})
			if err = os.WriteFile(answerPath, answerBody, 0600); err != nil {
				t.Fatal(err)
			}
			runBinary(t, ctx, bin, "answer", "--state-dir", state, "--request", answerPath)
			break
		}
		if status.Status == "failed" || status.Status == "unknown" {
			collected := runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "question-task", "--control-file", submitted.ControlFile)
			t.Fatalf("provider question failed: status=%#v collected=%s", status, collected)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("question was not persisted: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	for {
		status := taskStatus(t, ctx, bin, state, "question-task", submitted.ControlFile)
		if status.Status == "result_ready" {
			var collection struct {
				Events []contract.Event `json:"events"`
			}
			if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "question-task", "--control-file", submitted.ControlFile), &collection); err != nil {
				t.Fatal(err)
			}
			var result contract.Event
			for _, event := range collection.Events {
				if event.Kind == contract.EventResult {
					result = event
				}
			}
			if result.EventID == "" {
				t.Fatalf("result event missing: %#v", collection.Events)
			}
			runBinary(t, ctx, bin, "accept", "--state-dir", state, "--task-id", "question-task", "--work-revision", "1", "--control-file", submitted.ControlFile, "--event-id", result.EventID, "--event-revision", fmt.Sprint(result.EventRevision), "--event-hash", result.PayloadHash, "--action-slot", result.ActionSlot, "--decision", "accept", "--command-id", "review-question-result")
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("answered provider did not return result: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if captured, readErr := os.ReadFile(answerCapture); readErr != nil || !strings.Contains(string(captured), "Use the narrow API.") {
		t.Fatalf("answer was not delivered through resume stdin: %q err=%v", captured, readErr)
	}
	if status := taskStatus(t, ctx, bin, state, "question-task", submitted.ControlFile); status.Status != "completed" {
		t.Fatalf("accepted status=%#v", status)
	}
}

func TestCLIWorkerReportUsesSegmentCapability(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-report-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	artifactBody := []byte("reported\n")
	artifactHash := sha256.Sum256(artifactBody)
	worker := filepath.Join(root, "worker.sh")
	script := "#!/bin/sh\nset -eu\ndir=$(dirname \"$ORCHESTRATOR_REPORT_CAPABILITY\")\nartifact=$dir/result.txt\nrequest=$dir/report.json\nprintf 'reported\\n' > \"$artifact\"\nchmod 600 \"$artifact\"\nprintf '%s\\n' '{\"version\":1,\"event_id\":\"worker-result-1\",\"sequence\":1,\"kind\":\"result\",\"payload\":{\"status\":\"completed\",\"artifact\":{\"id\":\"result.txt\",\"path\":\"'\"$artifact\"'\",\"size\":9,\"sha256\":\"" + fmt.Sprintf("%x", artifactHash) + "\"}}}' > \"$request\"\nchmod 600 \"$request\"\n\"$ORCHESTRATOR_REPORT_EXECUTABLE\" report --capability-file \"$ORCHESTRATOR_REPORT_CAPABILITY\" --request \"$request\"\n"
	if err = os.WriteFile(worker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	worker, err = filepath.EvalSymlinks(worker)
	if err != nil {
		t.Fatal(err)
	}
	originBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "submit.json")
	body, _ := json.Marshal(map[string]any{"run_id": "report-run", "controller_thread": "report-thread", "plan_revision": 1, "origin_context_id": "report-origin", "origin_pid": os.Getpid(), "origin_birth": originBirth, "host_generation": "report-generation", "tasks": []any{map[string]any{"id": "report-task", "max_attempts": 1, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{worker}, "directory": root}}}})
	if err = os.WriteFile(requestPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	for {
		if status := taskStatus(t, ctx, bin, state, "report-task", submitted.ControlFile); status.Status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reported task did not complete")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var collection struct {
		Events []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "report-task", "--control-file", submitted.ControlFile), &collection); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range collection.Events {
		if event.EventID == "worker-result-1" && strings.HasPrefix(event.ProducerID, "report-capability-") && event.Artifact != nil && event.Artifact.SHA256 == fmt.Sprintf("%x", artifactHash) {
			found = true
		}
	}
	if !found {
		t.Fatalf("durable worker report missing: %#v", collection.Events)
	}
}

func TestCLIOwnerExitRequiresVerifiedRebindBeforeResume(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-rebind-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	owner := exec.Command("/bin/sleep", "1")
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	oldPID := owner.Process.Pid
	oldBirth, err := process.Birth(oldPID)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "marker")
	requestPath := filepath.Join(root, "submit.json")
	body, _ := json.Marshal(map[string]any{"run_id": "rebind-run", "controller_thread": "rebind-thread", "plan_revision": 1, "origin_context_id": "rebind-origin", "origin_pid": oldPID, "origin_birth": oldBirth, "host_generation": "generation-00000001", "tasks": []any{map[string]any{"id": "rebind-task", "max_attempts": 3, "completion_policy": "exit_success_fixture", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "if [ -e " + marker + " ]; then exit 0; else : > " + marker + "; sleep 30; fi"}, "directory": root}}}})
	if err = os.WriteFile(requestPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	for {
		status := taskStatus(t, ctx, bin, state, "rebind-task", submitted.ControlFile)
		if status.Status == "interrupted" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("owner exit did not interrupt task: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	newBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	service := exec.Command("/bin/sleep", "30")
	backend := exec.Command("/bin/sleep", "30")
	if err = service.Start(); err != nil {
		t.Fatal(err)
	}
	if err = backend.Start(); err != nil {
		_ = service.Process.Kill()
		_ = service.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Process.Kill()
		_ = service.Wait()
		_ = backend.Process.Kill()
		_ = backend.Wait()
	})
	serviceBirth, err := process.Birth(service.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	backendBirth, err := process.Birth(backend.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ownerExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ownerExecutable, err = filepath.EvalSymlinks(ownerExecutable)
	if err != nil {
		t.Fatal(err)
	}
	sleepExecutable, err := filepath.EvalSymlinks("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "owner.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err = os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	socketInfo, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	socketStat := socketInfo.Sys().(*syscall.Stat_t)
	leaseID := "lease-2"
	activationID := strings.Repeat("a", 32)
	attachment := map[string]any{
		"version": 1, "profile_id": "profile-a", "controller_thread_id": "rebind-thread",
		"controller_epoch": 2, "owner_context_sha256": strings.Repeat("c", 64),
		"lease_id": leaseID, "owner_connection_id": "connection-2", "activation_id": activationID,
		"manifest_sha256": strings.Repeat("d", 64), "helper_grant_sha256": strings.Repeat("e", 64),
		"service_identity": map[string]any{"pid": service.Process.Pid, "uid": os.Getuid(), "birth": serviceBirth,
			"executable": sleepExecutable, "executable_sha256": testFileSHA(t, sleepExecutable)},
		"backend_identity": map[string]any{"pid": backend.Process.Pid, "uid": os.Getuid(), "birth": backendBirth,
			"executable": sleepExecutable, "executable_sha256": testFileSHA(t, sleepExecutable)},
		"private_socket":          socketPath,
		"private_socket_identity": []uint64{uint64(socketStat.Dev), uint64(socketStat.Ino), uint64(socketStat.Uid), uint64(socketStat.Mode)},
		"origin_process": map[string]any{"pid": os.Getpid(), "uid": os.Getuid(), "birth": newBirth,
			"executable": ownerExecutable, "executable_sha256": testFileSHA(t, ownerExecutable)},
	}
	attachmentJSON, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	attachmentProof := sha256.Sum256(attachmentJSON)
	proof := hex.EncodeToString(attachmentProof[:])
	ownerCapability := filepath.Join(root, "owner.json")
	ownerBody, _ := json.Marshal(map[string]any{"version": 1, "path": ownerCapability, "controller_thread_id": "rebind-thread", "origin_context_id": "rebind-origin", "origin_pid": os.Getpid(), "origin_birth": newBirth, "host_generation": "generation-00000002", "generation_number": 2, "attachment_proof_sha256": proof, "lease_id": leaseID, "activation_id": activationID, "owner_attachment": attachment})
	if err = os.WriteFile(ownerCapability, ownerBody, 0600); err != nil {
		t.Fatal(err)
	}
	rebindPath := filepath.Join(root, "rebind.json")
	rebindBody, _ := json.Marshal(map[string]any{"version": 1, "owner_capability": ownerCapability, "control_file": submitted.ControlFile, "controller_thread": "rebind-thread", "origin_context_id": "rebind-origin", "origin_pid": os.Getpid(), "origin_birth": newBirth, "host_generation": "generation-00000002", "attachment_proof_sha256": proof})
	if err = os.WriteFile(rebindPath, rebindBody, 0600); err != nil {
		t.Fatal(err)
	}
	runBinary(t, ctx, bin, "rebind-owner", "--state-dir", state, "--request", rebindPath)
	runBinary(t, ctx, bin, "resume", "--state-dir", state, "--task-id", "rebind-task", "--work-revision", "1", "--control-file", submitted.ControlFile)
	for {
		status := taskStatus(t, ctx, bin, state, "rebind-task", submitted.ControlFile)
		if status.Status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("rebound task did not resume: %#v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func testFileSHA(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestSourceEnvironmentPreservesOwnerEnvironmentWithoutToolCapabilities(t *testing.T) {
	t.Setenv("OWNER_PROVIDER_AUTH", "owner-auth")
	t.Setenv("ORCHESTRATOR_CONTROL_TOKEN", "control-secret")
	t.Setenv("ORCHESTRATOR_REPORT_CAPABILITY", "report-secret")
	t.Setenv("ORCHESTRATOR_BOOTSTRAP", "bootstrap-secret")
	t.Setenv("ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	values := make(map[string]string)
	for _, item := range sourceEnvironment() {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	if values["OWNER_PROVIDER_AUTH"] != "owner-auth" || values["ORCHESTRATOR_ENABLE_TEST_FAKE"] != "1" {
		t.Fatalf("owner environment missing: %#v", values)
	}
	for _, key := range []string{"ORCHESTRATOR_CONTROL_TOKEN", "ORCHESTRATOR_REPORT_CAPABILITY", "ORCHESTRATOR_BOOTSTRAP"} {
		if _, ok := values[key]; ok {
			t.Fatalf("tool capability leaked: %s", key)
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
