package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestReportRetentionPublicCLIPolicyAndSavedPlan(t *testing.T) {
	testDiagnosticsPublicCLI(t, true, false, false, true)
}

// Reuse the controlled original owner/CLI fixture; this verifies real IPC,
// private request files, owner binding, and exact saved-plan round-tripping.
func testReportRetentionPublicRequests(t *testing.T, ctx context.Context, root, state, control, runID, token string, db *store.DB, server *coordinator.Server, invoke func(...string) []byte) {
	t.Helper()
	for _, step := range []struct {
		action   string
		expected int64
		enabled  bool
		revision int64
	}{{"status", 0, false, 0}, {"enable", 0, true, 1}, {"disable", 1, false, 2}, {"status", 0, false, 2}} {
		path := filepath.Join(root, "report-gc-"+step.action+fmt.Sprint(step.revision)+".json")
		if err := writeExclusiveJSON(path, store.ReportGCRequest{Version: 1, RunID: runID, ExpectedRevision: step.expected}); err != nil {
			t.Fatal(err)
		}
		raw := invoke("report-gc-"+step.action, "--state-dir", state, "--control-file", control, "--request", path)
		var status struct {
			Enabled  bool  `json:"enabled"`
			Revision int64 `json:"revision"`
		}
		if err := json.Unmarshal(raw, &status); err != nil || status.Enabled != step.enabled || status.Revision != step.revision {
			t.Fatalf("unexpected policy response %s: %v", raw, err)
		}
		if step.enabled {
			deadline := time.Now().Add(time.Second)
			for {
				var tick string
				if err := db.SQL().QueryRow(`SELECT last_tick FROM report_gc_policies WHERE run_id=?`, runID).Scan(&tick); err != nil {
					t.Fatal(err)
				}
				if tick != "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("GC enable did not wake an automatic report round")
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	foreign, _ := json.Marshal(coordinator.ReportRetentionRequest[store.ReportGCRequest]{ControllerThread: "foreign", ControlToken: token, Request: store.ReportGCRequest{Version: 1, RunID: runID, ExpectedRevision: 2}})
	reply, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindReportGCEnable, RequestID: "report-foreign", Payload: foreign})
	if err != nil || reply.Kind != ipc.KindError || !bytes.Contains(reply.Payload, []byte("owner_mismatch")) {
		t.Fatal(reply, err)
	}
	bad := []byte(`{"controller_thread":"controller","control_token":"` + token + `","request":{"version":1,"run_id":"` + runID + `","expected_revision":2,"unexpected":true}}`)
	reply, err = ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindReportGCEnable, RequestID: "report-invalid", Payload: bad})
	if err != nil || reply.Kind != ipc.KindError {
		t.Fatal(reply, err)
	}
	path := filepath.Join(root, "report-plan.json")
	if err = writeExclusiveJSON(path, store.ReportArtifactRetentionPlanRequest{Version: 1, RunID: runID, RequestID: "report-plan", Limit: 8}); err != nil {
		t.Fatal(err)
	}
	plan := invoke("report-artifact-retention-plan", "--state-dir", state, "--control-file", control, "--request", path)
	var parsed store.ReportArtifactRetentionPlan
	if err = json.Unmarshal(plan, &parsed); err != nil || parsed.RunID != runID {
		t.Fatal(string(plan), err)
	}
	path = filepath.Join(root, "report-saved-plan.json")
	if err = writeExclusiveJSON(path, parsed); err != nil {
		t.Fatal(err)
	}
	first := invoke("report-artifact-retention-apply", "--state-dir", state, "--control-file", control, "--request", path)
	second := invoke("report-artifact-retention-apply", "--state-dir", state, "--control-file", control, "--request", path)
	if !bytes.Equal(first, second) {
		t.Fatalf("saved plan replay changed: %s vs %s", first, second)
	}
}
