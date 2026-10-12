package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func b07StartInstalledFixture(t *testing.T) *b05Fixture {
	return b07StartInstalledProviderFixture(t, "agy")
}
func b07StartInstalledProviderFixture(t *testing.T, provider string) *b05Fixture {
	targetType := "agy-hooks"
	entryName := "agent-bird-agy-hook"
	fixtureVersion := "agy-fixture-v1"
	protocol := "agy-stream-json-v1"
	noMessage := `{"injectSteps":[]}`
	completionPolicy := "owner_review"
	adapterProvider := adapter.ProviderAGY
	if provider == "claude" {
		adapterProvider = adapter.ProviderClaude
		targetType = "claude-settings"
		entryName = "agent-bird-hook"
		fixtureVersion = "b05-fixture-v1"
		protocol = "claude-stream-json-v1"
		noMessage = ""
	}
	installed := os.Getenv("B07_INSTALLED_ROOT")
	if installed == "" {
		t.Skip("B07 installed candidate not supplied")
	}
	overlay := ""
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "agy-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &b05Fixture{root: root, state: filepath.Join(root, "state"), handle: filepath.Join(root, "handle.json"), started: time.Now()}
	// Skill is consumed from the actual installed closure.
	f.bin = filepath.Join(installed, "bin", "codex-orchestrator")
	build := func(out string, args ...string) {
		t.Helper()
		argv := []string{"build"}
		if overlay != "" && out == f.bin {
			argv = append(argv, "-overlay", overlay)
		}
		argv = append(argv, "-o", out)
		argv = append(argv, args...)
		if raw, err := exec.Command("go", argv...).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s %v", out, raw, err)
		}
	}
	// Product binary is never rebuilt by this fixture.
	if overlay != "" {
		raw, err := os.ReadFile(f.bin)
		if err != nil {
			t.Fatal(err)
		}
		agyEvidence(t, "producer-partial-binary", raw, 0700)
	}
	native := filepath.Join(root, provider)
	build(native, "testdata/b07/"+provider+"_fixture.go")
	binaryHashes := map[string]string{f.bin: b07RecordBinary(t, f.bin, "before", provider), native: b07RecordBinary(t, native, "before", provider)}
	content, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	payload := adapter.InvocationPayload{Provider: string(adapterProvider), Directory: root, Prompt: "offline fixture task", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapterProvider, Protocol: protocol, Binary: adapter.BinaryPin{Path: native, Version: fixtureVersion, SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
	raw, _ := json.Marshal(payload)
	taskID := os.Getenv("B05_FIXTURE_TASK_ID")
	if taskID == "" {
		taskID = "b05-task"
	}
	plan := taskPlan{RunID: "b05-complete", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: taskID, MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 10000, AdapterPayload: raw, CompletionPolicy: completionPolicy}}}
	planPath := filepath.Join(root, "plan.json")
	testInvocation := adapter.Request{Provider: adapterProvider, Binary: payload.ProviderLock.Binary, Lock: payload.ProviderLock, Profile: payload.Profile, CWD: root, Prompt: payload.Prompt}
	if _, e := adapter.BuildInvocation(testInvocation); e != nil {
		t.Fatalf("fixture own typed payload invalid: %v", e)
	}
	planEvidence, _ := json.Marshal(plan)
	agyEvidence(t, provider+"-actual-task-plan.json", planEvidence, 0600)
	if err := writeExclusiveJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "fixture-hooks.json")
	initialConfig := []byte(`{"unrelated":{"PreInvocation":[{"type":"command","command":"preserved"}]}}`)
	if provider == "claude" {
		initialConfig = []byte(`{"unrelated":"preserved","hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"preserved"}]}]}}`)
	}
	if err = os.WriteFile(configPath, initialConfig, 0600); err != nil {
		t.Fatal(err)
	}
	b07ConfigCommand(t, f.bin, root, "config-plan", map[string]any{"target_type": targetType, "target": configPath, "version_root": installed, "plan_path": filepath.Join(root, "config-plan.json"), "action": "apply"})
	b07ConfigCommand(t, f.bin, root, "config-apply", map[string]any{"plan_path": filepath.Join(root, "config-plan.json")})
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	agyEvidence(t, "installed-config.json", configRaw, 0600)
	for _, name := range []string{"config-plan.json", "fixture-hooks.json.agent-bird-receipt.json", "fixture-hooks.json.agent-bird-backup"} {
		raw, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		agyEvidence(t, "apply-"+name, raw, 0600)
	}
	source := filepath.Join(installed, "scripts", entryName)
	binaryHashes[source] = b07RecordBinary(t, source, "installed-before", provider)
	f.cmd = exec.Command(f.bin, "controller", "start", "--provider", provider, "--state-dir", f.state, "--skill-path", filepath.Join(installed, "skills", "orchestrate", "SKILL.md"), "--bridge-plan", planPath, "--bridge-handle", f.handle)
	f.cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	for k, v := range map[string]string{"ORCHESTRATOR_ENABLE_TEST_FAKE": "1", "PLUGIN_ROOT": installed, "B07_CONFIG_PATH": configPath, "B05_HOOK_ENTRY": source, "AGENT_BIRD_BRIDGE_BINDING": "/stale/binding", "AGENT_BIRD_BRIDGE_HANDLE": "/stale/handle", "B05_WORKER_BINDING": store.BridgeCapabilityPath(f.state, plan.RunID), "B05_WORKER_HANDLE": f.handle} {
		f.cmd.Env = environmentWith(f.cmd.Env, k, v)
	}
	f.input, err = f.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := f.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	f.scanner = bufio.NewScanner(stdout)
	f.output = stdout.(*os.File)
	f.scanner.Buffer(make([]byte, 4096), 131072)
	f.cmd.Stderr = &f.diagnostics
	if err = f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"worker-hook.json", "worker-hook.json.trace", "worker-hook.json.trace.panic", "state/source-host.log"} {
			if body, err := os.ReadFile(filepath.Join(root, name)); err == nil {
				t.Logf("B05_WORKER_ARTIFACT %s %s", name, body)
			}
		}
		if t.Failed() {
			if db, err := store.Open(filepath.Join(f.state, "state.db")); err == nil {
				rows, err := db.SQL().Query(`SELECT body_json FROM runtime_events WHERE task_id='b05-task' ORDER BY sequence`)
				if err == nil {
					for rows.Next() {
						var body string
						_ = rows.Scan(&body)
						t.Logf("B05_FAILURE_EVENT %s", body)
					}
					rows.Close()
				}
				db.Close()
			}
		}
		if f.cmd.ProcessState == nil {
			_, _ = io.WriteString(f.input, "{\"op\":\"exit\"}\n")
			_ = f.input.Close()
			done := make(chan error, 1)
			go func() { done <- f.cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				// Let the real controller cancel and use its verified native identity.
				_ = f.cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					_ = f.cmd.Process.Kill()
					<-done
				}
			}
		}
		if _, err := os.Stat(filepath.Join(f.state, "coordinator.pid")); err == nil {
			if birth, err := process.Birth(f.coordinatorPID); err != nil || birth != f.coordinatorBirth || birth == "" {
				t.Errorf("coordinator identity unverified; retained private root=%s", root)
				return
			}
			stopCoordinator(t, f.state)
		}
		for path, expected := range binaryHashes {
			if got := b07RecordBinary(t, path, "after", provider); got != expected {
				t.Errorf("binary changed during actual fixture: %s expected=%s actual=%s", path, expected, got)
			}
		}
		nativeAbsent := true
		if f.nativePID != 0 {
			if birth, err := process.Birth(f.nativePID); err == nil {
				nativeAbsent = false
				t.Errorf("native fixture remains alive pid=%d birth=%s", f.nativePID, birth)
			}
		}
		t.Logf("B05_FIXTURE_SUBPROCESS_LOG %s", f.diagnostics.String())
		b07ConfigCommand(t, f.bin, root, "config-plan", map[string]any{"target_type": targetType, "target": configPath, "version_root": installed, "plan_path": filepath.Join(root, "config-remove.json"), "action": "remove"})
		b07ConfigCommand(t, f.bin, root, "config-remove", map[string]any{"plan_path": filepath.Join(root, "config-remove.json")})
		removedRaw, err := os.ReadFile(configPath)
		if err != nil {
			t.Error(err)
		} else {
			agyEvidence(t, "removed-config.json", removedRaw, 0600)
		}
		cleanupErr := os.RemoveAll(root)
		_, rootErr := os.Stat(root)
		t.Logf("B05_PROCESS_TERMINAL controller_pid=%d terminal=%v native_pid=%d native_absent=%v coordinator_pid=%d coordinator_birth=%q uid=%d private_root=%s mode=0700 start=%s end=%s cleanup=%v", f.cmd.Process.Pid, f.cmd.ProcessState != nil, f.nativePID, nativeAbsent, f.coordinatorPID, f.coordinatorBirth, os.Geteuid(), root, f.started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), cleanupErr == nil && os.IsNotExist(rootErr))
	})
	var ready map[string]any
	f.read(t, &ready)
	f.nativePID = int(ready["pid"].(float64))
	coordinatorRaw, err := os.ReadFile(filepath.Join(f.state, "coordinator.pid"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Sscanf(string(coordinatorRaw), "%d", &f.coordinatorPID)
	if f.coordinatorBirth, err = process.Birth(f.coordinatorPID); err != nil || f.coordinatorPID <= 0 {
		t.Fatalf("coordinator identity unavailable: %v", err)
	}
	if ready["binding"] != store.BridgeCapabilityPath(f.state, plan.RunID) || ready["handle"] != f.handle {
		t.Fatalf("pre-spawn routing env: %#v", ready)
	}
	// Only routing FILE references, no DB guessing, are needed by the owner.
	until := time.Now().Add(5 * time.Second)
	for {
		raw, err := readBridgeRequestFile(f.handle + ".bridge-owner.json")
		if err == nil {
			var metadata controllerBridgeMetadata
			if decode(raw, &metadata) == nil && metadata.BindingID != "" {
				f.ownerFile = metadata.OwnerCapabilityFile
				break
			}
		}
		if time.Now().After(until) {
			t.Fatalf("controller after-Start binding metadata absent diagnostics=%s", f.diagnostics.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ready["first_stdout"] != noMessage {
		t.Fatalf("first Hook must be no-op: %#v", ready)
	}
	return f
}
func b07ConfigCommand(t *testing.T, binary, root, operation string, request any) {
	t.Helper()
	label := operation
	if requestMap, ok := request.(map[string]any); ok && requestMap["action"] != nil {
		label += "-" + fmt.Sprint(requestMap["action"])
	}
	path := filepath.Join(root, label+"-request.json")
	body, _ := json.Marshal(request)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, operation, "--request", path)
	raw, err := cmd.CombinedOutput()
	agyEvidence(t, label+"-result.json", raw, 0600)
	if err != nil {
		t.Fatalf("installed %s: %v %s", operation, err, raw)
	}
}
func TestB07InstalledConfigHookNextInputOriginalOwnerACK(t *testing.T) {
	f := b07StartInstalledFixture(t)
	if out := agyHook(t, f, "fixture-owner"); out != `{"injectSteps":[]}` {
		t.Fatalf("observation auto-activated: %q", out)
	}
	base := f.base(t)
	// The controller's first grant, submit owner and binding owner must agree.
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var runOwner, bindingOwner, observedID, observedHash, session, status string
	var grants int
	if err = db.SQL().QueryRow(`SELECT r.origin_context_id,b.owner_context_id,b.candidate_observation_event_id,b.candidate_observation_hash,b.native_session_id,b.status FROM runs r JOIN bridge_bindings b ON b.run_id=r.id WHERE r.id='b05-complete'`).Scan(&runOwner, &bindingOwner, &observedID, &observedHash, &session, &status); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM owner_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if runOwner != bindingOwner || grants != 1 || status != "SESSION_OBSERVED" || session != "fixture-owner" || observedID == "" || observedHash == "" {
		t.Fatalf("first-grant/observation invariant owner=%s/%s grants=%d status=%s", runOwner, bindingOwner, grants, status)
	}
	f.owner(t, []string{"bridge", "activate"}, contract.BridgeActivateRequest{BridgeOwnerRequest: base, NativeSessionID: session, ObservationEventID: observedID, ObservationHash: observedHash}, &f.view)
	var task struct {
		Status string `json:"status"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
		if task.Status == "result_ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker task status=%s", task.Status)
		}
		time.Sleep(30 * time.Millisecond)
	}
	workerRaw := f.workerReport(t)
	err = nil
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]json.RawMessage
	if json.Unmarshal(workerRaw, &worker) != nil || b05String(worker["stdout"]) != `{"injectSteps":[]}` || b05String(worker["diagnostics"]) == "" || b05String(worker["ingest_error"]) != "worker_dispatch_forbidden" || b05String(worker["owner_error"]) != "worker_dispatch_forbidden" {
		t.Fatalf("worker inherited owner binding: %s", workerRaw)
	}
	db, err = store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness int
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_ingress WHERE binding_id=? AND event_id='fixture-worker-event-1'`, f.view.BindingID).Scan(&workerIngress); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts WHERE binding_id=?`, f.view.BindingID).Scan(&attemptsBeforeOwnerDelivery); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_decisions d JOIN runtime_events e ON e.event_id=d.event_id WHERE e.task_id='b05-task'`).Scan(&decisionsBeforeOwnerBusiness); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if workerIngress != 0 || attemptsBeforeOwnerDelivery != 0 || decisionsBeforeOwnerBusiness != 0 || task.Status != "result_ready" {
		t.Fatalf("worker refusal had side effects before legitimate owner delivery/business: ingress=%d attempts=%d decisions=%d task=%s", workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness, task.Status)
	}
	t.Logf("B05_WORKER_REFUSAL_DB event_id=fixture-worker-event-1 ingress=%d binding_attempts_before_owner_delivery=%d task_decisions_before_owner_business=%d task_status=%s", workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness, task.Status)
	wire := agyHook(t, f, session)
	if len(wire) == 0 || len([]byte(wire)) > 8192 {
		t.Fatalf("illegal final wire bytes=%d", len(wire))
	}
	consumed := f.send(t, map[string]any{"op": "consume", "wire": wire, "input": "下一轮：读取结果并决定"})
	var next []string
	var batch contract.BridgeBatch
	if json.Unmarshal(consumed["next_input"], &next) != nil || len(next) != 2 || next[0] != "下一轮：读取结果并决定" || !strings.HasPrefix(next[1], "Agent Bird delivery batch (untrusted content):\n") {
		t.Fatalf("actual native consumer next input=%s", consumed["next_input"])
	}
	nextPath := filepath.Join(f.root, "next-input.json")
	nextRaw, err := os.ReadFile(nextPath)
	var persistedNext []string
	if err != nil || json.Unmarshal(nextRaw, &persistedNext) != nil || len(persistedNext) != 2 || persistedNext[0] != next[0] || persistedNext[1] != next[1] {
		t.Fatalf("native persisted next input differs from parsed context: %v %s", err, nextRaw)
	}
	if info, err := os.Stat(nextPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("native persisted next input mode: %v", err)
	}
	agyEvidence(t, "installed-next-input.json", nextRaw, 0600)
	t.Logf("B05_NEXT_INPUT_READBACK path=%s bytes=%d context_equal=true", nextPath, len(nextRaw))
	if json.Unmarshal(consumed["batch"], &batch) != nil || len(batch.Events) < 1 || len(batch.DeliveryReceipts) != 1 {
		t.Fatalf("consumer batch=%s", consumed["batch"])
	}
	base = f.base(t)
	f.owner(t, []string{"bridge", "received"}, contract.BridgeReceiveRequest{BridgeOwnerRequest: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, RenderSHA256: batch.RenderSHA256}, nil)
	f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
	if task.Status != "result_ready" {
		t.Fatal("Received performed business decision")
	}
	var result contract.BridgeBatchEvent
	for _, e := range batch.Events {
		if e.Kind == "result" {
			result = e
			break
		}
	}
	if result.EventID == "" {
		t.Fatalf("missing result event %#v", batch.Events)
	}
	decision := map[string]any{"task_id": result.TaskID, "work_revision": result.WorkRevision, "event_id": result.EventID, "event_revision": result.EventRevision, "event_hash": result.EventHash, "action_slot": result.ActionSlot, "decision": "accept", "command_id": "b05-accept"}
	f.owner(t, []string{"task", "accept", "--handle", f.handle}, decision, nil)
	receipt := batch.DeliveryReceipts[0]
	decisions := []map[string]any{}
	for _, e := range batch.Events {
		if e.TaskID == receipt.TaskID {
			decisions = append(decisions, map[string]any{"event_id": e.EventID, "event_revision": e.EventRevision, "event_hash": e.EventHash, "action_slot": e.ActionSlot, "decision": "handled", "command_id": "b05-ack-" + e.EventID})
		}
	}
	f.owner(t, []string{"task", "ack", "--handle", f.handle}, map[string]any{"task_id": receipt.TaskID, "version": 1, "delivery_id": receipt.DeliveryID, "collection_proof_sha256": receipt.ProofSHA256, "decisions": decisions}, nil)
	f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
	if task.Status != "completed" {
		t.Fatalf("business+ACK did not finish: %s", task.Status)
	}
	if out := agyHook(t, f, session); out != `{"injectSteps":[]}` {
		t.Fatalf("empty final batch=%q", out)
	}
	f.sendExit(t)
	var handle taskHandle
	body, err := os.ReadFile(f.handle)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.Status != "queued" || handle.ControlFile == "" {
		t.Fatalf("persistent original handle lost: %s %v", body, err)
	}
	for _, path := range []string{f.handle, handle.ControlFile} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("persistent mode %s %v", path, err)
		}
	}
	t.Logf("REAL_FIXTURE_ONLY controller_pid=%d native_binding_pid=%d hook_entry=installed-config-loaded original_grants=%d wire_bytes=%d next_input_bytes=%d persistent_handle=%s", f.cmd.Process.Pid, f.view.NativePID, grants, len(wire), len(consumed["next_input"]), f.handle)
	for _, line := range f.log {
		t.Logf("B05_CONSUMER %s", line)
	}
}

