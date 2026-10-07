//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/grokorigin"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type acpPublicBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *acpPublicBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *acpPublicBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type acpPublicFixture struct {
	root, state, handle, bin string
	cmd                      *exec.Cmd
	birth                    string
	stdout                   *os.File
	scanner                  *bufio.Scanner
	diagnostics              acpPublicBuffer
	lines                    []json.RawMessage
	coordinatorPID           int
	coordinatorBirth         string
	waited                   bool
	retain                   bool
	outputClosed             bool
}

func startACPPublicFixture(t *testing.T, mode string, faults ...string) *acpPublicFixture {
	t.Helper()
	if os.Getenv("ACP_PUBLIC_NATIVE") != "1" {
		t.Skip("explicit owned public ACP actors require process token")
	}
	root, rootErr := os.MkdirTemp("/private/tmp", "acp-public-")
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	f := &acpPublicFixture{root: root, state: filepath.Join(root, "state"), handle: filepath.Join(root, "handle.json"), bin: filepath.Join(root, "orchestrator")}
	t.Cleanup(func() {
		if !f.retain {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("owned fixture cleanup: %v", err)
			}
		}
	})
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "actor-mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	build := func(path, source string) {
		t.Helper()
		if out, err := exec.Command("go", "build", "-o", path, source).CombinedOutput(); err != nil {
			t.Fatalf("owned build: %s %v", out, err)
		}
	}
	if len(faults) == 0 {
		build(f.bin, ".")
	} else if len(faults) == 1 {
		buildACPFaultBinary(t, f.root, f.bin, faults[0])
	} else {
		t.Fatal("one owned fault variant required")
	}
	origin, worker := filepath.Join(root, "origin"), filepath.Join(root, "worker")
	build(origin, "testdata/acp-public/origin.go")
	build(worker, "testdata/acp-public/worker.go")
	digest := func(path string) string {
		t.Helper()
		sha, err := process.ExecutableDigest(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	payload := adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: work, Prompt: "owned offline task", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: "claude-stream-json-v1", Binary: adapter.BinaryPin{Path: worker, Version: "acp-public-worker-v1", SHA256: digest(worker)}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 10000}}
	raw, _ := json.Marshal(payload)
	plan := taskPlan{RunID: "acp-public-run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "acp-public-task", MaxAttempts: 1, WorkRevision: 1, MaxActiveMS: 10000, AdapterPayload: raw, CompletionPolicy: "owner_review"}}}
	planPath, requestPath := filepath.Join(root, "plan.json"), filepath.Join(root, "origin-request.json")
	if err := writeExclusiveJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	req := grokorigin.Request{SchemaVersion: 1, Binary: origin, BinarySHA256: digest(origin), BinaryVersion: "acp-public-origin-v1", Workspace: work, InitialPrompt: "owned initial prompt", OperationTimeoutMS: 3000, TotalTimeoutMS: 20000, OriginProfile: "readonly", AuthMode: "none"}
	if err := writeExclusiveJSON(requestPath, req); err != nil {
		t.Fatal(err)
	}
	f.cmd = exec.Command(f.bin, "controller", "start", "--provider", "grok", "--state-dir", f.state, "--bridge-plan", planPath, "--bridge-handle", f.handle, "--grok-session-write", "--grok-acp-request", requestPath)
	f.cmd.Env = environmentWith(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
	f.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := f.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	f.stdout = out.(*os.File)
	f.scanner = bufio.NewScanner(out)
	f.scanner.Buffer(make([]byte, 4096), 65536)
	f.cmd.Stderr = &f.diagnostics
	if err = f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.birth, err = process.Birth(f.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			if raw, err := os.ReadFile(filepath.Join(f.state, "coordinator.log")); err == nil {
				t.Logf("owned coordinator diagnostics: %s", raw)
			}
		}
		if !f.waited {
			f.stop(t)
		}
		// Failure paths must not delete a possibly live origin's private layout.
		if leaves, _ := filepath.Glob(filepath.Join(f.state, "grok-origin", "*")); len(leaves) != 0 {
			f.retain = true
			t.Errorf("origin cleanup unproved; retained owned state=%s", f.state)
		}
		if raw, err := os.ReadFile(filepath.Join(f.state, "coordinator.pid")); err == nil {
			var pid int
			_, _ = fmt.Sscanf(string(raw), "%d", &pid)
			if f.coordinatorPID == 0 {
				f.coordinatorPID = pid
				f.coordinatorBirth, _ = process.Birth(pid)
			}
			birth, birthErr := process.Birth(pid)
			path, pathErr := process.ExecutablePath(pid)
			if birthErr == nil && pathErr == nil && pid == f.coordinatorPID && birth == f.coordinatorBirth && path == f.bin {
				stopCoordinator(t, f.state)
			} else if birthErr == nil {
				f.retain = true
				t.Errorf("owned coordinator identity drift; retained state=%s", f.state)
			}
		}
	})
	return f
}

