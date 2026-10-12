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
	"io"
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
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hookcli"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

//go:embed testdata/b05/agent-bird-hook
var b05HookEntry []byte

type b05Fixture struct {
	root, state, bin, handle string
	cmd                      *exec.Cmd
	input                    io.WriteCloser
	output                   *os.File
	scanner                  *bufio.Scanner
	diagnostics              bytes.Buffer
	ownerFile                string
	view                     contract.BridgeBindingView
	log                      []json.RawMessage
	nativePID                int
	coordinatorPID           int
	coordinatorBirth         string
	started                  time.Time
}

func b05StartFixture(t *testing.T) *b05Fixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "b05-cli-")
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
		argv := append([]string{"build", "-o", out}, args...)
		if raw, err := exec.Command("go", argv...).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s %v", out, raw, err)
		}
	}
	build(f.bin, ".")
	native := filepath.Join(root, "claude")
	build(native, "testdata/b05/claude_fixture.go")
	binaryHashes := map[string]string{f.bin: b05RecordBinary(t, f.bin, "before"), native: b05RecordBinary(t, native, "before")}
	content, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	payload := adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: root, Prompt: "offline fixture task", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: native, Version: "b05-fixture-v1", SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
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
	source := filepath.Join(root, "agent-bird-hook")
	if err := os.WriteFile(source, b05HookEntry, 0700); err != nil {
		t.Fatal(err)
	}
	f.cmd = exec.Command(f.bin, "controller", "start", "--provider", "claude", "--state-dir", f.state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--bridge-plan", planPath, "--bridge-handle", f.handle)
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
			if got := b05RecordBinary(t, path, "after"); got != expected {
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
	return f
}
func (f *b05Fixture) read(t *testing.T, dst any) {
	t.Helper()
	if err := f.output.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !f.scanner.Scan() {
		t.Fatalf("fixture stdout ended: %v diagnostics=%s", f.scanner.Err(), f.diagnostics.String())
	}
	raw := append([]byte(nil), f.scanner.Bytes()...)
	f.log = append(f.log, raw)
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("fixture JSON %s: %v", raw, err)
	}
}
func (f *b05Fixture) send(t *testing.T, cmd any) map[string]json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(cmd)
	if _, err := f.input.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	f.read(t, &result)
	return result
}
func b05String(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func (f *b05Fixture) owner(t *testing.T, args []string, request any, dst any) {
	t.Helper()
	result := f.send(t, map[string]any{"op": "owner", "args": args, "request": request})
	if string(result["rc"]) != "0" {
		t.Fatalf("owner %v failed: %s", args, b05String(result["diagnostics"]))
	}
	if dst != nil && json.Unmarshal([]byte(b05String(result["stdout"])), dst) != nil {
		t.Fatalf("owner %v invalid output %s", args, result["stdout"])
	}
}
func (f *b05Fixture) hook(t *testing.T, hook, session string) string {
	t.Helper()
	result := f.send(t, map[string]any{"op": "hook", "event": map[string]any{"hook_event_name": hook, "session_id": session, "prompt": "NATIVE-SECRET-PROMPT", "tool_input": map[string]string{"command": "NATIVE-SECRET-TOOL"}}})
	if string(result["rc"]) != "0" {
		t.Fatalf("Hook rc %s diagnostics %s", result["rc"], result["diagnostics"])
	}
	t.Logf("Hook %s diagnostics=%s stdout_bytes=%d", hook, b05String(result["diagnostics"]), len(b05String(result["stdout"])))
	return b05String(result["stdout"])
}
func (f *b05Fixture) base(t *testing.T) contract.BridgeOwnerRequest {
	t.Helper()
	raw, err := os.ReadFile(f.handle)
	if err != nil {
		t.Fatal(err)
	}
	var h taskHandle
	if json.Unmarshal(raw, &h) != nil {
		t.Fatal("invalid handle")
	}
	cap, err := store.ReadBridgeCapability(store.BridgeCapabilityPath(f.state, h.RunID))
	if err != nil {
		t.Fatal(err)
	}
	return contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, RequestID: fmt.Sprintf("b05-%d", time.Now().UnixNano())}, OwnerMode: "local", OwnerCapabilityFile: f.ownerFile, ControlFile: h.ControlFile}
}

