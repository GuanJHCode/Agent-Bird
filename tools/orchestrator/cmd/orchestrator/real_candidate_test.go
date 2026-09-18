package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// Opt-in real model. Retains the private run root, including on unknown; test
// cleanup must never destroy an unconfirmed process's workspace or credentials.
func TestRealClaudeManagedCandidate(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_REAL_CLAUDE_IMPLEMENTER") != "1" {
		t.Skip("requires explicit authenticated implementer trial")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	// The chain must have enough wall time for every bounded stage plus Git
	// setup/cleanup. A single 120s deadline can truncate a healthy reviewer after
	// a slower third-party-backed implementer has used most of that deadline.
	ctx, cancel := context.WithTimeout(context.Background(), (110+3*180+30)*time.Second)
	defer cancel()
	lock, err := adapter.Probe(ctx, adapter.ProviderClaude, binary)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "p0-real-candidate-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private evidence root: %s", root)
	repo, work := filepath.Join(root, "repo"), filepath.Join(root, "worker")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.name", "Orchestrator Trial")
	git(repo, "config", "user.email", "trial@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "add.py"), []byte("def add(a, b):\n    return a - b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "add.py")
	git(repo, "commit", "-qm", "trial base")
	base := git(repo, "rev-parse", "HEAD")
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 110000}
	payload, _ := json.Marshal(invocationPayload{Provider: string(adapter.ProviderClaude), ProviderLock: &lock, Profile: &profile, Directory: work, Prompt: "Fix the small function in add.py so add(a, b) returns a + b. Edit only add.py using the native file edit tool. Do not run Git, delegate, read credentials, or edit any other file. The Host freezes the commit after you exit. Report what you changed, and report permission rejection honestly.", CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"add.py"}}})
	grant := contract.LaunchCommand{RunID: "real-candidate", TaskID: "implement", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", ReservationID: "reservation", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 110000, AdapterPayload: payload}
	inv, err := invocationForGrant(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", "launch"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := h.ExecuteLaunch(ctx, grant, inv)
	record := map[string]any{"provider_version": lock.Binary.Version, "provider_sha256": lock.Binary.SHA256, "result": result, "runtime_root": root, "base_oid": base}
	if runErr != nil {
		record["error"] = runErr.Error()
	}
	body, _ := json.MarshalIndent(record, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "evidence.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if runErr != nil || result.Status != "result_ready" {
		t.Fatalf("real implementer: status=%s exit=%d error=%v; evidence retained", result.Status, result.ExitCode, runErr)
	}
	content, err := os.ReadFile(filepath.Join(work, "add.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "return a + b") {
		t.Fatal("actual model edit did not satisfy task")
	}
	body, err = os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate gitops.CandidateReceipt `json:"candidate"`
	}
	if err = json.Unmarshal(body, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Candidate.CandidateOID == "" {
		t.Fatal("missing frozen candidate")
	}
	validation := filepath.Join(root, "validation")
	git(repo, "worktree", "add", "--detach", validation, artifact.Candidate.CandidateOID)
	check := exec.CommandContext(ctx, "python3", "-c", "from add import add; assert add(2,3)==5; assert add(-2,3)==1")
	check.Dir = validation
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("candidate behavior: %v: %s", err, out)
	}
	if git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("user branch moved")
	}
	// Exercise the new production Host stages with an independently launched
	// real reviewer. The harness remains the owner decision maker in this test.
	previousGrant, previousResult := grant, result
	stageRecords := []map[string]any{}
	for _, stage := range []string{"validate", "review", "integrate"} {
		target := adapter.CandidateTarget{Worktree: repo, Ref: "refs/heads/main", BaseOID: base}
		action := &adapter.CandidateAction{Version: 1, Operation: stage, SourceTask: previousGrant.TaskID, Target: target}
		request := invocationPayload{Kind: "candidate", Directory: filepath.Join(root, "host-"+stage), CandidateAction: action}
		if stage == "validate" {
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			python, err = filepath.EvalSymlinks(python)
			if err != nil {
				t.Fatal(err)
			}
			action.Command = []string{python, "-c", "from add import add; assert add(2,3)==5; assert add(-2,3)==1"}
		}
		if stage == "review" {
			request.Kind = ""
			request.Provider = string(adapter.ProviderClaude)
			request.ProviderLock = &lock
			request.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 180000}
			request.Prompt = "Review the change in add.py. Acceptance: add(a,b) returns the arithmetic sum for positive and negative numbers. Read the actual candidate file; report real defects. Do not edit files or request broader permissions."
		}
		if stage == "integrate" {
			request.Directory = repo
		}
		body, _ := json.Marshal(request)
		info, err := os.Stat(previousResult.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		next := contract.LaunchCommand{RunID: grant.RunID, TaskID: stage, AttemptID: "attempt-" + stage, SegmentID: "segment-" + stage, CommandID: "command-" + stage, ReservationID: "reservation-" + stage, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 180000, AdapterPayload: body, Input: &contract.AcceptedInput{DecisionCommandID: "accept-" + previousGrant.TaskID, AdapterPayload: previousGrant.AdapterPayload, Event: contract.Event{RunID: grant.RunID, TaskID: previousGrant.TaskID, AttemptID: previousGrant.AttemptID, SegmentID: previousGrant.SegmentID, WorkRevision: 1, Kind: contract.EventResult, EventID: "event-" + previousGrant.TaskID, EventRevision: 1, PayloadHash: previousResult.OutputHash, Artifact: &contract.ArtifactRef{Path: previousResult.ArtifactPath, Size: info.Size(), SHA256: previousResult.OutputHash}}}}
		invocation, err := invocationForGrant(ctx, next)
		if err != nil {
			t.Fatalf("%s build: %v", stage, err)
		}
		stageResult, stageErr := h.ExecuteLaunch(ctx, next, invocation)
		stageRecord := map[string]any{"stage": stage, "result": stageResult}
		if stageErr != nil {
			stageRecord["error"] = stageErr.Error()
		}
		stageRecords = append(stageRecords, stageRecord)
		evidence, _ := json.MarshalIndent(stageRecords, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "delivery-evidence.json"), evidence, 0600); err != nil {
			t.Fatal(err)
		}
		if stageErr != nil || stageResult.Status != "result_ready" {
			t.Fatalf("%s status=%s error=%v; private evidence retained", stage, stageResult.Status, stageErr)
		}
		previousGrant, previousResult = next, stageResult
	}
	if git(repo, "rev-parse", "HEAD") != artifact.Candidate.CandidateOID || git(repo, "status", "--porcelain") != "" {
		t.Fatal("real integration did not deliver the tested/reviewed candidate")
	}
	t.Log("real model edit, Host validation, independent real reviewer and exact-candidate integration passed; autonomous main-agent dispatch is not covered")

}
