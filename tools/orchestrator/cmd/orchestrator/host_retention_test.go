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

func TestHostRetentionPublicCLISmoke(t *testing.T) {
	testDiagnosticsPublicCLI(t, true, false, false, false, true)
}

func testHostRetentionPublicRequests(t *testing.T, ctx context.Context, root, state, control, runID, token string, db *store.DB, server *coordinator.Server, invoke func(...string) []byte) {
	t.Helper()
	for _, step := range []struct {
		action             string
		expected, revision int64
		enabled            bool
	}{
		{"status", 0, 0, false}, {"enable", 0, 1, true}, {"disable", 1, 2, false}, {"status", 0, 2, false},
	} {
		path := filepath.Join(root, "host-gc-"+step.action+fmt.Sprint(step.revision)+".json")
		if err := writeExclusiveJSON(path, store.HostGCRequest{Version: 1, RunID: runID, ExpectedRevision: step.expected}); err != nil {
			t.Fatal(err)
		}
		raw := invoke("host-gc-"+step.action, "--state-dir", state, "--control-file", control, "--request", path)
		var status store.HostGCStatus
		if err := json.Unmarshal(raw, &status); err != nil || status.Enabled != step.enabled || status.Revision != step.revision {
			t.Fatalf("unexpected host GC response %s: %v", raw, err)
		}
		if step.enabled {
			deadline := time.Now().Add(time.Second)
			for {
				var tick string
				if err := db.SQL().QueryRow(`SELECT last_tick FROM host_gc_policies WHERE run_id=?`, runID).Scan(&tick); err != nil {
					t.Fatal(err)
				}
				if tick != "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("host GC enable did not wake an automatic round")
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	foreign, _ := json.Marshal(coordinator.HostRetentionRequest[store.HostGCRequest]{ControllerThread: "foreign", ControlToken: token, Request: store.HostGCRequest{Version: 1, RunID: runID, ExpectedRevision: 2}})
	reply, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostGCEnable, RequestID: "host-foreign", Payload: foreign})
	if err != nil || reply.Kind != ipc.KindError || !bytes.Contains(reply.Payload, []byte("owner_mismatch")) {
		t.Fatal(reply, err)
	}
	bad := []byte(`{"controller_thread":"controller","control_token":"` + token + `","request":{"version":1,"run_id":"` + runID + `","expected_revision":2,"unexpected":true}}`)
	reply, err = ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostGCEnable, RequestID: "host-invalid", Payload: bad})
	if err != nil || reply.Kind != ipc.KindError {
		t.Fatal(reply, err)
	}
	path := filepath.Join(root, "host-plan.json")
	if err = writeExclusiveJSON(path, store.HostArtifactRetentionPlanRequest{Version: 1, RunID: runID, RequestID: "host-plan", Limit: 8}); err != nil {
		t.Fatal(err)
	}
	plan := invoke("host-artifact-retention-plan", "--state-dir", state, "--control-file", control, "--request", path)
	var parsed store.HostArtifactRetentionPlan
	if err = json.Unmarshal(plan, &parsed); err != nil || parsed.RunID != runID || len(parsed.Items) != 0 {
		t.Fatal(string(plan), err)
	}
	path = filepath.Join(root, "host-saved-plan.json")
	if err = writeExclusiveJSON(path, parsed); err != nil {
		t.Fatal(err)
	}
	first := invoke("host-artifact-retention-apply", "--state-dir", state, "--control-file", control, "--request", path)
	second := invoke("host-artifact-retention-apply", "--state-dir", state, "--control-file", control, "--request", path)
	if !bytes.Equal(first, second) {
		t.Fatalf("saved host plan replay changed: %s vs %s", first, second)
	}
}
