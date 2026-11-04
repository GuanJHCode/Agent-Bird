package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

func TestCLIOwnerRequestChangesSingleTaskAndReplay(t *testing.T) {
	exerciseCLIOwnerRequestChanges(t, false)
}
func TestCLIAutoWorkspaceOwnerReworkRemainsOutsideState(t *testing.T) {
	exerciseCLIOwnerRequestChanges(t, true)
}

func exerciseCLIOwnerRequestChanges(t *testing.T, automatic bool) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "orchestrator")
	bin = deliveryTestBinary(t, bin)
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	for _, name := range []string{"README", "obsolete.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	fixture, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	t.Setenv("CANDIDATE_FIXTURE_MODE", "rework")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	path := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"controller_thread": "owner", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", path), &owner); err != nil {
		t.Fatal(err)
	}
	delete(owner, "version")
	owner["run_id"] = "owner-development"
	owner["plan_revision"] = 7
	owner["delivery_mode"] = "collect"
	owner["tasks"] = []any{map[string]any{"id": "implement", "max_attempts": 3, "adapter": map[string]any{"provider": "claude-code", "provider_lock": map[string]any{"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": map[string]any{"path": fixture, "version": "2.1.267 (Claude Code)", "sha256": hex.EncodeToString(digest[:])}}, "profile": map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 15000}, "directory": filepath.Join(root, "work"), "prompt": "implement", "candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{"README", "added.txt", "obsolete.txt"}}}}}
	if automatic {
		raw, err := json.Marshal(owner["tasks"])
		if err != nil {
			t.Fatal(err)
		}
		var tasks []coordinator.TaskRequest
		if err = json.Unmarshal(raw, &tasks); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err = json.Unmarshal(tasks[0].AdapterPayload, &payload); err != nil {
			t.Fatal(err)
		}
		delete(payload, "directory")
		tasks[0].AdapterPayload, _ = json.Marshal(payload)
		if err = prepareTaskWorkspaces(state, filepath.Join(state, "dispatch", "owner", "request", "handle.json"), tasks); err != nil {
			t.Fatal(err)
		}
		owner["tasks"] = tasks
	}
	write(owner)
	var receipt struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", path), &receipt); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "owner-development", StateDir: state, ControlFile: receipt.ControlFile, TaskIDs: []string{"implement"}, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	wait := func(revision int) store.RunSummary {
		t.Helper()
		for {
			var summary store.RunSummary
			if err := json.Unmarshal(runBinary(t, ctx, bin, "task", "inspect", "--handle", handle), &summary); err != nil {
				t.Fatal(err)
			}
			task := summary.Tasks[0]
			if task.WorkRevision == revision && task.Status == "result_ready" && task.ActiveSegments == 0 {
				return summary
			}
			if ctx.Err() != nil || task.Status == "failed" || task.Status == "unknown" {
				t.Fatalf("task failed %+v", task)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	summary := wait(1)
	if !slices.Contains(summary.Tasks[0].AllowedActions, "request-changes") {
		t.Fatalf("missing owner action %+v", summary)
	}
	collect := func() contract.Event {
		t.Helper()
		var page struct {
			Events []contract.Event `json:"events"`
		}
		if err := json.Unmarshal(runBinary(t, ctx, bin, "task", "collect", "--handle", handle), &page); err != nil || len(page.Events) != 1 {
			t.Fatalf("collect %+v %v", page, err)
		}
		return page.Events[0]
	}
	first := collect()
	firstBytes, err := os.ReadFile(first.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	var firstArtifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(firstBytes, &firstArtifact); err != nil {
		t.Fatal(err)
	}
	if automatic {
		assertOutsideState(t, state, firstArtifact.Candidate.Worktree)
		if !strings.Contains(firstArtifact.Candidate.Worktree, ".attempt-") {
			t.Fatal("automatic attempt identity lost")
		}
	}
	// Delivery acknowledgement remains independent of the business revision.
	var page struct {
		DeliveryID string `json:"delivery_id"`
		Proof      string `json:"collection_proof_sha256"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "collect", "--handle", handle), &page); err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"version": 1, "task_id": "implement", "delivery_id": page.DeliveryID, "collection_proof_sha256": page.Proof, "decisions": []store.AckDecision{{EventID: first.EventID, EventRevision: first.EventRevision, EventHash: first.PayloadHash, ActionSlot: first.ActionSlot, Decision: "handled", CommandID: "ack-first"}}})
	runBinary(t, ctx, bin, "task", "ack", "--handle", handle, "--request", path)
	brief := ownerChangeBrief{WorkRevision: 1, CandidateOID: firstArtifact.Candidate.CandidateOID, Feedback: "Revise README", Acceptance: "ACCEPTANCE_MARKER: revised", CommandID: "changes-1"}
	write(brief)
	firstReceipt := runBinary(t, ctx, bin, "task", "request-changes", "--handle", handle, "--request", path)
	replay := runBinary(t, ctx, bin, "task", "request-changes", "--handle", handle, "--request", path)
	if string(firstReceipt) != string(replay) {
		t.Fatalf("replay changed receipt: %s %s", firstReceipt, replay)
	}
	reject := func(want string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, bin, "task", "request-changes", "--handle", handle, "--request", path).CombinedOutput()
		if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("expected %s: %s %v", want, out, err)
		}
	}
	changed := brief
	changed.Feedback = "different"
	write(changed)
	reject("request_changes_command_conflict")
	changed = brief
	changed.CommandID = "stale-command"
	write(changed)
	reject("request_changes_stale")
	write(map[string]any{"task_id": "implement", "work_revision": 1, "event_id": first.EventID, "event_revision": first.EventRevision, "event_hash": first.PayloadHash, "action_slot": first.ActionSlot, "decision": "accept", "command_id": "stale-accept"})
	if out, err := exec.CommandContext(ctx, bin, "task", "accept", "--handle", handle, "--request", path).CombinedOutput(); err == nil {
		t.Fatalf("old accept succeeded: %s", out)
	}
	summary = wait(2)
	if summary.Tasks[0].ReviewReworkCount != 1 || summary.Tasks[0].TechnicalRetryCount != 0 {
		t.Fatalf("wrong counters %+v", summary)
	}
	second := collect()
	var secondArtifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	data, err = os.ReadFile(second.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &secondArtifact); err != nil {
		t.Fatal(err)
	}
	if automatic {
		assertOutsideState(t, state, secondArtifact.Candidate.Worktree)
	}
	if secondArtifact.Candidate.BaseOID != firstArtifact.Candidate.CandidateOID || secondArtifact.Candidate.CandidateOID == firstArtifact.Candidate.CandidateOID || secondArtifact.Candidate.Worktree == firstArtifact.Candidate.Worktree || secondArtifact.Candidate.Binding.WorkRevision != 2 || secondArtifact.Candidate.Binding.RevisionSHA256 == "" {
		t.Fatalf("lost lineage %+v", secondArtifact)
	}
	if git("show", firstArtifact.Candidate.CandidateOID+":README") != "worker implementation" || git("show", secondArtifact.Candidate.CandidateOID+":README") != "worker implementation revised" || git("rev-parse", "HEAD") != base {
		t.Fatal("old candidate or original repository changed")
	}
	after, err := os.ReadFile(first.Artifact.Path)
	if err != nil || string(after) != string(firstBytes) {
		t.Fatal("old artifact changed")
	}
	write(map[string]any{"task_id": "implement", "work_revision": 2, "event_id": second.EventID, "event_revision": second.EventRevision, "event_hash": second.PayloadHash, "action_slot": second.ActionSlot, "decision": "accept", "command_id": "accept-second"})
	runBinary(t, ctx, bin, "task", "accept", "--handle", handle, "--request", path)
	if taskStatus(t, ctx, bin, state, "implement", receipt.ControlFile).Status != "completed" {
		t.Fatal("new revision did not complete")
	}
}