func TestB07InstalledClaudeConfigNextInputOriginalOwnerACK(t *testing.T) {
	f := b07StartInstalledProviderFixture(t, "claude")
	if out := f.hook(t, "SessionStart", "fixture-owner"); out != "" {
		t.Fatalf("observation auto-activated: %q", out)
	}
	base := f.base(t)
	// The controller's first grant, submit owner and binding owner must agree.
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var runOwner, bindingOwner, observedID, observedHash, session, status string
	var grants int
	if err = db.SQL().QueryRow(`SELECT r.origin_context_id,b.owner_context_id,b.candidate_observation_event_id,b.candidate_observation_hash,b.native_session_id,b.status FROM runs r JOIN bridge_bindings b ON b.run_id=r.id WHERE r.id='b05-complete'`).Scan(&runOwner, &bindingOwner, &observedID, &observedHash, &session, &status); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM owner_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if runOwner != bindingOwner || grants != 1 || status != "SESSION_OBSERVED" || session != "fixture-owner" || observedID == "" || observedHash == "" {
		t.Fatalf("first-grant/observation invariant owner=%s/%s grants=%d status=%s", runOwner, bindingOwner, grants, status)
	}
	f.owner(t, []string{"bridge", "activate"}, contract.BridgeActivateRequest{BridgeOwnerRequest: base, NativeSessionID: session, ObservationEventID: observedID, ObservationHash: observedHash}, &f.view)
	var task struct {
		Status string `json:"status"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
		if task.Status == "result_ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker task status=%s", task.Status)
		}
		time.Sleep(30 * time.Millisecond)
	}
	workerRaw := f.workerReport(t)
	err = nil
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]json.RawMessage
	if json.Unmarshal(workerRaw, &worker) != nil || b05String(worker["stdout"]) != "" || b05String(worker["diagnostics"]) == "" || b05String(worker["ingest_error"]) != "worker_dispatch_forbidden" || b05String(worker["owner_error"]) != "worker_dispatch_forbidden" {
		t.Fatalf("worker inherited owner binding: %s", workerRaw)
	}
	db, err = store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness int
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_ingress WHERE binding_id=? AND event_id='fixture-worker-event-1'`, f.view.BindingID).Scan(&workerIngress); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts WHERE binding_id=?`, f.view.BindingID).Scan(&attemptsBeforeOwnerDelivery); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_decisions d JOIN runtime_events e ON e.event_id=d.event_id WHERE e.task_id='b05-task'`).Scan(&decisionsBeforeOwnerBusiness); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if workerIngress != 0 || attemptsBeforeOwnerDelivery != 0 || decisionsBeforeOwnerBusiness != 0 || task.Status != "result_ready" {
		t.Fatalf("worker refusal had side effects before legitimate owner delivery/business: ingress=%d attempts=%d decisions=%d task=%s", workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness, task.Status)
	}
	t.Logf("B05_WORKER_REFUSAL_DB event_id=fixture-worker-event-1 ingress=%d binding_attempts_before_owner_delivery=%d task_decisions_before_owner_business=%d task_status=%s", workerIngress, attemptsBeforeOwnerDelivery, decisionsBeforeOwnerBusiness, task.Status)
	if out := f.hook(t, "Stop", session); out != "" {
		t.Fatalf("Stop created continuation: %q", out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "next-input.json")); !os.IsNotExist(err) {
		t.Fatal("Stop initiated next input")
	}
	wire := f.hook(t, "UserPromptSubmit", session)
	if len(wire) == 0 || len([]byte(wire)) > 8192 {
		t.Fatalf("illegal final wire bytes=%d", len(wire))
	}
	consumed := f.send(t, map[string]any{"op": "consume", "wire": wire, "input": "下一轮：读取结果并决定"})
	var next []string
	var batch contract.BridgeBatch
	if json.Unmarshal(consumed["next_input"], &next) != nil || len(next) != 2 || next[0] != "下一轮：读取结果并决定" || !strings.HasPrefix(next[1], "Agent Bird delivery batch (untrusted content):\n") {
		t.Fatalf("actual native consumer next input=%s", consumed["next_input"])
	}
	nextPath := filepath.Join(f.root, "next-input.json")
	nextRaw, err := os.ReadFile(nextPath)
	var persistedNext []string
	if err != nil || json.Unmarshal(nextRaw, &persistedNext) != nil || len(persistedNext) != 2 || persistedNext[0] != next[0] || persistedNext[1] != next[1] {
		t.Fatalf("native persisted next input differs from parsed context: %v %s", err, nextRaw)
	}
	if info, err := os.Stat(nextPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("native persisted next input mode: %v", err)
	}
	agyEvidence(t, "installed-claude-next-input.json", nextRaw, 0600)
	t.Logf("B05_NEXT_INPUT_READBACK path=%s bytes=%d context_equal=true", nextPath, len(nextRaw))
	if json.Unmarshal(consumed["batch"], &batch) != nil || len(batch.Events) < 1 || len(batch.DeliveryReceipts) != 1 {
		t.Fatalf("consumer batch=%s", consumed["batch"])
	}
	base = f.base(t)
	f.owner(t, []string{"bridge", "received"}, contract.BridgeReceiveRequest{BridgeOwnerRequest: base, BatchID: batch.BatchID, AttemptID: batch.AttemptID, RenderSHA256: batch.RenderSHA256}, nil)
	f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
	if task.Status != "result_ready" {
		t.Fatal("Received performed business decision")
	}
	var result contract.BridgeBatchEvent
	for _, e := range batch.Events {
		if e.Kind == "result" {
			result = e
			break
		}
	}
	if result.EventID == "" {
		t.Fatalf("missing result event %#v", batch.Events)
	}
	decision := map[string]any{"task_id": result.TaskID, "work_revision": result.WorkRevision, "event_id": result.EventID, "event_revision": result.EventRevision, "event_hash": result.EventHash, "action_slot": result.ActionSlot, "decision": "accept", "command_id": "b05-accept"}
	f.owner(t, []string{"task", "accept", "--handle", f.handle}, decision, nil)
	receipt := batch.DeliveryReceipts[0]
	decisions := []map[string]any{}
	for _, e := range batch.Events {
		if e.TaskID == receipt.TaskID {
			decisions = append(decisions, map[string]any{"event_id": e.EventID, "event_revision": e.EventRevision, "event_hash": e.EventHash, "action_slot": e.ActionSlot, "decision": "handled", "command_id": "b05-ack-" + e.EventID})
		}
	}
	f.owner(t, []string{"task", "ack", "--handle", f.handle}, map[string]any{"task_id": receipt.TaskID, "version": 1, "delivery_id": receipt.DeliveryID, "collection_proof_sha256": receipt.ProofSHA256, "decisions": decisions}, nil)
	f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
	if task.Status != "completed" {
		t.Fatalf("business+ACK did not finish: %s", task.Status)
	}
	if out := f.hook(t, "SessionEnd", session); out != "" {
		t.Fatalf("SessionEnd continued %q", out)
	}
	if out := f.hook(t, "UserPromptSubmit", session); out != "" {
		t.Fatalf("acked no-message trigger emitted old batch: %q", out)
	}
	f.sendExit(t)
	var handle taskHandle
	body, err := os.ReadFile(f.handle)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.Status != "queued" || handle.ControlFile == "" {
		t.Fatalf("persistent original handle lost: %s %v", body, err)
	}
	for _, path := range []string{f.handle, handle.ControlFile} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("persistent mode %s %v", path, err)
		}
	}
	t.Logf("REAL_FIXTURE_ONLY controller_pid=%d native_binding_pid=%d hook_entry=source-closed original_grants=%d wire_bytes=%d next_input_bytes=%d persistent_handle=%s", f.cmd.Process.Pid, f.view.NativePID, grants, len(wire), len(consumed["next_input"]), f.handle)
	for _, line := range f.log {
		t.Logf("B05_CONSUMER %s", line)
	}
}

func b07RecordBinary(t *testing.T, path, phase, provider string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	t.Logf("B07_EXECUTABLE provider=%s phase=%s path=%s sha256=%x source_manifest_sha256=%s native_model=OFF", provider, phase, path, sum, os.Getenv("B07_SOURCE_MANIFEST_SHA256"))
	return hex.EncodeToString(sum[:])
}
