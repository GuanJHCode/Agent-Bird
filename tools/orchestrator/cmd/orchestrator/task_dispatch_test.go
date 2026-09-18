package main

import (
	"bytes"
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// No owner/Host/model may be started for malformed business requests.
func TestDispatchRejectsBusinessErrorsBeforeAllocation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"unknown-owner", `{"version":1,"owner_pid":1}`, "invalid_json"},
		{"missing-budget", `{"version":1,"request_id":"fix","provider":"grok","directory":".","prompt":"fix","role":"implementer","paths":["calc.py"],"acceptance":["tests"]}`, "dispatch_request_invalid"},
		{"explicit-provider", `{"version":1,"request_id":"fix","provider":"auto","provider_lock":"/missing","directory":".","prompt":"fix","role":"reviewer","acceptance":["tests"],"max_attempts":1,"max_active_ms":1000}`, "task_provider_unsupported"},
		{"missing-file-scope", `{"version":1,"request_id":"fix","provider":"claude","provider_lock":"/missing","directory":".","prompt":"fix","role":"implementer","acceptance":["tests"],"max_attempts":1,"max_active_ms":1000}`, "dispatch_paths_required"},
		{"missing-grok-grant", `{"version":1,"request_id":"fix","provider":"grok","provider_lock":"/missing","directory":".","prompt":"fix","role":"reviewer","acceptance":["tests"],"max_attempts":1,"max_active_ms":1000}`, "grok_session_write_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTaskTemp(t)
			path := filepath.Join(root, "request.json")
			state := filepath.Join(root, "state")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			err := taskEntry(context.Background(), []string{"dispatch", "--request", path, "--state-dir", state}, &bytes.Buffer{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v want %s", err, tc.want)
			}
			if _, err = os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("invalid request allocated state: %v", err)
			}
		})
	}
}

func TestDispatchRefusesDirtySourceAndFreezesCleanBase(t *testing.T) {
	root := privateTaskTemp(t)
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	if err := os.WriteFile(filepath.Join(root, "calc.py"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "calc.py")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	expected := git("rev-parse", "HEAD")
	base, err := dispatchSourceBase(context.Background(), root)
	if err != nil || base != expected {
		t.Fatalf("base=%s %v", base, err)
	}
	if err = os.WriteFile(filepath.Join(root, "calc.py"), []byte("user change"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = dispatchSourceBase(context.Background(), root); err == nil || err.Error() != "dispatch_source_dirty" {
		t.Fatalf("dirty source accepted: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "calc.py"))
	if string(body) != "user change" || git("rev-parse", "HEAD") != expected {
		t.Fatal("user state mutated")
	}
}

func TestDispatchCompilesExactOwnershipWithoutLegacyPermissions(t *testing.T) {
	req := dispatchRequest{Version: 1, RequestID: "fix", Provider: "grok", Directory: "/private/repo", Prompt: "Fix `calc.py`\n保留其他内容", Role: adapter.Implementer, Paths: []string{"calc.py"}, Acceptance: []string{"add(2,3)==5"}, Model: "cli-default", MaxAttempts: 1, MaxActiveMS: 1000, GrokSessionWrite: true}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok}
	plan, err := dispatchPlan(req, lock, "bird-test", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := adapter.DecodeInvocationPayload(plan.Tasks[0].AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Directory != "" || payload.CandidateWorkspace == nil || !payload.CandidateWorkspace.AutoDirectory || payload.CandidateWorkspace.RepoRoot != "/private/repo" || len(payload.CandidateWorkspace.Paths) != 1 || payload.CandidateWorkspace.Paths[0] != "calc.py" {
		t.Fatalf("unsafe workspace: %+v", payload)
	}
	if payload.Profile.Permission != adapter.WorkspaceWrite || !payload.Profile.CLIModelDefault || payload.Profile.Model != "" || len(payload.Allow) != 0 || len(payload.ExtraArgs) != 0 {
		t.Fatalf("expanded or substituted profile: %+v", payload)
	}
	if !strings.HasPrefix(payload.Prompt, req.Prompt) || !strings.Contains(payload.Prompt, "add(2,3)==5") {
		t.Fatal("task or acceptance lost")
	}
	if plan.Tasks[0].BudgetGroupID != "bird-test-budget" || plan.Tasks[0].MaxActiveMS != 1000 || plan.Tasks[0].MaxAttempts != 1 || plan.Tasks[0].CompletionPolicy != "owner_review" {
		t.Fatal("budget/acceptance changed")
	}
}

func TestDispatchSourceDoesNotExecutePathGit(t *testing.T) {
	root := privateTaskTemp(t)
	marker := filepath.Join(root, "executed")
	git := filepath.Join(root, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf x > '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	_, _ = dispatchSourceBase(context.Background(), root)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed PATH-controlled Git during preflight")
	}
}
