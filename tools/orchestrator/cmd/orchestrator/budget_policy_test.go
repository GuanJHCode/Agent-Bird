package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
)

func TestAttemptLimitsAgreeBeforeSubmission(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 64} {
		req := dispatchRequest{Version: 1, RequestID: "r", Provider: "claude", ProviderLock: "/private/lock", Directory: "/private/repo", Prompt: "read", Role: adapter.Reviewer, Acceptance: []string{"check"}, MaxAttempts: n, MaxActiveMS: 1000}
		err := validateDispatch(req)
		if (n >= 1 && n <= 3) != (err == nil) {
			t.Fatalf("dispatch attempts %d=%v", n, err)
		}
		err = preflightTasks([]coordinator.TaskRequest{{ID: "task", MaxAttempts: n, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}})
		if (n >= 1 && n <= 3) != (err == nil) {
			t.Fatalf("plan attempts %d=%v", n, err)
		}
	}
}

func TestDispatchBudgetV2CompilesAndRequiresCoordinatorCapability(t *testing.T) {
	req := dispatchRequest{Version: 2, RequestID: "r", Provider: "claude", ProviderLock: "/private/lock", Directory: "/private/repo", Prompt: "read", Role: adapter.Reviewer, Acceptance: []string{"check"}, MaxAttempts: 2, BudgetGroupID: "shared", MaxActiveMSPerSegment: 120000, MaxActiveMSTotal: 180000}
	if err := validateDispatch(req); err != nil {
		t.Fatal(err)
	}
	p, err := dispatchPlan(req, adapter.ProviderLock{}, "run", "")
	if err != nil {
		t.Fatal(err)
	}
	task := p.Tasks[0]
	if task.BudgetPolicy == nil || task.BudgetPolicy.Version != 2 || task.BudgetPolicy.MaxActiveMSTotal != 180000 || task.MaxActiveMS != 120000 || task.BudgetGroupID != "shared" {
		t.Fatalf("policy=%+v", task)
	}
	if err = checkCoordinatorTasks([]string{"interruption_feedback_v1"}, p.Tasks); err == nil || err.Error() != "coordinator_upgrade_required" {
		t.Fatalf("old coordinator accepted v2: %v", err)
	}
	if err = checkCoordinatorTasks([]string{"interruption_feedback_v1", "budget_group_policy_v2"}, p.Tasks); err != nil {
		t.Fatal(err)
	}
	req.MaxActiveMS = 120000
	if err = validateDispatch(req); err == nil {
		t.Fatal("v2 silently accepted legacy allowance")
	}
	req.MaxActiveMS = 0
	req.Version = 1
	if err = validateDispatch(req); err == nil {
		t.Fatal("v1 silently accepted total allowance")
	}
}

func TestSubmitPreflightUsesExactSerializedFrameBytes(t *testing.T) {
	// Raw input under 64 KiB can exceed the actual JSON envelope after escaping.
	raw, _ := json.Marshal(map[string]string{"kind": "fake", "prompt": strings.Repeat("<", 4000) + strings.Repeat("中", 1000)})
	request := coordinator.SubmitRequest{Tasks: []coordinator.TaskRequest{{ID: "a", MaxAttempts: 1, AdapterPayload: raw}, {ID: "b", MaxAttempts: 1, AdapterPayload: raw}, {ID: "c", MaxAttempts: 1, AdapterPayload: raw}}}
	requestID := "control-123-1793840000000000000"
	if err := preflightSubmitFrame(request, requestID); err != ipc.ErrMessageTooLarge {
		t.Fatalf("oversize frame=%v", err)
	}
	request.Tasks = request.Tasks[:1]
	var expected bytes.Buffer
	payload, _ := json.Marshal(request)
	if err := ipc.Write(&expected, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindSubmit, RequestID: requestID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := preflightSubmitFrame(request, requestID); err != nil {
		t.Fatal(err)
	}
	// The same precheck also happens before a socket connection can be attempted.
	huge := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindSubmit, RequestID: requestID, Payload: json.RawMessage(`{"s":"` + strings.Repeat("x", ipc.MaxMessageSize) + `"}`)}
	if _, err := ipc.Call(context.Background(), "/private/nonexistent-socket", huge); err != ipc.ErrMessageTooLarge {
		t.Fatalf("oversized request reached connect: %v", err)
	}
}

func TestAdapterLimitsIncludeUTF8AndJSONEscaping(t *testing.T) {
	for _, text := range []string{strings.Repeat("中", 6000), strings.Repeat("<", 3000), strings.Repeat("\\\"", 5000)} {
		raw, _ := json.Marshal(map[string]string{"kind": "fake", "prompt": text})
		if err := preflightTasks([]coordinator.TaskRequest{{ID: "a", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: raw}}); err == nil || err.Error() != "adapter_payload_too_large" {
			t.Fatalf("encoded %d bytes accepted: %v", len(raw), err)
		}
	}
	raw, _ := json.Marshal(map[string]string{"kind": "fake", "prompt": "保留完整合同"})
	if err := contract.ValidateAdapterSize(raw); err != nil {
		t.Fatal(err)
	}
}

func TestControlIDBytesRejectedInPreflight(t *testing.T) {
	for _, id := range []string{strings.Repeat("g", 64800), strings.Repeat("中", 43)} {
		task := coordinator.TaskRequest{ID: "task", BudgetGroupID: id, MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
		if err := preflightTasks([]coordinator.TaskRequest{task}); err == nil {
			t.Fatal("oversized group passed CLI preflight")
		}
	}
	for _, id := range []string{strings.Repeat("x", 128), strings.Repeat("中", 42)} {
		task := coordinator.TaskRequest{ID: id, BudgetGroupID: id, MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
		if err := preflightTasks([]coordinator.TaskRequest{task}); err != nil {
			t.Fatalf("bounded ID rejected: %v", err)
		}
	}
}
