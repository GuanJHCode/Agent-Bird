package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// The only provider is the local Go fixture executable, never a model/account.
func TestManagedTrialExactCollectionAcrossOwnerRevisions(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("ack-first-%t", ackFirst), func(t *testing.T) { exerciseManagedTrialCollection(t, ackFirst) })
	}
}
func exerciseManagedTrialCollection(t *testing.T, ackFirst bool) {
	root, err := os.MkdirTemp("/private/tmp", "trial-page-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
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
	var savedACK []byte
	calls := 0
	collect := func() contract.Event {
		t.Helper()
		var page struct {
			DeliveryID string           `json:"delivery_id"`
			Proof      string           `json:"collection_proof_sha256"`
			Events     []contract.Event `json:"events"`
		}
		raw := runBinary(t, ctx, bin, "task", "collect", "--handle", handle)
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		calls++
		want := 1
		if calls == 2 && !ackFirst {
			want = 2
		}
		if len(page.Events) != want {
			t.Fatalf("pending events=%d want=%d: %s", len(page.Events), want, raw)
		}
		event := page.Events[len(page.Events)-1]
		input := filepath.Join(root, "collection.json")
		if err := os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
		driver, err := filepath.Abs("../../../../tasks/cross-cli-subagent/scripts/managed-coding-trial.py")
		if err != nil {
			t.Fatal(err)
		}
		code := `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location("trial",sys.argv[1]); trial=importlib.util.module_from_spec(spec); spec.loader.exec_module(trial)
page=json.load(open(sys.argv[2])); e=page["events"][-1]
binding={k:e[k] for k in ("event_id","event_revision","action_slot")}; binding["event_hash"]=e["payload_hash"]
print(json.dumps(trial.collection_ack(page,"implement",binding,"ack-"+e["event_id"])))`
		savedACK, err = exec.CommandContext(ctx, "python3", "-c", code, driver, input).CombinedOutput()
		if want == 2 {
			if err == nil || !strings.Contains(string(savedACK), "exact_reviewed_collection_required") {
				t.Fatalf("driver ignored rev1: %s %v", savedACK, err)
			}
			write(map[string]any{"version": 1, "task_id": "implement", "delivery_id": page.DeliveryID, "collection_proof_sha256": page.Proof, "decisions": []store.AckDecision{{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "handled", CommandID: "partial"}}})
			if out, err := exec.CommandContext(ctx, bin, "task", "ack", "--handle", handle, "--request", path).CombinedOutput(); err == nil {
				t.Fatalf("store accepted partial receipt: %s", out)
			}
		} else if err != nil {
			t.Fatalf("ACK builder: %s %v", savedACK, err)
		}
		return event
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
	if ackFirst {
		if err := os.WriteFile(path, savedACK, 0600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			runBinary(t, ctx, bin, "task", "ack", "--handle", handle, "--request", path)
		}
		if status := taskStatus(t, ctx, bin, state, "implement", receipt.ControlFile).Status; status != "result_ready" {
			t.Fatalf("ACK accepted business result: %s", status)
		}
	}
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
	if ackFirst {
		// Lost ACK/feedback replies may be replayed after revision two already exists.
		if err := os.WriteFile(path, savedACK, 0600); err != nil {
			t.Fatal(err)
		}
		runBinary(t, ctx, bin, "task", "ack", "--handle", handle, "--request", path)
	}
	collect()
	if ackFirst {
		if err := os.WriteFile(path, savedACK, 0600); err != nil {
			t.Fatal(err)
		}
		runBinary(t, ctx, bin, "task", "ack", "--handle", handle, "--request", path)
		var remaining struct {
			Events []contract.Event `json:"events"`
		}
		if err := json.Unmarshal(runBinary(t, ctx, bin, "task", "collect", "--handle", handle), &remaining); err != nil || len(remaining.Events) != 0 {
			t.Fatalf("remaining events: %+v %v", remaining, err)
		}
	}

	if git("rev-parse", "HEAD") != base {
		t.Fatal("original HEAD changed")
	}
}
