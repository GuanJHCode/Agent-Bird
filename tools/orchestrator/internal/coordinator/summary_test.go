package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestSummaryPersistsOwnerScopeAndDoesNotDispatch(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "summary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	server, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	ctx := context.Background()
	var owner SubmitResponse
	for _, run := range []string{"mine", "other"} {
		body, err := server.control(ctx, ipc.KindSubmit, jsonBytes(t, SubmitRequest{
			RunID: run, ControllerThread: run, PlanRevision: 1,
			OriginContextID: run, OriginPID: 7, OriginBirth: "birth",
			HostGeneration: "g", HostExecutable: "/private/bin/orchestrator",
			Tasks: []TaskRequest{
				{ID: run + "-first", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake","prompt":"PRIVATE_PROMPT"}`)},
				{ID: run + "-dependent", Dependencies: []string{run + "-first"}, MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if run == "mine" {
			if err := json.Unmarshal(body, &owner); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Recovery reads existing rows; no conversation checkpoint or task cache.
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server, err = NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	request := TaskControlRequest{TaskID: "mine-first", ControllerThread: "mine", ControlToken: owner.ControlToken}
	body, err := server.control(ctx, ipc.Kind("summary"), jsonBytes(t, request))
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Version int            `json:"version"`
		RunID   string         `json:"run_id"`
		Counts  map[string]int `json:"counts"`
		Tasks   []struct {
			TaskID  string   `json:"task_id"`
			Status  string   `json:"status"`
			Group   string   `json:"group"`
			Actions []string `json:"allowed_actions"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Version != 1 || summary.RunID != "mine" || len(summary.Tasks) != 2 || summary.Counts["pending"] != 1 || summary.Counts["blocked"] != 1 {
		t.Fatalf("summary=%s", body)
	}
	for _, value := range []string{"other", "PRIVATE_PROMPT", owner.ControlToken, owner.LaunchToken} {
		if strings.Contains(string(body), value) {
			t.Fatalf("summary leaked private data: %s", value)
		}
	}
	for _, task := range summary.Tasks {
		if task.Status != "ready" && task.Status != "queued" {
			t.Fatalf("summary dispatched work: %s", body)
		}
		if strings.Contains(strings.Join(task.Actions, ","), "resume") {
			t.Fatalf("summary suggests implicit recovery: %s", body)
		}
	}
	for _, denied := range []TaskControlRequest{
		{TaskID: "other-first", ControllerThread: "mine", ControlToken: owner.ControlToken},
		{TaskID: "mine-first", ControllerThread: "other", ControlToken: owner.ControlToken},
		{TaskID: "mine-first", ControllerThread: "mine", ControlToken: "wrong"},
	} {
		if _, err := server.control(ctx, ipc.Kind("summary"), jsonBytes(t, denied)); err == nil {
			t.Fatal("unauthorized summary")
		}
	}
}
