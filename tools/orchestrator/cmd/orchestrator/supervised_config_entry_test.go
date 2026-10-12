package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// TestSupervisedInstalledConfigPublicEntry exercises the public managed-owner
// entry point.  The only substituted program is a task-private provider shell;
// package identity, config publication, task submit, Store admission and the
// original-owner process chain are all production paths.
func TestSupervisedInstalledConfigPublicEntry(t *testing.T) {
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
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
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(bin, image, 0700); err != nil {
		t.Fatal(err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	server := exec.Command(installed, "serve", "--state-dir", state)
	server.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1", "SUPERVISED_CONFIG_ENTRY_FIXTURE=1", "SUPERVISED_FIXTURE_STATE="+state)
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	t.Cleanup(func() {
		_ = server.Process.Signal(syscall.SIGTERM)
		select {
		case waitErr := <-serverDone:
			if waitErr != nil {
				t.Errorf("owned coordinator original Cmd.Wait: %v", waitErr)
			}
			t.Logf("owned Coordinator PID=%d original Wait=%v", server.Process.Pid, waitErr)
		case <-time.After(13 * time.Second):
			_ = server.Process.Kill()
			<-serverDone
			t.Error("owned coordinator required forced cleanup")
		}
	})
	for deadline := time.Now().Add(15 * time.Second); ; {
		readyCtx, stopReady := context.WithTimeout(ctx, 150*time.Millisecond)
		ready, readyErr := ipc.Call(readyCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "supervised-config-entry-ready"})
		stopReady()
		if readyErr == nil && ready.Kind == ipc.KindResponse {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned coordinator not ready: %v", readyErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = writeExclusiveJSON(publish, install.ConfigGenerationOperation{RequestID: "publish", Action: "publish", SavedDefaults: &install.ConfigSavedDefaults{AGY: "agy-model", Claude: "claude-model", Codex: "codex-model"}}); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(root, "claude")
	body := []byte(`#!/bin/sh
set -eu
umask 077
case "${1:-}" in
--version) echo fixture-v1; exit 0;;
--help) echo '--output-format --input-format --permission-mode --disallowed-tools --json-schema --prompt-file --session-id --output --model --allowed-tools --denied-tools --no-subagents --disable-web-search --leader-socket --tools --deny'; exit 0;;
esac
"$AGENT_BIRD_COMMAND" provider snapshot publish --state-dir "$SUPERVISED_STATE" --request "$SUPERVISED_PUBLISH"
"$AGENT_BIRD_COMMAND" task submit --state-dir "$SUPERVISED_STATE" --request "$SUPERVISED_PLAN" --handle "$SUPERVISED_HANDLE"
i=0
while test $i -lt 400; do
  status=$("$AGENT_BIRD_COMMAND" task status --handle "$SUPERVISED_HANDLE")
  if printf '%s' "$status" | grep -q '"status":"result_ready"'; then break; fi
  if printf '%s' "$status" | grep -q '"status":"failed"'; then printf '%s\n' "$status"; exit 71; fi
  i=$((i+1)); sleep 0.05
done
"$AGENT_BIRD_COMMAND" task collect --handle "$SUPERVISED_HANDLE" > "$SUPERVISED_COLLECTION.tmp"
mv "$SUPERVISED_COLLECTION.tmp" "$SUPERVISED_COLLECTION"
while test ! -f "$SUPERVISED_DECISION"; do sleep 0.05; done
"$AGENT_BIRD_COMMAND" task accept --handle "$SUPERVISED_HANDLE" --request "$SUPERVISED_DECISION"
while test ! -f "$SUPERVISED_RELEASE"; do sleep 0.05; done
`)
	if err = os.WriteFile(provider, body, 0700); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(root, "supervised-config-worker-fixture")
	if err = os.WriteFile(worker, image, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	payload, _ := json.Marshal(adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: root, Prompt: "controlled local fixture", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: worker, Version: "fixture-v1", SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000}})
	if err = writeExclusiveJSON(plan, taskPlan{RunID: "supervised-config-run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "supervised-config-task", MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 5000, AdapterPayload: payload, CompletionPolicy: "owner_review"}}}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, installed, "controller", "start", "--provider", "claude", "--state-dir", state)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 13 * time.Second
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	collectionPath, decisionPath, release := filepath.Join(root, "collection.json"), filepath.Join(root, "accept.json"), filepath.Join(root, "release")
	for key, value := range map[string]string{"ORCHESTRATOR_ENABLE_TEST_FAKE": "1", "SUPERVISED_CONFIG_ENTRY_FIXTURE": "1", "SUPERVISED_FIXTURE_STATE": state, "SUPERVISED_STATE": state, "SUPERVISED_PUBLISH": publish, "SUPERVISED_PLAN": plan, "SUPERVISED_HANDLE": handle, "SUPERVISED_COLLECTION": collectionPath, "SUPERVISED_DECISION": decisionPath, "SUPERVISED_RELEASE": release} {
		cmd.Env = environmentWith(cmd.Env, key, value)
	}
	controllerLog, err := os.OpenFile(filepath.Join(root, "controller.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer controllerLog.Close()
	cmd.Stdout, cmd.Stderr = controllerLog, controllerLog
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	controllerDone := make(chan error, 1)
	go func() { controllerDone <- cmd.Wait() }()
	controllerWaited := false
	t.Cleanup(func() {
		if !controllerWaited {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case waitErr := <-controllerDone:
				t.Logf("controller cleanup original Wait=%v", waitErr)
			case <-time.After(13 * time.Second):
				_ = cmd.Process.Kill()
				<-controllerDone
				t.Error("controller forced cleanup")
			}
		}
	})
	for {
		if _, e := os.Stat(collectionPath); e == nil {
			break
		}
		select {
		case e := <-controllerDone:
			controllerWaited = true
			raw, _ := os.ReadFile(filepath.Join(root, "controller.log"))
			t.Fatalf("controller early original Wait=%v: %s", e, raw)
		case <-ctx.Done():
			t.Fatal("public config result timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	output, _ := os.ReadFile(filepath.Join(root, "controller.log"))
	var h taskHandle
	if err = readPrivateJSON(handle, &h); err != nil || h.Status != "queued" || h.ControlFile == "" {
		raw, _ := os.ReadFile(handle)
		info, statErr := os.Stat(handle)
		t.Fatalf("public task submit did not retain a queued original handle: %#v err=%v raw=%s stat=%v staterr=%v controller=%s", h, err, raw, info, statErr, output)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var taskState string
	if err = db.QueryRow(`SELECT status FROM tasks WHERE id=?`, "supervised-config-task").Scan(&taskState); err != nil || taskState != "result_ready" {
		_ = db.Close()
		t.Fatal("configured worker result not durable", taskState, err)
	}
	var payloadText string
	err = db.QueryRow(`SELECT l.execution_owner_mode,r.adapter_payload FROM host_launches l JOIN task_runtime r ON r.host_launch_id=l.id WHERE r.task_id=?`, "supervised-config-task").Scan(&mode, &payloadText)
	_ = db.Close()
	var consumed adapter.InvocationPayload
	if err != nil || mode != "supervised-v1" || json.Unmarshal([]byte(payloadText), &consumed) != nil || consumed.ConfigGeneration == nil || consumed.ConfigModelSource != "saved-default" || consumed.Profile == nil || consumed.Profile.Model != "claude-model" {
		t.Fatalf("public submission did not consume its pinned installed generation through a supervised owner: mode=%q payload=%s err=%v", mode, payloadText, err)
	}
	raw, err := os.ReadFile(collectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var collected struct {
		Events []contract.Event `json:"events"`
	}
	if err = json.Unmarshal(raw, &collected); err != nil {
		t.Fatal(err)
	}
	var result contract.Event
	for _, event := range collected.Events {
		if event.Kind == contract.EventResult {
			result = event
		}
	}
	if result.EventID == "" || result.Artifact == nil {
		t.Fatalf("no actionable configured result: %s", raw)
	}
	artifact, err := os.ReadFile(result.Artifact.Path)
	artifactHash := sha256.Sum256(artifact)
	if err != nil || hex.EncodeToString(artifactHash[:]) != result.Artifact.SHA256 || !strings.Contains(string(artifact), "consumed --model claude-model") {
		t.Fatalf("worker did not consume configured model: %q %v", artifact, err)
	}
	// The durable Exited event is published only after the owned worker Wait.
	exits, err := sql.Open("sqlite", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var exitRaw string
	err = exits.QueryRow(`SELECT body_json FROM runtime_events WHERE task_id=? AND json_extract(body_json,'$.kind')='exited'`, "supervised-config-task").Scan(&exitRaw)
	_ = exits.Close()
	var exited contract.Event
	if err != nil || json.Unmarshal([]byte(exitRaw), &exited) != nil || exited.Process == nil || result.Process == nil || *exited.Process != *result.Process || exited.ExitCode == nil || *exited.ExitCode != 0 || !errors.Is(syscall.Kill(exited.Process.PID, 0), syscall.ESRCH) {
		t.Fatal("configured worker original Exited/Wait invalid", exitRaw, err)
	}
	t.Logf("configured worker original Exited PID=%d birth=%s exit=0", exited.Process.PID, exited.Process.Birth)
	if err = writeExclusiveJSON(decisionPath, map[string]any{"task_id": "supervised-config-task", "work_revision": 1, "event_id": result.EventID, "event_revision": result.EventRevision, "event_hash": result.PayloadHash, "action_slot": result.ActionSlot, "decision": "accept", "command_id": "supervised-config-accept"}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var bindingRaw string
	if err = sqlDB.QueryRow(`SELECT original_binding_json FROM execution_owners WHERE run_id='supervised-config-run' AND is_current=1 AND phase='bound'`).Scan(&bindingRaw); err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	binding, err := contract.DecodeOriginalOwnerBinding([]byte(bindingRaw))
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	_ = sqlDB.Close()
	ownerWait := publicSuccessionReadWait(t, state, "owner", binding.Owner.PID)
	hostWait := publicSuccessionReadWait(t, state, "first_host", binding.FirstHost.PID)
	if ownerWait.ExitCode != 0 || ownerWait.WaitError != "" || hostWait.ExitCode != 0 || hostWait.WaitError != "" {
		t.Fatal("configured owner/host original Wait", ownerWait, hostWait)
	}
	t.Logf("configured Owner original Wait=%+v; Host original Wait=%+v", ownerWait, hostWait)
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-controllerDone:
		controllerWaited = true
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("configured controller PID=%d original Wait=0", cmd.Process.Pid)
	case <-ctx.Done():
		t.Fatal("controller completion timeout")
	}

	clean = true
}

func runSupervisedConfigEntryFixture() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "source-host":
		err = publicSuccessionOwnedHostLauncher(os.Args[2:])
	case "public-succession-owned-host":
		err = publicSuccessionOwnedHost(os.Args[2:])
	default:
		err = run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil && os.Args[1] == "original-owner" {
		state := os.Getenv("SUPERVISED_FIXTURE_STATE")
		if state != "" {
			_ = os.WriteFile(filepath.Join(state, "supervised-original-owner-error.txt"), []byte(err.Error()+"\n"), 0600)
		}
	}
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "error", "error": errorCode(err)})
		os.Exit(2)
	}
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

func runSupervisedConfigWorkerFixture() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("fixture-v1")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--output-format --input-format --permission-mode --disallowed-tools --json-schema --prompt-file --session-id --output --model --allowed-tools --denied-tools --no-subagents --disable-web-search --leader-socket --tools --deny")
		return
	}
	var model string
	for i, a := range os.Args {
		if a == "--model" && i+1 < len(os.Args) {
			model = os.Args[i+1]
		}
	}
	if model != "claude-model" {
		fmt.Fprintln(os.Stderr, errors.New("configured model not consumed"))
		os.Exit(3)
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"supervised-config-worker"}`)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "subtype": "success", "session_id": "supervised-config-worker", "result": "native aggregate commentary", "structured_output": map[string]any{"version": 1, "kind": "result", "text": "consumed --model " + model}})
}
