package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
