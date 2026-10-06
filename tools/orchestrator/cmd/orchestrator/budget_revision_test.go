package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const budgetRevisionCLIRequest = `{"budget_group_id":"task","expected_revision":0,"command_id":"increase","group_max_attempts":4,"group_max_active_ms":1500,"tasks":[{"task_id":"task","work_revision":1,"max_attempts":4,"attempt_max_active_ms":700,"task_max_active_ms":900,"segment_max_active_ms":400}]}`

func TestBudgetRevisionPublicCLIAuthenticatesAndProjects(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "budget-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("budget coordinator did not stop")
		}
	}()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	response, err := call(ctx, state, ipc.KindSubmit, coordinator.SubmitRequest{RunID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation", HostExecutable: "/private/bin/orchestrator", Tasks: []coordinator.TaskRequest{{ID: "task", MaxAttempts: 3, MaxActiveMS: 500, ExecutionPolicy: &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 1000, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 200}}}})
	if err != nil {
		t.Fatal(err)
	}
	var submitted coordinator.SubmitResponse
	if err = json.Unmarshal(response.Payload, &submitted); err != nil {
		t.Fatal(err)
	}
	// The bootstrap intentionally does not exist: revision must not start or
	// resurrect an execution owner. Only an explicit retry/resume may do so.
	control, err := writeControlCapability(state, "run", "owner", submitted.ControlToken, filepath.Join(root, "absent-bootstrap"), submitted.LaunchID, submitted.LaunchToken)
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	if err = os.WriteFile(request, []byte(budgetRevisionCLIRequest), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"budget-revise", "--state-dir", state, "--task-id", "task", "--control-file", control, "--request", request}
	var out bytes.Buffer
	if err = run(ctx, args, &out, &out); err != nil {
		t.Fatal("public revision did not reach Store", err)
	}
	var receipt store.BudgetRevisionReceipt
	if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Revision != 1 {
		t.Fatal("invalid receipt", err)
	}
	out.Reset()
	if err = run(ctx, args, &out, &out); err != nil {
		t.Fatal("exact CLI replay failed", err)
	}
	var replay store.BudgetRevisionReceipt
	if err = json.Unmarshal(out.Bytes(), &replay); err != nil || replay != receipt {
		t.Fatal("exact replay changed receipt", err)
	}
	controlRequest := coordinator.TaskControlRequest{TaskID: "task", ControllerThread: "owner", ControlToken: submitted.ControlToken}
	status, err := call(ctx, state, ipc.KindStatus, controlRequest)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot store.TaskSnapshot
	if err = json.Unmarshal(status.Payload, &snapshot); err != nil || snapshot.EffectiveBudget == nil || snapshot.EffectiveBudget.Revision != 1 || snapshot.EffectiveBudget.SegmentMaxActiveMS != 400 || snapshot.AttemptID != "" || snapshot.Status != "ready" {
		t.Fatal("revision status or no-start contract failed", err)
	}
	summary, err := call(ctx, state, ipc.KindSummary, controlRequest)
	if err != nil {
		t.Fatal(err)
	}
	var projected store.RunSummary
	if err = json.Unmarshal(summary.Payload, &projected); err != nil || len(projected.Tasks) != 1 || projected.Tasks[0].EffectiveBudget == nil || *projected.Tasks[0].EffectiveBudget != *snapshot.EffectiveBudget || projected.Tasks[0].ExecutionPolicy.SegmentMaxActiveMS != 200 {
		t.Fatal("summary lost effective/baseline distinction", err)
	}
	for _, test := range []struct{ name, token, controller, body, want string }{
		{"token", "wrong", "owner", budgetRevisionCLIRequest, "owner_mismatch"},
		{"controller", submitted.ControlToken, "wrong", budgetRevisionCLIRequest, "owner_mismatch"},
		{"duplicate", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"expected_revision":0`, `"expected_revision":0,"expected_revision":1`, 1), "budget_revision_invalid"},
		{"nested_duplicate", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"max_attempts":4`, `"max_attempts":4,"max_attempts":5`, 1), "budget_revision_invalid"},
		{"unknown", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"command_id"`, `"forged":true,"command_id"`, 1), "budget_revision_invalid"},
		{"null_revision", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"expected_revision":0`, `"expected_revision":null`, 1), "budget_revision_invalid"},
		{"missing_revision", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"expected_revision":0,`, ``, 1), "budget_revision_invalid"},
		{"stale", submitted.ControlToken, "owner", strings.Replace(budgetRevisionCLIRequest, `"increase"`, `"new-command"`, 1), "conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := json.Marshal(map[string]any{"task_id": "task", "controller_thread": test.controller, "control_token": test.token, "request": json.RawMessage(test.body)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = call(ctx, state, ipc.Kind("budget_revise"), json.RawMessage(wire))
			if err == nil || err.Error() != test.want {
				t.Fatalf("expected %s, got %v", test.want, err)
			}
		})
	}
}

func TestBudgetRevisionCLIRejectsUnsafeRequestBeforeControl(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"duplicate", "nested_duplicate", "unknown", "case_alias", "trailing", "missing_revision", "null_revision", "oversize", "permissions", "symlink", "hardlink"} {
		t.Run(name, func(t *testing.T) {
			body := budgetRevisionCLIRequest
			want := "budget_revision_invalid"
			switch name {
			case "duplicate":
				body = strings.Replace(body, `"command_id":"increase"`, `"command_id":"increase","command_id":"other"`, 1)
			case "nested_duplicate":
				body = strings.Replace(body, `"task_id":"task"`, `"task_id":"task","task_id":"other"`, 1)
			case "unknown":
				body = strings.Replace(body, `"command_id"`, `"control_token":"forged","command_id"`, 1)
			case "case_alias":
				body = strings.Replace(body, `"command_id"`, `"Command_id"`, 1)
			case "trailing":
				body += `{}`
			case "missing_revision":
				body = strings.Replace(body, `"expected_revision":0,`, ``, 1)
			case "null_revision":
				body = strings.Replace(body, `"expected_revision":0`, `"expected_revision":null`, 1)
			case "oversize":
				body += strings.Repeat(" ", 64*1024)
				want = "budget_revision_request_file_invalid"
			default:
				want = "budget_revision_request_file_invalid"
			}
			path := filepath.Join(root, name+".json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "hardlink":
				if err := os.Link(path, path+".link"); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := run(context.Background(), []string{"budget-revise", "--task-id", "task", "--control-file", filepath.Join(root, "absent-control"), "--request", path}, &out, &out)
			if err == nil || err.Error() != want {
				t.Fatalf("expected %s before opening control, got %v", want, err)
			}
		})
	}
}
