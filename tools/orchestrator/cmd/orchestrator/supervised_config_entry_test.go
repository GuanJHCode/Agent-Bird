package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// TestSupervisedInstalledConfigPublicEntry exercises the public managed-owner
// entry point.  The only substituted program is a task-private provider shell;
// package identity, config publication, task submit, Store admission and the
// original-owner process chain are all production paths.
func TestSupervisedInstalledConfigPublicEntry(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "supervised-config-entry-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Log("retained supervised installed-config fixture:", root)
		}
	})

	source, dest, data := filepath.Join(root, "source"), filepath.Join(root, "installed"), filepath.Join(root, "data")
	if err = os.MkdirAll(filepath.Join(source, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(source, "bin", "codex-orchestrator")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	out, err := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, ".").CombinedOutput()
	cancelBuild()
	if err != nil {
		t.Fatalf("build private product binary: %v %s", err, out)
	}
	if err = os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err = supervisedConfigPackageSource(source); err != nil {
		t.Fatal(err)
	}
	if _, err = install.InstallPackage(install.InstallOptions{SourceRoot: source, BinaryPath: bin, DestinationRoot: dest, DataRoot: data, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dest, "versions", "1.0.0", "bin", "codex-orchestrator")
	state, handle := filepath.Join(root, "state"), filepath.Join(root, "handle.json")
	publish, plan := filepath.Join(root, "publish.json"), filepath.Join(root, "plan.json")
	if err = writeExclusiveJSON(publish, install.ConfigGenerationOperation{RequestID: "publish", Action: "publish", SavedDefaults: &install.ConfigSavedDefaults{AGY: "agy-model", Claude: "claude-model", Codex: "codex-model"}}); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(root, "claude")
	body := []byte("#!/bin/sh\nset -eu\ncase \"${1:-}\" in\n--version) echo fixture-v1; exit 0;;\n--help) echo '--output-format --input-format --permission-mode --disallowed-tools --json-schema --prompt-file --session-id --output --model --allowed-tools --denied-tools --no-subagents --disable-web-search --leader-socket --tools --deny'; exit 0;;\nesac\nif test ! -e \"$SUPERVISED_ONCE\"; then\n  : > \"$SUPERVISED_ONCE\"\n  \"$AGENT_BIRD_COMMAND\" provider snapshot publish --state-dir \"$SUPERVISED_STATE\" --request \"$SUPERVISED_PUBLISH\"\n  \"$AGENT_BIRD_COMMAND\" task submit --state-dir \"$SUPERVISED_STATE\" --request \"$SUPERVISED_PLAN\" --handle \"$SUPERVISED_HANDLE\"\n  exit 0\nfi\nprintf '{\\\"type\\\":\\\"result\\\",\\\"result\\\":\\\"controlled fixture result\\\"}\\n'\n")
	if err = os.WriteFile(provider, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	payload, _ := json.Marshal(adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: root, Prompt: "controlled local fixture", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: provider, Version: "fixture-v1", SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000}})
	if err = writeExclusiveJSON(plan, taskPlan{RunID: "supervised-config-run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "supervised-config-task", MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 5000, AdapterPayload: payload, CompletionPolicy: "owner_review"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, installed, "controller", "start", "--provider", "claude", "--state-dir", state)
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	for key, value := range map[string]string{"ORCHESTRATOR_ENABLE_TEST_FAKE": "1", "SUPERVISED_STATE": state, "SUPERVISED_PUBLISH": publish, "SUPERVISED_PLAN": plan, "SUPERVISED_HANDLE": handle, "SUPERVISED_ONCE": filepath.Join(root, "provider-once")} {
		cmd.Env = environmentWith(cmd.Env, key, value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("public controller/config/task entry: %v %s", err, output)
	}
	if ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("original controller Cmd.Wait did not report clean exit: ctx=%v state=%v", ctx.Err(), cmd.ProcessState)
	}
	var h taskHandle
	if err = readPrivateJSON(handle, &h); err != nil || h.Status != "queued" || h.ControlFile == "" {
		raw, _ := os.ReadFile(handle)
		info, statErr := os.Stat(handle)
		t.Fatalf("public task submit did not retain a queued original handle: %#v err=%v raw=%s stat=%v staterr=%v controller=%s", h, err, raw, info, statErr, output)
	}
	// The config generation is consumed by Store submission, not merely prepared.
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := taskStatus(t, ctx, installed, state, "supervised-config-task", h.ControlFile)
		if status.Status != "queued" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervised task never left queued")
		}
		time.Sleep(20 * time.Millisecond)
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var payloadText string
	err = db.SQL().QueryRow(`SELECT l.execution_owner_mode,r.adapter_payload FROM host_launches l JOIN task_runtime r ON r.host_launch_id=l.id WHERE r.task_id=?`, "supervised-config-task").Scan(&mode, &payloadText)
	_ = db.Close()
	var consumed adapter.InvocationPayload
	if err != nil || mode != "supervised-v1" || json.Unmarshal([]byte(payloadText), &consumed) != nil || consumed.ConfigGeneration == nil || consumed.ConfigModelSource != "saved-default" || consumed.Profile == nil || consumed.Profile.Model != "claude-model" {
		t.Fatalf("public submission did not consume its pinned installed generation through a supervised owner: mode=%q payload=%s err=%v", mode, payloadText, err)
	}
	stopCoordinator(t, state)
	clean = true
}

func supervisedConfigPackageSource(root string) error {
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		".codex-plugin/plugin.json": {`{"name":"codex-orchestrator","version":"1.0.0"}`, 0600},
		"defaults.json":             {`{"version":1,"write_workspace":"isolated-worktree","allow_shared_write":false,"provider_scope":"codex-session","model":"cli-default"}`, 0600},
		"scripts/invoke.sh":         {"synthetic entry", 0700}, "skills/orchestrate/SKILL.md": {"synthetic skill", 0600},
		"scripts/agent-bird-hook": {"synthetic hook", 0700}, "scripts/agent-bird-agy-hook": {"synthetic hook", 0700},
		"hooks/agy-proposed-hooks.json": {"synthetic proposed hooks", 0600}, "runtime/g0/runtime-manifest.json": {`{"version":2,"kind":"collect-only"}`, 0600},
	}
	metadata := make([]map[string]any, 0, len(files)+1)
	for path, f := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(f.body), f.mode); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(f.body))
		metadata = append(metadata, map[string]any{"path": path, "sha256": hex.EncodeToString(sum[:]), "mode": uint32(f.mode)})
	}
	bin := filepath.Join(root, "bin", "codex-orchestrator")
	body, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	metadata = append(metadata, map[string]any{"path": "bin/codex-orchestrator", "sha256": hex.EncodeToString(sum[:]), "mode": uint32(0700)})
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "kind": "collect-only-hooks", "files": metadata})
	return os.WriteFile(filepath.Join(root, "payload-manifest.json"), payload, 0600)
}
