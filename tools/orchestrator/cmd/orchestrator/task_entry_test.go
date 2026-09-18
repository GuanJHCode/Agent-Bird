package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskSubmitRejectsOwnerOverrides(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(p, map[string]any{"run_id": "r", "plan_revision": 1, "origin_pid": 1, "tasks": []any{}}); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"task", "submit", "--request", p, "--handle", filepath.Join(root, "handle.json")}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "invalid_json" {
		t.Fatalf("got %v", err)
	}
}

func TestTaskSubmitExistingHandleNeverResubmits(t *testing.T) {
	root := privateTaskTemp(t)
	var err error
	handle := filepath.Join(root, "handle.json")
	original := []byte(`{"status":"submitting"}`)
	if err = os.WriteFile(handle, original, 0600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "plan.json")
	if err = writeExclusiveJSON(p, map[string]any{"run_id": "r", "plan_revision": 1, "tasks": []any{map[string]any{"id": "t", "max_attempts": 1, "max_active_ms": 1000, "adapter": map[string]any{"kind": "provider"}}}}); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"task", "submit", "--request", p, "--handle", handle}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "task_handle_exists" {
		t.Fatalf("got %v", err)
	}
	after, err := os.ReadFile(handle)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("overwrote uncertain task")
	}
}

func TestTaskRunMissingWorkspaceDoesNotBindOrCreateHandle(t *testing.T) {
	root := privateTaskTemp(t)
	var err error
	prompt := filepath.Join(root, "prompt.txt")
	if err = os.WriteFile(prompt, []byte("Review only"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "lock.json")
	if err = writeExclusiveJSON(lock, map[string]any{"version": 1}); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	state := filepath.Join(root, "state")
	err = run(context.Background(), []string{"task", "run", "--run-id", "r", "--directory", filepath.Join(root, "missing"), "--prompt-file", prompt, "--provider-lock", lock, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "profile_workspace_untrusted" {
		t.Fatalf("got %v", err)
	}
	for _, p := range []string{state, handle} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("created %s", p)
		}
	}
}

func TestTaskHandleScopeRejectsForeignTaskBeforeReadingControl(t *testing.T) {
	root := privateTaskTemp(t)
	var err error
	path := filepath.Join(root, "handle.json")
	h := map[string]any{"version": 1, "run_id": "r", "state_dir": root, "control_file": filepath.Join(root, "absent-control.json"), "task_ids": []string{"t"}, "status": "queued"}
	if err = writeExclusiveJSON(path, h); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"inspect", "collect", "wait"} {
		err = run(context.Background(), []string{"task", action, "--handle", path, "--task-id", "foreign"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || err.Error() != "task_handle_scope_mismatch" {
			t.Fatalf("%s: %v", action, err)
		}
	}
	decision := filepath.Join(root, "decision.json")
	if err = writeExclusiveJSON(decision, map[string]any{"task_id": "t", "review_task_id": "foreign"}); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"task", "rework", "--handle", path, "--request", decision}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "task_handle_scope_mismatch" {
		t.Fatalf("rework: %v", err)
	}
}

func TestTaskHandleNeverAcceptsControlOverride(t *testing.T) {
	root := privateTaskTemp(t)
	var err error
	h := filepath.Join(root, "handle.json")
	p := filepath.Join(root, "decision.json")
	if err = writeExclusiveJSON(h, map[string]any{"version": 1, "run_id": "r", "state_dir": root, "control_file": filepath.Join(root, "control"), "task_ids": []string{"t"}, "status": "queued"}); err != nil {
		t.Fatal(err)
	}
	if err = writeExclusiveJSON(p, map[string]any{"task_id": "t", "control_file": "/other"}); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"task", "accept", "--handle", h, "--request", p}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "task_control_override_forbidden" {
		t.Fatalf("got %v", err)
	}
	var original map[string]any
	if err = readPrivateJSON(p, &original); err != nil {
		t.Fatal(err)
	}
	if original["control_file"] != "/other" {
		t.Fatal("rewrote decision")
	}
}

func privateTaskTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

// The fixture only implements version/help; model execution is never allowed.
func TestTaskRunChecksProviderBeforeBinding(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	for _, tc := range []struct {
		name, help, want string
		tamper           bool
	}{
		{"supported", "--output-format --input-format --permission-mode --disallowed-tools", "current_codex_thread_unavailable", false},
		{"missing_guard", "--output-format --input-format --permission-mode", "provider_capability_unsupported", false},
		{"changed_binary", "--output-format --input-format --permission-mode --disallowed-tools", "binary_sha256_mismatch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTaskTemp(t)
			bin := filepath.Join(root, "provider")
			body := []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo fixture-v1;;\n--help) echo '" + tc.help + "';;\n*) exit 99;;\nesac\n")
			if err := os.WriteFile(bin, body, 0700); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(body)
			lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: bin, Version: "fixture-v1", SHA256: hex.EncodeToString(hash[:])}}
			lockPath := filepath.Join(root, "lock.json")
			if err := writeExclusiveJSON(lockPath, lock); err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				if err := os.WriteFile(bin, append(body, []byte("# changed\n")...), 0700); err != nil {
					t.Fatal(err)
				}
			}
			prompt := filepath.Join(root, "prompt.txt")
			if err := os.WriteFile(prompt, []byte("read only"), 0600); err != nil {
				t.Fatal(err)
			}
			handle, state := filepath.Join(root, "handle.json"), filepath.Join(root, "state")
			err := run(context.Background(), []string{"task", "run", "--run-id", "r", "--directory", root, "--prompt-file", prompt, "--provider-lock", lockPath, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v", err)
			}
			for _, path := range []string{handle, state} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("created runtime before preflight completed")
				}
			}
		})
	}
}
