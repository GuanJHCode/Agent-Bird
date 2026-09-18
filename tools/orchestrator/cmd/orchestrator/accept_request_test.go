package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestAcceptRequestPrivateFileAndArguments(t *testing.T) {
	root := t.TempDir()
	valid := map[string]any{"task_id": "task", "control_file": filepath.Join(root, "missing-control"), "work_revision": 1, "event_id": "result", "event_revision": 1, "event_hash": strings.Repeat("a", 64), "action_slot": "slot", "decision": "accept", "command_id": "accept-result"}
	write := func(name string, value any) string {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := write("accept.json", valid)
	args := []string{"--state-dir", filepath.Join(root, "state"), "--request", path}
	// A valid private request reaches the unchanged capability-file loader.
	if err := reviewResult(context.Background(), args, io.Discard); errorCode(err) != "untrusted_private_file" {
		t.Fatalf("valid request did not reach capability loading: %v", err)
	}
	for _, flag := range []string{"task-id", "control-file", "work-revision", "event-id", "event-revision", "event-hash", "action-slot", "decision", "command-id"} {
		t.Run("mixed_"+flag, func(t *testing.T) {
			value := "accept"
			if flag == "work-revision" || flag == "event-revision" {
				value = "1"
			}
			err := reviewResult(context.Background(), append(append([]string{}, args...), "--"+flag, value), io.Discard)
			if errorCode(err) != "invalid_args" {
				t.Fatalf("mixed flags: %v", err)
			}
		})
	}
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown", func(m map[string]any) { m["unexpected"] = "secret" }},
		{"missing_decision", func(m map[string]any) { delete(m, "decision") }},
		{"missing_command", func(m map[string]any) { delete(m, "command_id") }},
		{"bad_revision", func(m map[string]any) { m["work_revision"] = 0 }},
		{"bad_decision", func(m map[string]any) { m["decision"] = "automatic" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range valid {
				m[k] = v
			}
			tc.change(m)
			p := write(tc.name+".json", m)
			err := reviewResult(context.Background(), []string{"--request", p}, io.Discard)
			if errorCode(err) != "invalid_review" {
				t.Fatalf("invalid request: %v", err)
			}
		})
	}
	t.Run("private_file", func(t *testing.T) {
		p := write("public.json", valid)
		if err := os.Chmod(p, 0644); err != nil {
			t.Fatal(err)
		}
		err := reviewResult(context.Background(), []string{"--request", p}, io.Discard)
		if errorCode(err) != "untrusted_private_file" {
			t.Fatalf("public request: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		p := filepath.Join(root, "link.json")
		if err := os.Symlink(path, p); err != nil {
			t.Fatal(err)
		}
		err := reviewResult(context.Background(), []string{"--request", p}, io.Discard)
		if errorCode(err) != "untrusted_private_file" {
			t.Fatalf("symlink request: %v", err)
		}
	})
	t.Run("relative", func(t *testing.T) {
		err := reviewResult(context.Background(), []string{"--request", "accept.json"}, io.Discard)
		if errorCode(err) != "path_not_absolute" {
			t.Fatalf("relative request: %v", err)
		}
	})
	t.Run("trailing_json", func(t *testing.T) {
		p := write("trailing.json", valid)
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(" {}")
		_ = f.Close()
		err = reviewResult(context.Background(), []string{"--request", p}, io.Discard)
		if errorCode(err) != "invalid_review" {
			t.Fatalf("trailing JSON: %v", err)
		}
	})
}

// This runs the real CLI, coordinator, SQLite and synthetic OS worker. It does
// not establish native Provider or autonomous-main acceptance.
func TestAcceptRequestCLIEquivalentReplayAndBindings(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "accept-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"controller_thread": "master", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", path), &owner); err != nil {
		t.Fatal(err)
	}
	delete(owner, "version")
	owner["run_id"] = "accept-request"
	owner["plan_revision"] = 1
	owner["delivery_mode"] = "collect"
	owner["tasks"] = []any{map[string]any{"id": "task", "max_attempts": 1, "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "printf result"}, "directory": root}}}
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", path), &submitted); err != nil {
		t.Fatal(err)
	}
	for {
		s := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile)
		if s.Status == "result_ready" {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("status=%s", s.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var collection struct {
		Events []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "task", "--control-file", submitted.ControlFile), &collection); err != nil || len(collection.Events) != 1 {
		t.Fatalf("collection=%+v err=%v", collection, err)
	}
	e := collection.Events[0]
	request := map[string]any{"task_id": "task", "control_file": submitted.ControlFile, "work_revision": 1, "event_id": e.EventID, "event_revision": e.EventRevision, "event_hash": e.PayloadHash, "action_slot": e.ActionSlot, "decision": "accept", "command_id": "accept-result"}
	// Every incorrect binding must fail while the result is still pending.
	for field, bad := range map[string]any{"event_id": e.EventID + "x", "event_revision": e.EventRevision + 1, "event_hash": strings.Repeat("0", 64), "action_slot": e.ActionSlot + "x", "work_revision": 2} {
		original := request[field]
		request[field] = bad
		write(request)
		var output bytes.Buffer
		err := reviewResult(ctx, []string{"--state-dir", state, "--request", path}, &output)
		if errorCode(err) != "conflict" {
			t.Fatalf("%s binding: %v output=%s", field, err, output.String())
		}
		request[field] = original
	}
	// A syntactically valid capability with the wrong owner token stays rejected.
	body, err := os.ReadFile(submitted.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	var cap controlCapability
	if err = json.Unmarshal(body, &cap); err != nil {
		t.Fatal(err)
	}
	cap.ControlToken = "wrong-owner"
	capBody, _ := json.Marshal(cap)
	wrongCap := filepath.Join(root, "wrong-control.json")
	if err = os.WriteFile(wrongCap, capBody, 0600); err != nil {
		t.Fatal(err)
	}
	request["control_file"] = wrongCap
	write(request)
	if err := reviewResult(ctx, []string{"--state-dir", state, "--request", path}, io.Discard); errorCode(err) != "owner_mismatch" {
		t.Fatalf("wrong owner: %v", err)
	}
	request["control_file"] = submitted.ControlFile
	write(request)
	if snapshot := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile); snapshot.Status != "result_ready" {
		t.Fatalf("invalid requests changed state: %+v", snapshot)
	}
	first := runBinary(t, ctx, bin, "accept", "--state-dir", state, "--request", path)
	replay := runBinary(t, ctx, bin, "accept", "--state-dir", state, "--request", path)
	// Legacy flags (including their default accept decision) replay the same decision.
	flags := runBinary(t, ctx, bin, "accept", "--state-dir", state, "--task-id", "task", "--control-file", submitted.ControlFile, "--work-revision", "1", "--event-id", e.EventID, "--event-revision", fmt.Sprint(e.EventRevision), "--event-hash", e.PayloadHash, "--action-slot", e.ActionSlot, "--command-id", "accept-result")
	if !bytes.Equal(first, replay) || !bytes.Equal(first, flags) {
		t.Fatalf("responses differ: first=%s replay=%s flags=%s", first, replay, flags)
	}
	if snapshot := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile); snapshot.Status != "completed" {
		t.Fatalf("accept did not complete task: %+v", snapshot)
	}
	request["decision"] = "reject"
	write(request)
	if err := reviewResult(ctx, []string{"--state-dir", state, "--request", path}, io.Discard); errorCode(err) != "conflict" {
		t.Fatalf("changed replay decision: %v", err)
	}
}
