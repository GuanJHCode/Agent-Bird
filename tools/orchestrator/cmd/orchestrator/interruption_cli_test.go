package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCLIInterruptionFeedbackUsesIndependentProgress(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "local-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
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
	t.Setenv("CANDIDATE_FIXTURE_MODE", "progress")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	provider, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(provider)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(contents)
	owner["tasks"] = []any{map[string]any{"id": "task", "max_attempts": 1, "max_active_ms": 20000, "adapter": map[string]any{"provider": "claude-code", "directory": root, "prompt": "fixture", "profile": map[string]any{"version": 1, "role": "reviewer", "permission": "read-only", "timeout_ms": 20000}, "provider_lock": map[string]any{"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": map[string]any{"path": provider, "version": "fixture-v1", "sha256": hex.EncodeToString(hash[:])}}}}}

	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", path), &submitted); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "local", StateDir: state, ControlFile: submitted.ControlFile, TaskIDs: []string{"task"}, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	liveProgress := false
	lastInspect := time.Time{}
	for {
		s := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile)
		if s.Status == "failed" {
			t.Fatalf("provider fixture failed before interruption: %+v", s)
		}
		if s.Status == "interrupted" {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("status=%s", s.Status)
		}
		if time.Since(lastInspect) > time.Second {
			var status struct {
				Tasks []struct {
					LatestProgress *contract.ArtifactRef `json:"latest_progress"`
				} `json:"tasks"`
			}
			if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "inspect", "--handle", handle), &status); err != nil {
				t.Fatal(err)
			}
			if len(status.Tasks) == 1 && status.Tasks[0].LatestProgress != nil {
				liveProgress = true
			}
			lastInspect = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
	var collection struct {
		DeliveryID            string           `json:"delivery_id"`
		CollectionProofSHA256 string           `json:"collection_proof_sha256"`
		Events                []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "collect", "--handle", handle, "--include-diagnostics"), &collection); err != nil {
		t.Fatal(err)
	}
	if !liveProgress {
		t.Fatal("no progress visible before interruption")
	}
	if collection.CollectionProofSHA256 == "" {
		t.Fatal("missing receipt")
	}
	var event contract.Event
	found := false
	for _, e := range collection.Events {
		if e.Kind == contract.EventStopped {
			event = e
			found = true
		}
		if e.Kind == contract.EventResult {
			t.Fatal("partial was accepted")
		}
		if e.Kind == contract.EventProgress {
			if e.Artifact == nil {
				t.Fatal("missing checkpoint")
			}
			content, err := os.ReadFile(e.Artifact.Path)
			if err != nil || !strings.Contains(string(content), "Partial CLI finding") {
				t.Fatalf("checkpoint: %s %v", content, err)
			}
		}
	}
	if !found || event.StopReason == "" || event.Artifact != nil {
		t.Fatalf("missing interruption: %+v", event)
	}

	write(map[string]any{"version": 1, "task_id": "task",
		"delivery_id": collection.DeliveryID, "collection_proof_sha256": collection.CollectionProofSHA256,
		"decisions": []map[string]any{{"event_id": event.EventID, "event_revision": event.EventRevision,
			"event_hash": event.PayloadHash, "action_slot": event.ActionSlot, "decision": "handled", "command_id": "ack-result"}}})
	for i := 0; i < 2; i++ {
		runBinary(t, ctx, bin, "task", "ack", "--handle", handle, "--request", path)
	}
	var summary struct {
		Tasks []struct {
			Status              string `json:"status"`
			ResumeBlockedReason string `json:"resume_blocked_reason"`
			StopReason          string `json:"stop_reason"`
		} `json:"tasks"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "inspect", "--handle", handle), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Tasks) != 1 || summary.Tasks[0].Status != "interrupted" || summary.Tasks[0].ResumeBlockedReason != "profile_resume_not_verified" || summary.Tasks[0].StopReason != event.StopReason {
		t.Fatalf("%+v", summary)
	}
	output, err := exec.CommandContext(ctx, bin, "task", "resume", "--handle", handle, "--work-revision", "1").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "profile_resume_not_verified") {
		t.Fatalf("resume: %s %v", output, err)
	}
}
