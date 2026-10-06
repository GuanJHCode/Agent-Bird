package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
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

//go:embed testdata/agy/agent-bird-agy-hook
var agyHookEntry []byte

func agyHook(t *testing.T, f *b05Fixture, session string) string {
	t.Helper()
	r := f.send(t, map[string]any{"op": "hook", "event": map[string]any{"conversationId": session, "invocationNum": 1, "initialNumSteps": 0, "prompt": "NATIVE-SECRET-PROMPT", "transcriptPath": "NATIVE-SECRET-PATH", "provider_id": "claude", "owner_epoch": 99, "binding_id": "UNTRUSTED", "native_hook": "Stop"}})
	if string(r["rc"]) != "0" {
		t.Fatalf("Hook failed %s", r["diagnostics"])
	}
	return b05String(r["stdout"])
}
func agyStartFixture(t *testing.T) *b05Fixture { return agyStartFixtureOverlay(t, "") }
func agyStartFixtureOverlay(t *testing.T, overlay string) *b05Fixture {
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "agy-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &b05Fixture{root: root, state: filepath.Join(root, "state"), handle: filepath.Join(root, "handle.json"), started: time.Now()}
	prepareControllerSkill(t, root)
	f.bin = filepath.Join(root, "bin", "codex-orchestrator")
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
	build(f.bin, ".")
	if overlay != "" {
		raw, err := os.ReadFile(f.bin)
		if err != nil {
			t.Fatal(err)
		}
		agyEvidence(t, "producer-partial-binary", raw, 0700)
	}
	native := filepath.Join(root, "agy")
	build(native, "testdata/agy/agy_fixture.go")
	binaryHashes := map[string]string{f.bin: agyRecordBinary(t, f.bin, "before"), native: agyRecordBinary(t, native, "before")}
	content, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	payload := adapter.InvocationPayload{Provider: string(adapter.ProviderAGY), Directory: root, Prompt: "offline fixture task", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: "agy-stream-json-v1", Binary: adapter.BinaryPin{Path: native, Version: "agy-fixture-v1", SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
	raw, _ := json.Marshal(payload)
	taskID := os.Getenv("B05_FIXTURE_TASK_ID")
	if taskID == "" {
		taskID = "b05-task"
	}
	plan := taskPlan{RunID: "b05-complete", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: taskID, MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 10000, AdapterPayload: raw, CompletionPolicy: "owner_review"}}}
	planPath := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	// The assets are resolved within the fixed source closure, never live E/data.
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "scripts", "agent-bird-agy-hook")
	if err := os.WriteFile(source, agyHookEntry, 0700); err != nil {
		t.Fatal(err)
	}
	binaryHashes[source] = agyRecordBinary(t, source, "before")
	f.cmd = exec.Command(f.bin, "controller", "start", "--provider", "agy", "--state-dir", f.state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--bridge-plan", planPath, "--bridge-handle", f.handle)
	f.cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	for k, v := range map[string]string{"ORCHESTRATOR_ENABLE_TEST_FAKE": "1", "PLUGIN_ROOT": root, "B05_HOOK_ENTRY": source, "AGENT_BIRD_BRIDGE_BINDING": "/stale/binding", "AGENT_BIRD_BRIDGE_HANDLE": "/stale/handle", "B05_WORKER_BINDING": store.BridgeCapabilityPath(f.state, plan.RunID), "B05_WORKER_HANDLE": f.handle} {
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
			if got := agyRecordBinary(t, path, "after"); got != expected {
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
	if ready["first_stdout"] != `{"injectSteps":[]}` {
		t.Fatalf("first Hook must be no-op: %#v", ready)
	}
	return f
}
func TestAGYControllerRealFixtureConsumesNextInputThenOwnerDecidesAndACKs(t *testing.T) {
	f := agyStartFixture(t)
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
	t.Logf("REAL_FIXTURE_ONLY controller_pid=%d native_binding_pid=%d hook_entry=source-closed original_grants=%d wire_bytes=%d next_input_bytes=%d persistent_handle=%s", f.cmd.Process.Pid, f.view.NativePID, grants, len(wire), len(consumed["next_input"]), f.handle)
	for _, line := range f.log {
		t.Logf("B05_CONSUMER %s", line)
	}
}

func agyRecordBinary(t *testing.T, path, phase string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	entry := sha256.Sum256(agyHookEntry)
	t.Logf("AGY_EXECUTABLE phase=%s path=%s sha256=%x source_manifest_sha256=%s adapter_manifest_sha256=%s hook_entry_sha256=%x native_model=OFF", phase, path, sum, os.Getenv("B06_AGY_SOURCE_MANIFEST_SHA256"), hookcli.AGYManifestSHA256(), entry)
	return hex.EncodeToString(sum[:])
}

func TestAGYCommittedSubmitAndHostFailuresPreserveOriginalReservation(t *testing.T) {
	for _, failure := range []string{"submit_uncertain", "source_host_start_failed", "source_host_start_timeout"} {
		t.Run(failure, func(t *testing.T) { agyCommittedFailure(t, failure) })
	}
}
func agyCommittedFailure(t *testing.T, failure string) {
	t.Helper()
	prefix := strings.ReplaceAll(failure, "_", "-")
	root, err := os.MkdirTemp("/tmp", "b05-uncertain-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
		_ = os.RemoveAll(root)
	})
	prepareControllerSkill(t, root)
	original, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	needle := "response, err := call(ctx, state, ipc.KindSubmit, request)"
	if bytes.Count(original, []byte(needle)) != 1 {
		t.Fatal("exact source boundary changed; no guessed overlay")
	}
	tracePath := filepath.Join(root, "submit-calls.jsonl")
	traceCode := fmt.Sprintf("b05Trace, b05TraceErr := os.OpenFile(%q, os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if b05TraceErr!=nil { return b05TraceErr }; if _, b05TraceErr=b05Trace.Write([]byte(\"submit_call\\n\")); b05TraceErr!=nil { b05Trace.Close();return b05TraceErr }; if b05TraceErr=b05Trace.Close();b05TraceErr!=nil{return b05TraceErr}\n\t", tracePath)
	replacement := bytes.Replace(original, []byte(needle), []byte(traceCode+needle), 1)
	switch failure {
	case "submit_uncertain":
		replacement = bytes.Replace(replacement, []byte(needle), []byte(needle+"\n\t// B05 FAULT_INJECTED_RESPONSE_DISCARD: real server call committed.\n\tif err == nil { err = ipc.ErrInvalidMessage }"), 1)
	case "source_host_start_failed", "source_host_start_timeout":
		boundary := "if err = startSourceHost(executable, state, bootstrap); err != nil {"
		if failure == "source_host_start_timeout" {
			boundary = "if err = waitForHost(ctx, state, receipt.LaunchID, receipt.LaunchToken); err != nil {"
		}
		// The original submission branch emits this receipt; an unrelated
		// recovery helper also calls startSourceHost and must remain unchanged.
		receiptPrefix := "\n\t\t_ = json.NewEncoder(stdout).Encode"
		boundary += receiptPrefix
		if bytes.Count(replacement, []byte(boundary)) != 1 {
			t.Fatal("exact original Host stage boundary changed")
		}
		injected := fmt.Sprintf("// B05 FAULT_INJECTED_HOST_STAGE_RETURN: not an actual OS failure.\n\tif err = codeError(%q); err != nil {", "b05_injected_"+failure) + receiptPrefix
		replacement = bytes.Replace(replacement, []byte(boundary), []byte(injected), 1)
	default:
		t.Fatal("unapproved fault mode")
	}
	replaced := filepath.Join(root, "main-response-discard.go")
	if err := os.WriteFile(replaced, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	originalPath, err := filepath.Abs("main.go")
	if err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{originalPath: replaced}})
	overlayPath := filepath.Join(root, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "codex-orchestrator")
	if raw, err := exec.Command("go", "build", "-overlay", overlayPath, "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("compiled response discard variant: %s %v", raw, err)
	}
	agyEvidence(t, prefix+"-overlay.json", overlay, 0600)
	agyEvidence(t, prefix+"-replacement.go", replacement, 0600)
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	agyEvidence(t, prefix+"-variant-binary", binary, 0700)
	variantExpected := agyRecordBinary(t, bin, "before")
	native := filepath.Join(root, "agy")
	if raw, err := exec.Command("go", "build", "-o", native, "testdata/agy/agy_fixture.go").CombinedOutput(); err != nil {
		t.Fatalf("native fixture build %s %v", raw, err)
	}
	body, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	payload := adapter.InvocationPayload{Provider: string(adapter.ProviderAGY), Directory: root, Prompt: "offline uncertainty control", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: "agy-stream-json-v1", Binary: adapter.BinaryPin{Path: native, Version: "agy-fixture-v1", SHA256: hex.EncodeToString(digest[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
	raw, _ := json.Marshal(payload)
	plan := taskPlan{RunID: "b05-uncertain", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "b05-uncertain-task", MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 10000, AdapterPayload: raw, CompletionPolicy: "owner_review"}}}
	planPath := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("nonfresh uncertainty test state")
	}
	handle := filepath.Join(root, "handle.json")
	marker := filepath.Join(root, "native-started")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "agy", "--state-dir", state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--bridge-plan", planPath, "--bridge-handle", handle)
	cmd.Env = environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH"))
	cmd.Env = environmentWith(cmd.Env, "ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	cmd.Env = environmentWith(cmd.Env, "B05_NATIVE_MARKER", marker)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatal("committed failure silently succeeded")
	}
	var recovery struct {
		Status   string   `json:"status"`
		Handle   string   `json:"handle"`
		NextArgv []string `json:"next_argv"`
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		var candidate = recovery
		if json.Unmarshal(line, &candidate) == nil && candidate.Status == "controller_bridge_recovery_required" {
			recovery = candidate
		}
	}

	agyEvidence(t, prefix+"-cli-output.jsonl", out, 0600)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native CLI spawned after uncertain submission")
	}
	calls, err := os.ReadFile(tracePath)
	if err != nil || string(calls) != "submit_call\n" {
		t.Fatalf("uncertain path called Submit more than once: %q %v", calls, err)
	}
	agyEvidence(t, prefix+"-calls.jsonl", calls, 0600)
	var h taskHandle
	raw, err = os.ReadFile(handle)
	if err != nil || json.Unmarshal(raw, &h) != nil || h.RunID != plan.RunID || h.Status != "submitting" || h.ControlFile == "" {
		t.Fatalf("original uncertainty reservation lost: %s %v", raw, err)
	}
	if _, err := readBridgeRequestFile(h.ControlFile); err != nil {
		t.Fatal("original onPrepared control lost after uncertainty")
	}
	reconciled, err := exec.CommandContext(ctx, bin, "task", "reconcile", "--handle", handle).CombinedOutput()
	if err != nil {
		t.Fatalf("original task reconciliation unavailable: %v %s", err, reconciled)
	}
	var summary store.RunSummary
	if json.Unmarshal(reconciled, &summary) != nil || summary.RunID != h.RunID {
		t.Fatalf("reconciliation changed original run: %s", reconciled)
	}
	var restored taskHandle
	restoredRaw, err := os.ReadFile(handle)
	if err != nil || json.Unmarshal(restoredRaw, &restored) != nil || restored.Status != "reconciled" || restored.ControlFile != h.ControlFile || restored.RunID != h.RunID {
		t.Fatalf("reconciliation replaced original reservation/control: %v %s", err, restoredRaw)
	}
	if calls, err := os.ReadFile(tracePath); err != nil || string(calls) != "submit_call\n" {
		t.Fatalf("reconciliation resubmitted: %q %v", calls, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("reconciliation spawned a native CLI")
	}
	agyEvidence(t, prefix+"-reconciliation.json", reconciled, 0600)
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var runs, grants, tasks, hosts int
	for query, dst := range map[string]*int{"SELECT count(*) FROM runs": &runs, "SELECT count(*) FROM owner_grants": &grants, "SELECT count(*) FROM tasks": &tasks, "SELECT count(*) FROM host_launches": &hosts} {
		if err := db.SQL().QueryRow(query).Scan(dst); err != nil {
			t.Fatalf("uncertain committed-ledger %s: %v", query, err)
		}
	}
	db.Close()
	if runs != 1 || grants != 1 || tasks != 1 || hosts != 1 {
		t.Fatalf("uncertain launch resubmitted/rebound or silently refunded: runs=%d grants=%d tasks=%d hosts=%d", runs, grants, tasks, hosts)
	}
	ledger, _ := json.Marshal(map[string]any{"state_was_absent_before": true, "submit_calls": 1, "runs": runs, "owner_grants": grants, "tasks": tasks, "host_launches": hosts, "native_spawn": false, "original_control_file": h.ControlFile, "original_handle_status": h.Status, "reconciled_handle_status": restored.Status})
	agyEvidence(t, prefix+"-ledger.json", ledger, 0600)
	if got := agyRecordBinary(t, bin, "after"); got != variantExpected {
		t.Fatalf("response discard binary changed expected=%s actual=%s", variantExpected, got)
	}
	variantSHA := sha256.Sum256(binary)
	replacementSHA := sha256.Sum256(replacement)
	t.Logf("FAULT_INJECTED_COMMITTED_RETURN mode=%s actual_controller_pid=%d terminal=%v committed_runs=%d owner_grants=%d host_launches=%d native_spawn=false original_handle_status=%s variant_sha256=%x replacement_sha256=%x", failure, cmd.Process.Pid, cmd.ProcessState.Exited(), runs, grants, hosts, h.Status, variantSHA, replacementSHA)
	if !bytes.Contains(out, []byte(fmt.Sprintf(`"error":%q`, failure))) {
		t.Errorf("original committed failure masked: expected=%s err=%v output=%s", failure, runErr, out)
	}
	if recovery.Handle != handle || strings.Join(recovery.NextArgv, "\x00") != strings.Join([]string{"task", "reconcile", "--handle", handle}, "\x00") {
		t.Errorf("original reconcile instruction absent: %s", out)
	}
}
func agyEvidence(t *testing.T, name string, body []byte, mode os.FileMode) {
	t.Helper()
	root := os.Getenv("AGY_EVIDENCE_DIR")
	if root == "" {
		return
	}
	if !filepath.IsAbs(root) || filepath.Base(name) != name {
		t.Fatal("invalid evidence destination")
	}
	if err := os.WriteFile(filepath.Join(root, name), body, mode); err != nil {
		t.Fatal(err)
	}
}

func agyActivateFixture(t *testing.T, f *b05Fixture) string {
	t.Helper()
	if out := agyHook(t, f, "fixture-owner"); out != `{"injectSteps":[]}` {
		t.Fatalf("observe before activate out=%q", out)
	}
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var session, event, hash string
	if err = db.SQL().QueryRow(`SELECT native_session_id,candidate_observation_event_id,candidate_observation_hash FROM bridge_bindings WHERE run_id='b05-complete'`).Scan(&session, &event, &hash); err != nil {
		t.Fatal(err)
	}
	db.Close()
	f.owner(t, []string{"bridge", "activate"}, contract.BridgeActivateRequest{BridgeOwnerRequest: f.base(t), NativeSessionID: session, ObservationEventID: event, ObservationHash: hash}, &f.view)
	var task struct {
		Status string `json:"status"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &task)
		if task.Status == "result_ready" {
			return session
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker unavailable: %s", task.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func TestAGYConsumerLostOrTruncatedWireNeverConfirmsOrRedelivers(t *testing.T) {
	for _, op := range []string{"hook_lost", "hook_partial"} {
		t.Run(op, func(t *testing.T) {
			f := agyStartFixture(t)
			session := agyActivateFixture(t, f)
			result := f.send(t, map[string]any{"op": op, "event": map[string]any{"conversationId": session, "invocationNum": 1, "initialNumSteps": 0}})
			if op == "hook_lost" {
				var count int
				_ = json.Unmarshal(result["discarded_bytes"], &count)
				if count == 0 || count > 8192 {
					t.Fatalf("actual lost Hook frame bytes=%d", count)
				}
			} else {
				if b05String(result["partial"]) == "" || string(result["parse_rejected"]) != "true" {
					t.Fatalf("native consumer accepted partial wrapper: %s", mustB05JSON(result))
				}
			}
			if _, err := os.Stat(filepath.Join(f.root, "next-input.json")); !os.IsNotExist(err) {
				t.Fatal("lost/partial Hook frame entered next input")
			}
			if out := agyHook(t, f, session); out != `{"injectSteps":[]}` {
				t.Fatalf("unconfirmed delivery automatically resent: %q", out)
			}
			var status contract.BridgeStatusView
			f.owner(t, []string{"bridge", "status"}, contract.BridgeStatusRequest{BridgeOwnerRequest: f.base(t)}, &status)
			// Full-write/OFFERED is unconfirmed until original Received/timeout; an
			// incomplete producer write becomes UNCERTAIN once the sender is dead.
			if status.Counts.WaitingReceipt+status.Counts.Uncertain != 1 || len(status.Batches) != 1 {
				t.Fatalf("lost/partial output invented consumption: %#v", status)
			}
			db, err := store.Open(filepath.Join(f.state, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			var attempts, decisions, received int
			if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts WHERE receipt_id!=''`).Scan(&received); err != nil {
				t.Fatal(err)
			}
			if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts`).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_decisions`).Scan(&decisions); err != nil {
				t.Fatal(err)
			}
			db.Close()
			if attempts != 1 || decisions != 0 || received != 0 {
				t.Fatalf("unconfirmed native consumption auto-ACK/redelivery attempts=%d decisions=%d received=%d", attempts, decisions, received)
			}
			t.Logf("NATIVE_FIXTURE_UNCONFIRMED %s status=%s", mustB05JSON(result), mustB05JSON(status))
		})
	}
}

