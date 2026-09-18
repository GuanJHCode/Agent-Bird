package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
)

func TestPreflightCanonicalizesReadOnlyWorkspaceWithoutRuntime(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(root, "workspace")
	if err = os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	request := coordinator.SubmitRequest{Tasks: []coordinator.TaskRequest{{ID: "check", AdapterPayload: json.RawMessage(`{"kind":"provider","provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000},"directory":` + string(mustJSON(t, alias)) + `}`)}}}
	path := filepath.Join(root, "request.json")
	if err = writeExclusiveJSON(path, request); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = run(context.Background(), []string{"preflight", "--request", path}, &out, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	var result struct {
		Status string                    `json:"status"`
		Tasks  []coordinator.TaskRequest `json:"tasks"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "preflight_passed" || len(result.Tasks) != 1 {
		t.Fatalf("%s", out.String())
	}
	p, err := adapter.DecodeInvocationPayload(result.Tasks[0].AdapterPayload)
	if err != nil || p.Directory != actual || p.Profile.Permission != adapter.ReadOnly {
		t.Fatalf("canonical scope: %+v %v", p, err)
	}
	var original coordinator.SubmitRequest
	if err = readPrivateJSON(path, &original); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Tasks[0].AdapterPayload, request.Tasks[0].AdapterPayload) {
		t.Fatal("rewrote input evidence")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestPreflightPreservesLegacyGitopsProtocol(t *testing.T) {
	raw := json.RawMessage(`{"kind":"gitops","operation":"prepare","repo_root":"/repository"}`)
	tasks := []coordinator.TaskRequest{{ID: "legacy", AdapterPayload: raw}}
	if err := preflightTasks(tasks); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tasks[0].AdapterPayload, raw) {
		t.Fatal("rewrote legacy protocol")
	}
}

func TestSubmitInvalidWorkspaceFailsBeforeOwnerAndRuntime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "request.json")
	state := filepath.Join(root, "state")
	req := coordinator.SubmitRequest{Tasks: []coordinator.TaskRequest{{ID: "check", AdapterPayload: json.RawMessage(`{"kind":"provider","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000},"directory":` + string(mustJSON(t, filepath.Join(root, "missing"))) + `}`)}}}
	if err := writeExclusiveJSON(path, req); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"submit", "--request", path, "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "profile_workspace_untrusted" {
		t.Fatalf("got %v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("runtime created for invalid workspace")
	}
}
