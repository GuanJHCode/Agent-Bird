package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCandidateRetentionPublicCLI(t *testing.T) {
	testDiagnosticsPublicCLI(t, true, false, false, false, false, true)
}

func testCandidateRetentionPublicRequests(t *testing.T, ctx context.Context, root, state, control, runID, token string, invoke func(...string) []byte) {
	t.Helper()

	planRequest := filepath.Join(root, "candidate-plan-request.json")
	if err := writeExclusiveJSON(planRequest, store.CandidateRetentionPlanRequest{Version: 1, RunID: runID, RequestID: "candidate-empty-plan", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	var plan store.CandidateRetentionPlan
	if err := json.Unmarshal(invoke("candidate-retention-plan", "--state-dir", state, "--control-file", control, "--request", planRequest), &plan); err != nil || plan.Validate() != nil || len(plan.Items) != 0 {
		t.Fatal("plan", plan, err)
	}
	planFile := filepath.Join(root, "candidate-saved-plan.json")
	if err := writeExclusiveJSON(planFile, plan); err != nil {
		t.Fatal(err)
	}
	var applied store.CandidateRetentionApplyResult
	if err := json.Unmarshal(invoke("candidate-retention-apply", "--state-dir", state, "--control-file", control, "--request", planFile), &applied); err != nil || applied.Version != 1 || applied.RunID != runID || applied.RequestID != plan.RequestID || applied.Results == nil || len(applied.Results) != 0 {
		t.Fatal("apply", applied, err)
	}
	resolution := filepath.Join(root, "candidate-resolve-request.json")
	if err := writeExclusiveJSON(resolution, store.CandidateRetentionResolutionRequest{Version: 1, RunID: runID, RequestID: "candidate-resolve", Resolution: "resolved", Reason: "test explicit resolution"}); err != nil {
		t.Fatal(err)
	}
	err := run(ctx, []string{"candidate-retention-resolve", "--state-dir", state, "--control-file", control, "--request", resolution}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "candidate_retention_pinned") {
		t.Fatalf("resolve did not reach eligibility gate: %v", err)
	}
	for _, step := range []struct {
		action             string
		expected, revision int64
		enabled            bool
	}{{"status", 0, 0, false}, {"enable", 0, 1, true}, {"disable", 1, 2, false}} {
		path := filepath.Join(root, "candidate-gc-"+step.action+fmt.Sprint(step.revision)+".json")
		if err := writeExclusiveJSON(path, store.CandidateGCRequest{Version: 1, RunID: runID, ExpectedRevision: step.expected}); err != nil {
			t.Fatal(err)
		}
		var got store.CandidateGCStatus
		if err := json.Unmarshal(invoke("candidate-gc-"+step.action, "--state-dir", state, "--control-file", control, "--request", path), &got); err != nil || got.Enabled != step.enabled || got.Revision != step.revision {
			t.Fatal(got, err)
		}
	}
	foreign, _ := json.Marshal(coordinator.CandidateRetentionRequest[store.CandidateGCRequest]{ControllerThread: "foreign", ControlToken: token, Request: store.CandidateGCRequest{Version: 1, RunID: runID, ExpectedRevision: 2}})
	reply, err := ipc.Call(ctx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindCandidateGCEnable, RequestID: "candidate-foreign", Payload: foreign})
	if err != nil || reply.Kind != ipc.KindError || !bytes.Contains(reply.Payload, []byte("owner_mismatch")) {
		t.Fatal(reply, err)
	}
}
