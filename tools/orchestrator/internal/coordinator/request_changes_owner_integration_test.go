package coordinator

import (
	"encoding/json"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// Every refs phase shares the same current-owner boundary as legacy rework.
func TestRefsPhasesKeepCurrentOwnerAdmission(t *testing.T) {
	s, ctx, _ := projectionServer(t)
	receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{
		Run:   store.RunSpec{ID: "refs-run", ControllerThread: "refs-owner", PlanRevision: 1, OriginContextID: "refs-origin", OriginPID: 99999999, OriginBirth: "retired", DeliveryMode: "collect"},
		Host:  store.HostLaunchSpec{OriginContextID: "refs-origin", HostGeneration: "refs-generation", Executable: "/private/bin/orchestrator"},
		Tasks: []store.TaskSpec{{ID: "refs-task", RunID: "refs-run", MaxAttempts: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"begin", "chunk", "commit", "abort", "gc"} {
		for _, valid := range []bool{false, true} {
			label := phase + "/wrong-control"
			token, want := "wrong-token", "owner_mismatch"
			if valid {
				label = phase + "/retired-owner"
				token = receipt.ControlToken
				want = "origin_owner_lost"
			}
			t.Run(label, func(t *testing.T) {
				raw, _ := json.Marshal(map[string]any{"task_id": "refs-task", "controller_thread": "refs-owner", "control_token": token, "work_revision": 1, "refs_v3": map[string]any{"phase": phase}})
				response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindRequestChanges, RequestID: label, Payload: raw})
				if err != nil || response.Kind != ipc.KindError || !strings.Contains(string(response.Payload), want) {
					t.Fatalf("phase=%s response=%s err=%v", phase, response.Payload, err)
				}
				snapshot, err := s.db.TaskSnapshot(ctx, "refs-task")
				if err != nil || snapshot.Status != "ready" {
					t.Fatalf("rejected phase changed task: %+v %v", snapshot, err)
				}
			})
		}
	}
}

func TestRefsAndManagedOwnerCapabilitiesCoexist(t *testing.T) {
	s, ctx, _ := projectionServer(t)
	response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindReady, RequestID: "combined-features", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Capabilities []string `json:"capabilities"`
	}
	if err = json.Unmarshal(response.Payload, &ready); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owner_request_changes_v2", "owner_rework_refs_v3", "managed_owner_projection_v1", "budget_group_policy_v2"} {
		found := false
		for _, got := range ready.Capabilities {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing negotiated feature %q: %s", want, response.Payload)
		}
	}
}