func TestControllerBridgeRealFixtureConsumesNextInputThenOwnerDecidesAndACKs(t *testing.T) {
	f := b05StartFixture(t)
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
func (f *b05Fixture) sendExit(t *testing.T) {
	t.Helper()
	_, _ = io.WriteString(f.input, "{\"op\":\"exit\"}\n")
	_ = f.input.Close()
	if err := f.cmd.Wait(); err != nil {
		t.Fatalf("controller terminal %v diagnostics=%s", err, f.diagnostics.String())
	}
}

func TestControllerAfterStartRegistersVerifiedIdentityBeforeWaitAndReapsFailure(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	called := false
	err := runControllerCLI(context.Background(), cmd, func(pid int, birth string) error {
		called = true
		if cmd.ProcessState != nil || pid <= 0 || birth == "" {
			t.Fatal("registration ran after Wait or without actual identity")
		}
		return codeError("fixture_bind_refused")
	})
	if !called || err == nil || err.Error() != "fixture_bind_refused" || cmd.ProcessState == nil {
		t.Fatalf("after-Start registration failure not reaped: called=%v err=%v state=%v", called, err, cmd.ProcessState)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err == nil {
		t.Fatal("verified owned native child survived failed Bind")
	}
}
func TestControllerDefaultClearsInheritedBridgeEnvironment(t *testing.T) {
	env := controllerBridgeEnvironment([]string{"KEEP=yes", "AGENT_BIRD_BRIDGE_BINDING=/foreign", "AGENT_BIRD_BRIDGE_HANDLE=/foreign", "AGENT_BIRD_BRIDGE_RUN_ID=foreign", "AGENT_BIRD_BRIDGE_BOOTSTRAP=/foreign", "AGENT_BIRD_RUN_HANDLE=/foreign"})
	if len(env) != 1 || env[0] != "KEEP=yes" {
		t.Fatalf("unrelated binding inherited by default controller: %v", env)
	}
}
func TestControllerBridgeRejectsPlanAndHandleSymlinks(t *testing.T) {
	root := privateTaskTemp(t)
	target := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(target, taskPlan{RunID: "s", PlanRevision: 1}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "plan-link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readControllerBridgePlan(link, filepath.Join(root, "handle.json")); err == nil || err.Error() != "bridge_request_file_invalid" {
		t.Fatalf("symlink complete plan admitted: %v", err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readControllerBridgePlan(target, filepath.Join(root, "handle.json")); err == nil || err.Error() != "bridge_request_file_invalid" {
		t.Fatalf("public complete plan admitted: %v", err)
	}
}

func TestControllerBridgePreservesForeignHandleAndOwnerMetadata(t *testing.T) {
	for _, suffix := range []string{"", ".bridge-owner.json"} {
		t.Run(suffix, func(t *testing.T) {
			root := privateTaskTemp(t)
			planPath := filepath.Join(root, "plan.json")
			plan := taskPlan{RunID: "fresh", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "t", MaxAttempts: 1, MaxActiveMS: 1}}}
			if err := writeExclusiveJSON(planPath, plan); err != nil {
				t.Fatal(err)
			}
			handle := filepath.Join(root, "handle.json")
			foreign := []byte("foreign-private-file\n")
			if err := os.WriteFile(handle+suffix, foreign, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readControllerBridgePlan(planPath, handle); err == nil {
				t.Fatal("existing foreign reservation admitted")
			}
			got, err := os.ReadFile(handle + suffix)
			if err != nil || !bytes.Equal(got, foreign) {
				t.Fatalf("foreign reservation changed: %q %v", got, err)
			}
		})
	}
}

func TestControllerBridgeMetadataRejectsForeignReplacementAndLinks(t *testing.T) {
	root := privateTaskTemp(t)
	path := filepath.Join(root, "owner.json")
	expected := controllerBridgeMetadata{Version: 1, RunID: "original"}
	foreign := controllerBridgeMetadata{Version: 1, RunID: "foreign"}
	if err := writeControllerBridgeExclusive(path, foreign); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := replaceControllerBridgeMetadata(path, expected, expected); err == nil || err.Error() != "controller_bridge_metadata_conflict" {
		t.Fatalf("foreign metadata overwrite admitted: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("foreign metadata changed")
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			link := filepath.Join(root, kind+".json")
			var err error
			if kind == "symlink" {
				err = os.Symlink(path, link)
			} else {
				err = os.Link(path, link)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(link)
			if err := replaceControllerBridgeMetadata(link, foreign, expected); err == nil {
				t.Fatalf("%s metadata admitted", kind)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("metadata link changed original")
			}
		})
	}
}

func b05ActivateFixture(t *testing.T, f *b05Fixture) string {
	t.Helper()
	_ = f.hook(t, "SessionStart", "fixture-owner")
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
func TestControllerBridgeRegisteredHookTimeoutRemainsUncertainAndNeverRespawns(t *testing.T) {
	f := b05StartFixture(t)
	session := b05ActivateFixture(t, f)
	result := f.send(t, map[string]any{"op": "hook_timeout", "event": map[string]string{"hook_event_name": "UserPromptSubmit", "session_id": session}})
	diagnostic := b05String(result["diagnostics"])
	var elapsed int64
	_ = json.Unmarshal(result["elapsed_ms"], &elapsed)
	if diagnostic != "bridge_output_incomplete\n" && diagnostic != "bridge_timeout\n" {
		t.Fatalf("blocked registered native Hook diagnostics %q", diagnostic)
	}
	if elapsed < 1500 || elapsed > 2800 {
		t.Fatalf("shared Hook timeout not bounded: %dms", elapsed)
	}
	if out := f.hook(t, "UserPromptSubmit", session); out != "" {
		t.Fatalf("timeout-discarded output automatically reemitted %q", out)
	}
	var status contract.BridgeStatusView
	f.owner(t, []string{"bridge", "status"}, contract.BridgeStatusRequest{BridgeOwnerRequest: f.base(t)}, &status)
	if status.Counts.Uncertain != 1 || status.Counts.WaitingReceipt != 0 {
		t.Fatalf("timeout not uncertain: %#v", status)
	}
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var receipts, acks, attempts int
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts WHERE receipt_id!=''`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM bridge_delivery_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_decisions`).Scan(&acks); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || acks != 0 || attempts != 1 {
		t.Fatalf("timeout performed Received/ACK/automatic delivery: receipts=%d acks=%d attempts=%d", receipts, acks, attempts)
	}
	t.Logf("REAL_REGISTERED_HOOK_TIMEOUT %s", mustB05JSON(result))
}
func mustB05JSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func TestControllerBridgeConflictingSessionClearForkCannotClaim(t *testing.T) {
	for _, session := range []string{"same-process-other-session", "clear-session", "fork-session"} {
		t.Run(session, func(t *testing.T) {
			f := b05StartFixture(t)
			_ = b05ActivateFixture(t, f)
			result := f.send(t, map[string]any{"op": "hook", "event": map[string]string{"hook_event_name": "SessionStart", "session_id": session}})
			if b05String(result["stdout"]) != "" || b05String(result["diagnostics"]) != "bridge_session_mismatch\n" {
				t.Fatalf("unconfirmed same-process session claimed owner batch: %s", mustB05JSON(result))
			}
			var status contract.BridgeStatusView
			f.owner(t, []string{"bridge", "status"}, contract.BridgeStatusRequest{BridgeOwnerRequest: f.base(t)}, &status)
			if len(status.Batches) != 0 || status.Counts.WaitingReceipt != 0 || status.Counts.Uncertain != 0 {
				t.Fatalf("conflicting session created delivery: %#v", status)
			}
		})
	}
}

func TestControllerBridgePersistsOnlyOriginalOwnerReferences(t *testing.T) {
	f := b05StartFixture(t)
	path := f.handle + ".bridge-owner.json"
	raw, err := readBridgeRequestFile(path)
	if err != nil {
		t.Fatalf("persistent bridge owner FILE references absent/untrusted: %v", err)
	}
	var metadata struct {
		Version             int    `json:"version"`
		RunID               string `json:"run_id"`
		OwnerMode           string `json:"owner_mode"`
		OwnerCapabilityFile string `json:"owner_capability_file"`
		ControlFile         string `json:"control_file"`
		BindingFile         string `json:"binding_file"`
		BindingID           string `json:"binding_id"`
		OwnerEpoch          uint64 `json:"owner_epoch"`
		ManifestSHA256      string `json:"manifest_sha256"`
	}
	if err := decode(raw, &metadata); err != nil || metadata.Version != 1 || metadata.RunID != "b05-complete" || metadata.OwnerCapabilityFile != f.ownerFile || metadata.BindingID == "" || metadata.OwnerEpoch != 1 {
		t.Fatalf("metadata does not retain first original grant: %v", err)
	}
	var grant store.OwnerGrant
	ownerRaw, err := readBridgeRequestFile(metadata.OwnerCapabilityFile)
	if err != nil || json.Unmarshal(ownerRaw, &grant) != nil {
		t.Fatal("original owner FILE not private/persistent")
	}
	cap, err := loadControlCapability(metadata.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{grant.Token, cap.ControlToken, cap.HostLaunchToken} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("metadata leaked raw capability")
		}
	}
	if strings.Contains(metadata.OwnerCapabilityFile, "controller-") || strings.Contains(metadata.ControlFile, "controller-") {
		t.Fatal("persistent references point into auto-removed controller scope")
	}
	f.sendExit(t)
	for _, reference := range []string{path, metadata.OwnerCapabilityFile, metadata.ControlFile, f.handle} {
		if _, err := readBridgeRequestFile(reference); err != nil {
			t.Fatalf("reference unavailable after native/controller exit: %v", err)
		}
	}
}

func TestClaudeHookEntryUsesOfficialPluginRootAndSilentEarlyCapability(t *testing.T) {
	root := privateTaskTemp(t)
	bin := filepath.Join(root, "bin", "codex-orchestrator")
	if err := os.Mkdir(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	entry := filepath.Join(root, "agent-bird-hook")
	if err := os.WriteFile(entry, b05HookEntry, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(entry)
	cmd.Env = environmentWithout(os.Environ(), "PLUGIN_ROOT")
	cmd.Env = environmentWith(cmd.Env, "CLAUDE_PLUGIN_ROOT", root)
	cmd.Env = environmentWith(cmd.Env, bridgeBindingEnv, filepath.Join(root, "state", "bridge", "absent.json"))
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s"}`)
	var out, diagnostics bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || diagnostics.String() != "bridge_capability_invalid\n" {
		t.Fatalf("official plugin root/first-before-cap hook: stdout=%q diagnostics=%q", out.String(), diagnostics.String())
	}
}

// This controlled compiled variant discards one successful Submit response at
// the client IPC return boundary. It does not claim a native packet-loss trial.
func TestControllerBridgeSubmitUncertainKeepsOriginalReservationAndDoesNotSpawn(t *testing.T) {
	testControllerBridgeCommittedFailure(t, "submit_uncertain")
}

// These compiled variants preserve real Submit admission/commit. They inject
// only Host startup/wait return failures, not actual OS start/timeout failures.
func TestControllerBridgeHostUnavailablePreservesOriginalErrorAndRecovery(t *testing.T) {
	for _, failure := range []string{"source_host_start_failed", "source_host_start_timeout"} {
		t.Run(failure, func(t *testing.T) { testControllerBridgeCommittedFailure(t, failure) })
	}
}

func testControllerBridgeCommittedFailure(t *testing.T, failure string) {
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
		boundary := "if err = startHost(); err != nil {"
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
		injected := fmt.Sprintf("// B05 FAULT_INJECTED_HOST_STAGE_RETURN: not an actual OS failure.\n\tif err = startHost(); err == nil { err = codeError(%q) }\n\tif err != nil {", "b05_injected_"+failure) + receiptPrefix
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
	b05Evidence(t, prefix+"-overlay.json", overlay, 0600)
	b05Evidence(t, prefix+"-replacement.go", replacement, 0600)
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	b05Evidence(t, prefix+"-variant-binary", binary, 0700)
	variantExpected := b05RecordBinary(t, bin, "before")
	native := filepath.Join(root, "claude")
	if raw, err := exec.Command("go", "build", "-o", native, "testdata/b05/claude_fixture.go").CombinedOutput(); err != nil {
		t.Fatalf("native fixture build %s %v", raw, err)
	}
	body, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	payload := adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: root, Prompt: "offline uncertainty control", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: native, Version: "b05-fixture-v1", SHA256: hex.EncodeToString(digest[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
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
	cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "claude", "--state-dir", state, "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--bridge-plan", planPath, "--bridge-handle", handle)
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

	b05Evidence(t, prefix+"-cli-output.jsonl", out, 0600)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native CLI spawned after uncertain submission")
	}
	calls, err := os.ReadFile(tracePath)
	if err != nil || string(calls) != "submit_call\n" {
		t.Fatalf("uncertain path called Submit more than once: %q %v", calls, err)
	}
	b05Evidence(t, prefix+"-calls.jsonl", calls, 0600)
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
	b05Evidence(t, prefix+"-reconciliation.json", reconciled, 0600)
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
	b05Evidence(t, prefix+"-ledger.json", ledger, 0600)
	if got := b05RecordBinary(t, bin, "after"); got != variantExpected {
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
func b05Evidence(t *testing.T, name string, body []byte, mode os.FileMode) {
	t.Helper()
	root := os.Getenv("B05_EVIDENCE_DIR")
	if root == "" {
		return
	}
	if !filepath.IsAbs(root) || filepath.Base(name) != name {
		t.Fatal("invalid optional fixture evidence destination")
	}
	if err := os.WriteFile(filepath.Join(root, name), body, mode); err != nil {
		t.Fatal(err)
	}
}

func b05RecordBinary(t *testing.T, path, phase string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	entryDigest := sha256.Sum256(b05HookEntry)
	t.Logf("B05_EXECUTABLE phase=%s path=%s sha256=%x source_manifest_sha256=%s adapter_manifest_sha256=%s hook_entry_sha256=%x", phase, path, digest, os.Getenv("B05_SOURCE_MANIFEST_SHA256"), hookcli.ClaudeManifestSHA256(), entryDigest)
	return hex.EncodeToString(digest[:])
}

func TestControllerBridgeNativeLostOrPartialWrapperNeverConfirmsOrRedelivers(t *testing.T) {
	for _, op := range []string{"hook_lost", "hook_partial"} {
		t.Run(op, func(t *testing.T) {
			f := b05StartFixture(t)
			session := b05ActivateFixture(t, f)
			result := f.send(t, map[string]any{"op": op, "event": map[string]string{"hook_event_name": "UserPromptSubmit", "session_id": session}})
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
			if out := f.hook(t, "UserPromptSubmit", session); out != "" {
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

func (f *b05Fixture) workerReport(t *testing.T) []byte {
	t.Helper()
	var h taskHandle
	raw, err := os.ReadFile(f.handle)
	if err != nil || json.Unmarshal(raw, &h) != nil {
		t.Fatal("worker report original handle unavailable")
	}
	cap, err := loadControlCapability(h.ControlFile)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := loadHostBootstrap(cap)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		AttemptID string `json:"attempt_id"`
		SegmentID string `json:"segment_id"`
	}
	f.owner(t, []string{"task", "status", "--handle", f.handle}, nil, &status)
	report := filepath.Join(bootstrap.SpoolRoot, status.AttemptID, status.SegmentID, "scratch", "worker-hook.json")
	body, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("worker authorized scratch report missing: %v", err)
	}
	t.Logf("B05_WORKER_SCRATCH %s %s", report, body)
	return body
}

func TestControllerBridgeUnicodeEscapedPublicTaskSelectorsInActualWrapper(t *testing.T) {
	taskID := "任务\"\\b05"
	t.Setenv("B05_FIXTURE_TASK_ID", taskID)
	f := b05StartFixture(t)
	session := b05ActivateFixture(t, f)
	wire := f.hook(t, "UserPromptSubmit", session)
	if len([]byte(wire)) == 0 || len([]byte(wire)) > 8192 {
		t.Fatalf("actual Unicode wrapper final bytes=%d", len([]byte(wire)))
	}
	consumed := f.send(t, map[string]any{"op": "consume", "input": "中文下一轮", "wire": wire})
	var batch contract.BridgeBatch
	if json.Unmarshal(consumed["batch"], &batch) != nil || len(batch.Events) == 0 {
		t.Fatal("native consumer failed complete Unicode wrapper")
	}
	for _, event := range batch.Events {
		if event.TaskID != taskID {
			t.Fatalf("JSON quoting changed public task selector: %q", event.TaskID)
		}
	}
	t.Logf("ACTUAL_UNICODE_ESCAPING wire_bytes=%d task_selector_sha256=%x", len([]byte(wire)), sha256.Sum256([]byte(taskID)))
}