func (f *acpPublicFixture) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	if err := f.stdout.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !f.scanner.Scan() {
		t.Fatalf("public ACP output ended: %v diagnostics=%s", f.scanner.Err(), f.diagnostics.String())
	}
	raw := append([]byte(nil), f.scanner.Bytes()...)
	f.lines = append(f.lines, raw)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("public ACP output is not bounded status JSON: %s", raw)
	}
	if f.coordinatorPID == 0 {
		if body, err := os.ReadFile(filepath.Join(f.state, "coordinator.pid")); err == nil {
			_, _ = fmt.Sscanf(string(body), "%d", &f.coordinatorPID)
			f.coordinatorBirth, _ = process.Birth(f.coordinatorPID)
		}
	}
	return result
}

func (f *acpPublicFixture) stop(t *testing.T) {
	t.Helper()
	if f.waited {
		return
	}
	if birth, err := process.Birth(f.cmd.Process.Pid); err == nil && birth == f.birth {
		_ = f.cmd.Process.Signal(syscall.SIGTERM)
	}
	f.await(t, "bridge_cancelled", 10*time.Second)
}

func (f *acpPublicFixture) await(t *testing.T, want string, budget time.Duration) {
	t.Helper()
	if !f.outputClosed {
		_ = f.stdout.SetReadDeadline(time.Now().Add(budget))
		for f.scanner.Scan() {
			f.lines = append(f.lines, append([]byte(nil), f.scanner.Bytes()...))
		}
		if err := f.scanner.Err(); err != nil {
			t.Errorf("public controller stdout did not reach EOF: %v", err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.cmd.Wait() }()
	select {
	case err := <-done:
		f.waited = true
		if err == nil || f.cmd.ProcessState == nil || !strings.Contains(f.diagnostics.String(), want) {
			t.Errorf("expected controller terminal %s: %v diagnostics=%s", want, err, f.diagnostics.String())
		}
	case <-time.After(budget):
		t.Error("owned controller did not terminate within expected fault budget")
		if birth, err := process.Birth(f.cmd.Process.Pid); err == nil && birth == f.birth {
			_ = f.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			f.retain = true
			_ = f.cmd.Process.Kill()
			<-done
		}
		f.waited = true
	}
	if process.ConfirmGroupExited(f.cmd.Process.Pid) != nil {
		f.retain = true
		t.Error("owned controller group survived original Wait")
	}
}

func TestControllerACPClosedPublicOutputCleansOriginalOrigin(t *testing.T) {
	f := startACPPublicFixture(t, "")
	if err := f.stdout.Close(); err != nil {
		t.Fatal(err)
	}
	f.outputClosed = true
	f.await(t, "controller_acp_status_write_failed", 6*time.Second)
	raw, err := readBridgeRequestFile(f.handle + ".acp-origin.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta controllerACPMetadata
	if decode(raw, &meta) != nil || !meta.NativeExitKnown || !meta.NativeTreeExited || meta.NativeExitCode != 0 || meta.Status != "failed" {
		t.Fatalf("closed stdout lost original lifecycle: %s", raw)
	}
	if process.ConfirmGroupExited(meta.NativePID) != nil {
		t.Fatal("original native group survived closed stdout")
	}
	t.Logf("PUBLIC_CLOSED_OUTPUT native_pid=%d original_native_Wait=0 tree_exited=true", meta.NativePID)
}

func TestControllerACPRealPublicOriginDeliversWithoutBusinessACK(t *testing.T) {
	f := startACPPublicFixture(t, "")
	active := f.read(t)
	if b05String(active["status"]) != "controller_acp_active" {
		t.Fatalf("unexpected active status: %s", active["status"])
	}
	var nativePID int
	if err := json.Unmarshal(active["native_pid"], &nativePID); err != nil || nativePID < 1 {
		t.Fatal("original native PID absent")
	}
	for {
		received := f.read(t)
		if b05String(received["status"]) != "controller_acp_protocol_received" {
			t.Fatalf("unexpected public receipt status: %s", received["status"])
		}
		db, err := store.Open(filepath.Join(f.state, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = db.SQL().QueryRow(`SELECT COUNT(*) FROM bridge_delivery_attempts a JOIN bridge_batch_items i ON i.batch_id=a.batch_id JOIN runtime_events e ON e.event_id=i.event_id WHERE a.state='RECEIVED' AND json_extract(e.body_json,'$.kind')='result'`).Scan(&count)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
	}
	traceFiles, err := filepath.Glob(filepath.Join(f.state, "grok-origin", "*", "home", "acp-trace.jsonl"))
	if err != nil || len(traceFiles) != 1 {
		t.Fatal("one original actor trace absent")
	}
	trace, err := os.ReadFile(traceFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	methods := []string{}
	prompts := map[uint64]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(trace), []byte{'\n'}) {
		var r struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &r) != nil {
			t.Fatal("actor trace corrupt")
		}
		methods = append(methods, r.Method)
		if r.Method == "session/prompt" {
			if r.Params.SessionID != "owned-public-session" || len(r.Params.Prompt) != 1 {
				t.Fatal("actor changed original session")
			}
			prompts[r.ID] = r.Params.Prompt[0].Text
		}
	}
	if len(methods) < 4 || methods[0] != "initialize" || methods[1] != "session/new" || methods[2] != "session/prompt" || prompts[3] != "owned initial prompt" {
		t.Fatalf("original ordered startup absent: %v", methods)
	}
	for _, method := range methods[2:] {
		if method != "session/prompt" {
			t.Fatal("original session initialized more than once")
		}
	}
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var grants, decisions int
	var status, runOwner, bindingOwner, hookHash, hookFile, session string
	var boundPID int
	if err = db.SQL().QueryRow(`SELECT count(*) FROM owner_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT t.status,r.origin_context_id,b.owner_context_id,b.native_pid,b.native_session_id,b.hook_token_hash,b.capability_file FROM tasks t JOIN runs r ON r.id=t.run_id JOIN bridge_bindings b ON b.run_id=r.id WHERE t.id='acp-public-task'`).Scan(&status, &runOwner, &bindingOwner, &boundPID, &session, &hookHash, &hookFile); err != nil {
		t.Fatal(err)
	}
	if grants != 1 || decisions != 0 || status != "result_ready" || runOwner != bindingOwner || boundPID != nativePID || session != "owned-public-session" || hookHash != "" || hookFile != "" {
		t.Fatalf("owner/protocol/business separation failed grants=%d decisions=%d status=%s", grants, decisions, status)
	}
	files, err := filepath.Glob(filepath.Join(f.state, acpCompletionDirectory, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("durable local completion absent")
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		req, err := contract.DecodeACPReceiveRequest(raw)
		if err != nil {
			t.Fatal(err)
		}
		var state, responseHash, promptHash, stopReason, nativeSession string
		var rpc, ordinal int64
		if err = db.SQL().QueryRow(`SELECT state,acp_session_id,acp_rpc_request_id,acp_turn_ordinal,acp_response_sha256,wire_sha256,acp_stop_reason FROM bridge_delivery_attempts WHERE attempt_id=?`, req.AttemptID).Scan(&state, &nativeSession, &rpc, &ordinal, &responseHash, &promptHash, &stopReason); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(prompts[uint64(rpc)]))
		if state != "RECEIVED" || nativeSession != req.NativeSessionID || rpc != req.RPCRequestID || ordinal != req.TurnOrdinal || responseHash != req.ResponseSHA256 || promptHash != req.PromptSHA256 || hex.EncodeToString(sum[:]) != promptHash || stopReason != req.StopReason {
			t.Fatal("local/original-wire/Store receipt differ")
		}
		for _, line := range f.lines {
			if bytes.Contains(line, []byte(req.ClaimToken)) || bytes.Contains(line, []byte(prompts[uint64(rpc)])) {
				t.Fatal("private token or wire escaped public status")
			}
		}
	}
	f.stop(t)
	metaRaw, err := readBridgeRequestFile(f.handle + ".acp-origin.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta controllerACPMetadata
	if err = decode(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.NativeExitKnown || meta.NativeExitCode != 0 || !meta.NativeTreeExited || meta.NativePID != nativePID {
		t.Fatalf("original native Wait0/tree receipt missing: %+v", meta)
	}
	if birth, err := process.Birth(nativePID); err == nil {
		t.Fatalf("owned native PID survived: %s", birth)
	}
	t.Logf("PUBLIC_OWNED_ACP controller_pid=%d native_pid=%d original_native_Wait=0 tree_exited=true original_grants=%d prompts=%d completion_files=%d decisions=%d task_status=%s", f.cmd.Process.Pid, nativePID, grants, len(prompts), len(files), decisions, status)
}