func TestAGYProducerPartialWriteNeverAppendsNoMessageOrMarksOffered(t *testing.T) {
	root := privateTaskTemp(t)
	source, err := filepath.Abs("../../internal/hookcli/bounded_io.go")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte("n, err := unix.Write(b.fd, p)")
	if bytes.Count(original, needle) != 1 {
		t.Fatal("exact producer syscall boundary changed")
	}
	replacement := bytes.Replace(original, needle, []byte("// AGY CONTROL: actual delivery syscall writes only the first 3 bytes; no-message remains complete.\n\twritePayload := p; if len(p)>64 {writePayload=p[:3]}; n, err := unix.Write(b.fd, writePayload)"), 1)
	replaced := filepath.Join(root, "partial-write.go")
	if err := os.WriteFile(replaced, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{source: replaced}})
	overlayPath := filepath.Join(root, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	agyEvidence(t, "producer-partial-original.go", original, 0600)
	agyEvidence(t, "producer-partial-replacement.go", replacement, 0600)
	agyEvidence(t, "producer-partial-overlay.json", overlay, 0600)
	f := agyStartFixtureOverlay(t, overlayPath)
	session := agyActivateFixture(t, f)
	result := f.send(t, map[string]any{"op": "hook", "event": map[string]any{"conversationId": session, "invocationNum": 1, "initialNumSteps": 0}})
	out := b05String(result["stdout"])
	if out != `{"i` || b05String(result["diagnostics"]) != "bridge_output_incomplete\n" {
		t.Fatalf("producer partial changed/appended output result=%s", mustB05JSON(result))
	}
	assertAGYUnconfirmed(t, f, 1, 0)
	if _, err := os.Stat(filepath.Join(f.root, "next-input.json")); !os.IsNotExist(err) {
		t.Fatal("partial producer created next input")
	}
	var status contract.BridgeStatusView
	f.owner(t, []string{"bridge", "status"}, contract.BridgeStatusRequest{BridgeOwnerRequest: f.base(t)}, &status)
	if len(status.Batches) != 1 || (status.Batches[0].State != "EMITTING" && status.Batches[0].State != "UNCERTAIN") {
		t.Fatalf("partial lost unresolved state: %#v", status)
	}
	if out := agyHook(t, f, session); out != `{"injectSteps":[]}` {
		t.Fatalf("partial automatically repeated=%q", out)
	}
	assertAGYUnconfirmed(t, f, 1, 0)
	t.Logf("AGY_PRODUCER_PARTIAL actual_wire_bytes=%d no_appended_noop=true not_offered=true native_model=OFF", len(out))
}
func assertAGYUnconfirmed(t *testing.T, f *b05Fixture, attempts, offered int) {
	t.Helper()
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var a, o, r, d int
	for q, p := range map[string]*int{"SELECT count(*) FROM bridge_delivery_attempts": &a, "SELECT count(*) FROM bridge_delivery_attempts WHERE state='OFFERED'": &o, "SELECT count(*) FROM bridge_delivery_attempts WHERE receipt_id!=''": &r, "SELECT count(*) FROM runtime_decisions": &d} {
		if err := db.SQL().QueryRow(q).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	if a != attempts || o != offered || r != 0 || d != 0 {
		t.Fatalf("unconfirmed changed ledger attempts=%d offered=%d received=%d decisions=%d", a, o, r, d)
	}
}
func TestAGYOutputDeadlinePreservesUncertainWithoutOfferedOrRetry(t *testing.T) {
	f := agyStartFixture(t)
	session := agyActivateFixture(t, f)
	result := f.send(t, map[string]any{"op": "hook_timeout", "event": map[string]any{"conversationId": session, "invocationNum": 1, "initialNumSteps": 0}})
	var elapsed int64
	_ = json.Unmarshal(result["elapsed_ms"], &elapsed)
	diagnostic := b05String(result["diagnostics"])
	if (diagnostic != "bridge_output_incomplete\n" && diagnostic != "bridge_timeout\n") || elapsed < 1500 || elapsed > 2800 {
		t.Fatalf("deadline result=%s", mustB05JSON(result))
	}
	assertAGYUnconfirmed(t, f, 1, 0)
	if out := agyHook(t, f, session); out != `{"injectSteps":[]}` {
		t.Fatalf("timeout repeated=%q", out)
	}
	assertAGYUnconfirmed(t, f, 1, 0)
	var status contract.BridgeStatusView
	f.owner(t, []string{"bridge", "status"}, contract.BridgeStatusRequest{BridgeOwnerRequest: f.base(t)}, &status)
	if status.Counts.Uncertain != 1 || status.Counts.WaitingReceipt != 0 {
		t.Fatalf("timeout falsely offered=%#v", status)
	}
	t.Logf("AGY_OUTPUT_DEADLINE elapsed_ms=%d result=%s", elapsed, mustB05JSON(result))
}
func TestAGYConversationForkAndUnsupportedSelectorRejectBeforeDelivery(t *testing.T) {
	f := agyStartFixture(t)
	_ = agyActivateFixture(t, f)
	for _, session := range []string{"same-process-other-conversation", "fork-conversation"} {
		r := f.send(t, map[string]any{"op": "hook", "event": map[string]any{"conversationId": session, "invocationNum": 1, "initialNumSteps": 0}})
		if b05String(r["stdout"]) != `{"injectSteps":[]}` || b05String(r["diagnostics"]) != "bridge_session_mismatch\n" {
			t.Fatalf("session mismatch=%s", mustB05JSON(r))
		}
	}
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := db.SQL().QueryRow("SELECT count(*) FROM bridge_ingress").Scan(&before); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, selector := range []string{"Stop", "PreToolUse", "PostToolUse", "PostInvocation", "UNKNOWN"} {
		r := f.send(t, map[string]any{"op": "owner", "args": []string{"bridge", "hook", "--provider", "agy", "--native-hook", selector, "--binding-file", store.BridgeCapabilityPath(f.state, "b05-complete")}})
		if b05String(r["stdout"]) != "" || !strings.Contains(b05String(r["diagnostics"]), "bridge_event_unsupported") {
			t.Fatalf("selector guard=%s", mustB05JSON(r))
		}
	}
	db, err = store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SQL().QueryRow("SELECT count(*) FROM bridge_ingress").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unsupported selector ingested before=%d after=%d", before, after)
	}
	assertAGYUnconfirmed(t, f, 0, 0)
}
