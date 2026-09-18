package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// Called with a real Host-frozen candidate. Removing the candidate-action
// handoff must fail before the target branch can change.
func exerciseCandidateDelivery(t *testing.T, root, repo, base string, source contract.LaunchCommand, result contract.Result, git func(string, ...string) string) {
	t.Helper()
	target := filepath.Join(root, "delivery-target")
	git(repo, "worktree", "add", "-b", "delivery", target, base)
	targetSpec := map[string]any{"worktree": target, "ref": "refs/heads/delivery", "base_oid": base}
	input := func(g contract.LaunchCommand, r contract.Result) *contract.AcceptedInput {
		info, err := os.Stat(r.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		return &contract.AcceptedInput{DecisionCommandID: "accept-" + g.TaskID, AdapterPayload: g.AdapterPayload, Event: contract.Event{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, Kind: contract.EventResult, EventID: "event-" + g.TaskID, EventRevision: 1, PayloadHash: r.OutputHash, Artifact: &contract.ArtifactRef{Path: r.ArtifactPath, SHA256: r.OutputHash, Size: info.Size()}}}
	}
	run := func(name string, payload map[string]any, from contract.LaunchCommand, r contract.Result, wantError ...bool) (contract.LaunchCommand, contract.Result) {
		t.Helper()
		body, _ := json.Marshal(payload)
		grant := contract.LaunchCommand{RunID: source.RunID, TaskID: name, AttemptID: "attempt-" + name, SegmentID: "segment-" + name, CommandID: "command-" + name, ReservationID: "reservation-" + name, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: body, Input: input(from, r)}
		inv, err := invocationForGrant(context.Background(), grant)
		if err != nil {
			if len(wantError) > 0 && wantError[0] {
				return grant, contract.Result{Status: "failed"}
			}
			t.Fatalf("%s invocation: %v", name, err)
		}
		h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", "delivery-"+name), "producer")
		if err != nil {
			t.Fatal(err)
		}
		out, err := h.ExecuteLaunch(context.Background(), grant, inv)
		if err != nil {
			if len(wantError) > 0 && wantError[0] && out.Status == "failed" {
				return grant, out
			}
			t.Fatalf("%s execution: %s %v", name, out.Status, err)
		}
		if len(wantError) > 0 && wantError[0] {
			t.Fatalf("%s accepted forbidden input", name)
		}
		return grant, out
	}
	action := func(operation, sourceTask string) map[string]any {
		return map[string]any{"version": 1, "operation": operation, "source_task": sourceTask, "target": targetSpec}
	}
	failedAction := action("validate", source.TaskID)
	failedAction["command"] = []string{"/usr/bin/false"}
	_, failed := run("failed-check", map[string]any{"kind": "candidate", "directory": filepath.Join(root, "failed-validation"), "candidate_action": failedAction}, source, result)
	if failed.Status != "failed" {
		t.Fatalf("failed test was accepted: %+v", failed)
	}
	var failedEvidence host.CandidateDelivery
	failedBody, _ := os.ReadFile(failed.ArtifactPath)
	if json.Unmarshal(failedBody, &failedEvidence) != nil || failedEvidence.Validation == nil || failedEvidence.Validation.Passed || failedEvidence.Validation.ExitCode == 0 {
		t.Fatal("failed validation evidence lost")
	}
	a := action("validate", source.TaskID)
	a["command"] = []string{"/usr/bin/grep", "-q", "worker implementation", "README"}
	checked, checkResult := run("check", map[string]any{"kind": "candidate", "directory": filepath.Join(root, "validation"), "candidate_action": a}, source, result)
	if checkResult.Status != "result_ready" {
		t.Fatalf("validation failed: %+v", checkResult)
	}
	var reviewPayload map[string]any
	if err := json.Unmarshal(source.AdapterPayload, &reviewPayload); err != nil {
		t.Fatal(err)
	}
	delete(reviewPayload, "candidate_workspace")
	reviewPayload["directory"] = filepath.Join(root, "review")
	reviewPayload["candidate_action"] = action("review", "check")
	reviewPayload["profile"] = map[string]any{"version": 1, "role": "reviewer", "permission": "read-only", "timeout_ms": 15000}
	reviewPayload["prompt"] = "Independently review the candidate"
	if os.Getenv("GROK_REVIEW_FIXTURE") != "" {
		reviewPayload["provider"] = "grok-build"
		lock := reviewPayload["provider_lock"].(map[string]any)
		lock["provider"], lock["protocol"] = "grok-build", "grok-streaming-json-v1"
		lock["binary"].(map[string]any)["version"] = "grok 1.0.34 (3736acbc8658)"
		reviewPayload["profile"].(map[string]any)["grok_session_write"] = true
	}
	// Native schema absence and invalid fields fail closed; never fall back to
	// a valid-looking result string or trust Provider-side schema validation.
	for _, mode := range []string{"review_unstructured", "review_invalid"} {
		reviewPayload["directory"] = filepath.Join(root, mode)
		t.Setenv("CANDIDATE_FIXTURE_MODE", mode)
		_, invalid := run(mode, reviewPayload, checked, checkResult)
		body, _ := os.ReadFile(invalid.ArtifactPath)
		if invalid.Status != "failed" || !strings.Contains(string(body), "candidate_review_invalid") {
			t.Fatalf("invalid native structured result accepted: %s", body)
		}
	}
	// Rejected reviews preserve findings and cannot authorize integration.
	reviewPayload["directory"] = filepath.Join(root, "review-rejected")
	t.Setenv("CANDIDATE_FIXTURE_MODE", "review_reject")
	rejected, rejectedResult := run("review-rejected", reviewPayload, checked, checkResult)
	rejectedBody, _ := os.ReadFile(rejectedResult.ArtifactPath)
	if rejectedResult.Status != "failed" || !strings.Contains(string(rejectedBody), `"decision":"reject"`) {
		t.Fatal("review rejection or feedback lost")
	}
	run("blocked-rejected-integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": action("integrate", "review-rejected")}, rejected, rejectedResult, true)
	if git(target, "rev-parse", "HEAD") != base {
		t.Fatal("rejected review changed target")
	}
	// Changing bytes under an accepted artifact hash must fail before launch.
	tampered := checkResult
	tampered.ArtifactPath = filepath.Join(root, "tampered.json")
	if err := os.WriteFile(tampered.ArtifactPath, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	run("blocked-tamper", reviewPayload, checked, tampered, true)
	reviewPayload["directory"] = filepath.Join(root, "review")
	t.Setenv("CANDIDATE_FIXTURE_MODE", "review_approve")
	reviewed, reviewResult := run("review", reviewPayload, checked, checkResult)
	if reviewResult.Status != "result_ready" {
		body, _ := os.ReadFile(reviewResult.ArtifactPath)
		t.Fatalf("review failed: %s", body)
	}
	if os.Getenv("GROK_REVIEW_FIXTURE") != "" {
		var evidence host.CandidateDelivery
		body, _ := os.ReadFile(reviewResult.ArtifactPath)
		if json.Unmarshal(body, &evidence) != nil || len(evidence.ReviewInputSHA256) != 64 || len(evidence.ReviewSnapshotSHA256) != 64 || evidence.ReviewScope != "complete-tracked-text-base-and-candidate" {
			t.Fatalf("unbound snapshot review: %s", body)
		}
	}
	git(target, "checkout", "-b", "drifted")
	_, drifted := run("drift-integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": action("integrate", "review")}, reviewed, reviewResult)
	if drifted.Status != "failed" || git(target, "symbolic-ref", "HEAD") != "refs/heads/drifted" || git(target, "rev-parse", "HEAD") != base {
		t.Fatal("target drift was not rejected")
	}
	git(target, "checkout", "delivery")
	_, integrated := run("integrate", map[string]any{"kind": "candidate", "directory": target, "candidate_action": action("integrate", "review")}, reviewed, reviewResult)
	if integrated.Status != "result_ready" {
		body, _ := os.ReadFile(integrated.ArtifactPath)
		t.Fatalf("integration failed: %s", body)
	}
	var candidate struct {
		Candidate struct {
			OID string `json:"candidate_oid"`
		} `json:"candidate"`
	}
	body, _ := os.ReadFile(result.ArtifactPath)
	if json.Unmarshal(body, &candidate) != nil || candidate.Candidate.OID == "" {
		t.Fatal("missing candidate")
	}
	if git(target, "rev-parse", "HEAD") != candidate.Candidate.OID || git(target, "status", "--porcelain") != "" || git(repo, "rev-parse", "HEAD") != base {
		t.Fatal("integration did not deliver exactly frozen version")
	}
}
