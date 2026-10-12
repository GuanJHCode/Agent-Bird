package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestDiagnosticsCLIHelperProcess(t *testing.T) {
	mode := os.Getenv("DIAGNOSTICS_CLI_HELPER")
	if mode == "" {
		t.Skip("owned child helper")
	}
	if mode == "owner" {
		ready := os.NewFile(3, "ready")
		if _, err := ready.Write([]byte("R")); err != nil {
			os.Exit(11)
		}
		_ = ready.Close()
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(context.Background(), os.Args[i+1:], os.Stdout, os.Stderr); err != nil {
				_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": errorCode(err)})
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
	os.Exit(12)
}

func TestDiagnosticsPublicCLIReadAfterOriginalHostExitAndLostACK(t *testing.T) {
	testDiagnosticsPublicCLI(t, false)
}
func TestDiagnosticsPublicCLIDeleteAfterOwnerCompletion(t *testing.T) {
	testDiagnosticsPublicCLI(t, true)
}
func TestDiagnosticsPublicCLIRetentionSavedPlanAndReplay(t *testing.T) {
	testDiagnosticsPublicCLI(t, true, true)
}
func TestDiagnosticsPublicCLIGCPolicy(t *testing.T) { testDiagnosticsPublicCLI(t, true, false, true) }
func testDiagnosticsPublicCLI(t *testing.T, deleteCompleted bool, retention ...bool) {
	root, err := os.MkdirTemp("/private/tmp", "diag-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyR.Close()
	defer readyW.Close()
	owner := exec.CommandContext(ctx, exe, "-test.run=^TestDiagnosticsCLIHelperProcess$")
	owner.Env = append(os.Environ(), "DIAGNOSTICS_CLI_HELPER=owner")
	owner.ExtraFiles = []*os.File{readyW}
	stdin, err := owner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	}()
	_ = readyW.Close()
	deadline, _ := ctx.Deadline()
	_ = readyR.SetReadDeadline(deadline)
	var ready [1]byte
	if _, err = io.ReadFull(readyR, ready[:]); err != nil || ready[0] != 'R' {
		t.Fatal("original helper not ready", err)
	}
	birth, err := process.Birth(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plan := store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: owner.Process.Pid, OriginBirth: birth}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: exe}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`), Diagnostics: &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}}}}
	if deleteCompleted {
		plan.Tasks[0].CompletionPolicy = "exit_success_fixture"
	}
	accepted, err := db.SubmitPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: accepted.LaunchID, LaunchToken: accepted.LaunchToken, OriginContextID: "origin", OriginPID: owner.Process.Pid, OriginBirth: birth, HostGeneration: "generation", PID: owner.Process.Pid, Birth: birth, Executable: exe}
	if _, err = db.RegisterHost(ctx, hello, 1); err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, hello.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(state, "host-spool", g.HostID)
	logs := filepath.Join(spool, g.AttemptID, ".diagnostics")
	if err = os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := diagnostics.OpenWriter(ctx, logs, diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: g.Diagnostics.PolicySHA256})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Append(ctx, diagnostics.Record{SegmentID: "segment", Stream: diagnostics.Stderr, Data: []byte("original private output")}); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(root, "bootstrap.json")
	if err = writeExclusiveJSON(bootstrap, bootstrapMetadata(hostBootstrap{Version: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: spool, ProducerID: g.HostID, Hello: hello})); err != nil {
		t.Fatal(err)
	}
	control, err := writeControlCapability(state, "run", "controller", accepted.ControlToken, bootstrap, accepted.LaunchID, accepted.LaunchToken)
	if err != nil {
		t.Fatal(err)
	}
	if deleteCompleted {
		// Controlled Store events represent completion; the owned helper is only
		// a Host identity fixture and is not claimed as a real provider worker.
		e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: g.HostID + ":1", Sequence: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, Kind: contract.EventResult, PayloadHash: strings.Repeat("a", 64)}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		code := 0
		e.Sequence = 2
		e.EventID = g.HostID + ":2"
		e.Kind = contract.EventExited
		e.ExitCode = &code
		e.ActiveMS = 1
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Release the exact original child and retain its real Wait result before
	// any read. No disappeared-PID inference or execution restart is permitted.
	_ = stdin.Close()
	err = owner.Wait()
	waited = true
	if err != nil || owner.ProcessState.ExitCode() != 0 {
		t.Fatal("original Host fixture Wait failed", err)
	}
	if _, err = db.SQL().Exec(`UPDATE runtime_hosts SET status='offline'`); err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	defer func() {
		cancel()
		_ = server.Close()
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Error("original Coordinator did not join")
		}
	}()
	req := store.DiagnosticsReadRequest{Version: 1, TaskID: g.TaskID, AttemptID: g.AttemptID, RequestID: "read-1", ExpectedRevision: 1}
	requestFile := filepath.Join(root, "read.json")
	if err = writeExclusiveJSON(requestFile, req); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestDiagnosticsCLIHelperProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "DIAGNOSTICS_CLI_HELPER=cli")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("original CLI child Wait failed: %v %s", err, out)
		}
		return out
	}

	if len(retention) > 3 && retention[3] {
		testHostRetentionPublicRequests(t, ctx, root, state, control, g.RunID, accepted.ControlToken, db, server, invoke)
		return
	}
	if len(retention) > 4 && retention[4] {
		testCandidateRetentionPublicRequests(t, ctx, root, state, control, g.RunID, accepted.ControlToken, invoke)
		return
	}
	if len(retention) > 2 && retention[2] {
		testReportRetentionPublicRequests(t, ctx, root, state, control, g.RunID, accepted.ControlToken, db, server, invoke)
		return
	}
	if len(retention) > 1 && retention[1] {
		for _, step := range []struct {
			action   string
			expected int64
			enabled  bool
			revision int64
		}{{"status", 0, false, 0}, {"enable", 0, true, 1}, {"disable", 1, false, 2}, {"status", 0, false, 2}} {
			path := filepath.Join(root, "gc-"+step.action+"-"+fmt.Sprint(step.revision)+".json")
			if e := writeExclusiveJSON(path, store.DiagnosticsGCRequest{Version: 1, RunID: g.RunID, ExpectedRevision: step.expected}); e != nil {
				t.Fatal(e)
			}
			raw := invoke("diagnostics-gc-"+step.action, "--state-dir", state, "--control-file", control, "--request", path)
			var status store.DiagnosticsGCStatus
			if e := json.Unmarshal(raw, &status); e != nil || status.Enabled != step.enabled || status.Revision != step.revision || status.TTLDays != 7 {
				t.Fatal(string(raw), e)
			}
		}
		raw, _ := json.Marshal(coordinator.DiagnosticsGCRequest{ControllerThread: "foreign", ControlToken: accepted.ControlToken, Request: store.DiagnosticsGCRequest{Version: 1, RunID: g.RunID, ExpectedRevision: 2}})
		reply, e := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindDiagnosticsGCEnable, RequestID: "gc-foreign", Payload: raw})
		if e != nil || reply.Kind != ipc.KindError || !bytes.Contains(reply.Payload, []byte("owner_mismatch")) {
			t.Fatal(reply, e)
		}
		return
	}
	readArgs := []string{"diagnostics-read", "--state-dir", state, "--control-file", control, "--request", requestFile}
	first := invoke(readArgs...)
	var page store.DiagnosticsEnvelope
	if json.Unmarshal(first, &page) != nil || page.Body.Status != "available" || len(page.Body.Records) != 1 || string(page.Body.Records[0].Data) != "original private output" {
		t.Fatal("CLI did not expose authorized original data")
	}
	if again := invoke(readArgs...); !bytes.Equal(first, again) {
		t.Fatal("CLI pending replay changed output")
	}
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: control, TaskIDs: []string{"task"}, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if again := invoke("task", "diagnostics-read", "--handle", handle, "--request", requestFile); !bytes.Equal(first, again) {
		t.Fatal("task handle data route changed pending page")
	}
	for _, kind := range []string{"task", "run", "socket"} {
		bad := taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: control, TaskIDs: []string{"task"}, Status: "queued"}
		switch kind {
		case "task":
			bad.TaskIDs = []string{"foreign"}
		case "run":
			bad.RunID = "foreign"
		case "socket":
			bad.StateDir = filepath.Join(root, "foreign-state")
		}
		badPath := filepath.Join(root, "handle-"+kind+".json")
		if err = writeExclusiveJSON(badPath, bad); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err = run(ctx, []string{"task", "diagnostics-read", "--handle", badPath, "--request", requestFile}, &out, &out); err == nil || err.Error() != "task_handle_scope_mismatch" || out.Len() != 0 {
			t.Fatal("foreign handle admitted", kind, err)
		}
	}
	// Send the real ACK, wait for its durable state, then close without reading
	// the response. The next CLI must replay the latest receipt.
	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wire, _ := json.Marshal(coordinator.DiagnosticsAckRequest{ControllerThread: "controller", ControlToken: accepted.ControlToken, Receipt: page.Receipt})
	if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDiagnosticsAck, RequestID: "lost-ack", Payload: wire}); err != nil {
		t.Fatal(err)
	}
	for {
		var phase string
		if err = db.SQL().QueryRowContext(ctx, `SELECT phase FROM diagnostic_delivery_state`).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if phase == "idle" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("ACK never became durable")
		case <-time.After(5 * time.Millisecond):
		}
	}
	_ = conn.Close()
	ackFile := filepath.Join(root, "ack.json")
	if err = writeExclusiveJSON(ackFile, page.Receipt); err != nil {
		t.Fatal(err)
	}
	ackArgs := []string{"diagnostics-ack", "--state-dir", state, "--control-file", control, "--request", ackFile}
	ack := invoke(ackArgs...)
	if again := invoke("task", "diagnostics-ack", "--handle", handle, "--request", ackFile); !bytes.Equal(ack, again) {
		t.Fatal("lost ACK response replay drift")
	}
	var result store.DiagnosticsAckResult
	if json.Unmarshal(ack, &result) != nil || result.NextRevision != 2 || result.Status != "acknowledged" {
		t.Fatal("invalid public ACK")
	}
	expectedEvents := 0
	if deleteCompleted {
		expectedEvents = 2
	}
	var attempts, segments, events int
	if err = db.SQL().QueryRow(`SELECT (SELECT count(*) FROM attempts),(SELECT count(*) FROM segments),(SELECT count(*) FROM runtime_events)`).Scan(&attempts, &segments, &events); err != nil || attempts != 1 || segments != 1 || events != expectedEvents {
		t.Fatal("data access restarted or finalized execution", attempts, segments, events, err)
	}
	if deleteCompleted {
		if len(retention) > 0 && retention[0] {
			planRequest := store.DiagnosticsRetentionPlanRequest{Version: 1, RunID: g.RunID, RequestID: "retention-1", TerminalReceivedBefore: time.Now().UTC().Format(time.RFC3339Nano), Limit: 8}
			planFile := filepath.Join(root, "retention-request.json")
			if err = writeExclusiveJSON(planFile, planRequest); err != nil {
				t.Fatal(err)
			}
			stdout := invoke("diagnostics-retention-plan", "--state-dir", state, "--control-file", control, "--request", planFile)
			if len(stdout) > 8192 || stdout[len(stdout)-1] != '\n' {
				t.Fatal("plan stdout cannot be saved as apply input", len(stdout))
			}
			saved := filepath.Join(root, "retention-plan.json")
			if err = os.WriteFile(saved, stdout, 0600); err != nil {
				t.Fatal(err)
			}
			var retentionPlan store.DiagnosticsRetentionPlan
			if err = json.Unmarshal(stdout, &retentionPlan); err != nil || len(retentionPlan.Items) != 1 || retentionPlan.Items[0].ExpectedRevision != result.NextRevision {
				t.Fatalf("saved public plan invalid %s %v", stdout, err)
			}
			// Exercise the Coordinator's real outer exact-key wire scanner as well
			// as the CLI/store plan decoder. No malformed item may reach deletion.
			for _, shape := range []string{`null`, `{"diagnostics_no_attempt":null}`, `{"diagnostics_no_attempt":0}`, `{"unknown":1}`, `{"Diagnostics_no_attempt":1}`, `{"diagnostics_no_attempt":1,"diagnostics_no_attempt":1}`} {
				wire, _ := json.Marshal(coordinator.DiagnosticsRetentionApplyRequest{ControllerThread: "controller", ControlToken: accepted.ControlToken, Plan: retentionPlan})
				bad := strings.Replace(string(wire), `"excluded":{}`, `"excluded":`+shape, 1)
				reply, e := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindDiagnosticsRetentionApply, RequestID: "bad-plan", Payload: json.RawMessage(bad)})
				if e != nil || reply.Kind != ipc.KindError {
					t.Fatal("invalid outer plan accepted", shape, reply, e)
				}
				var n int
				if e = db.SQL().QueryRow(`SELECT count(*) FROM diagnostic_deletions`).Scan(&n); e != nil || n != 0 {
					t.Fatal("malformed plan crossed first deletion", shape, n, e)
				}
			}
			applyArgs := []string{"diagnostics-retention-apply", "--state-dir", state, "--control-file", control, "--request", saved}
			deleted := invoke(applyArgs...)
			if again := invoke(applyArgs...); !bytes.Equal(deleted, again) {
				t.Fatal("exact saved plan replay changed bytes")
			}
			var response store.DiagnosticsRetentionApplyResult
			if json.Unmarshal(deleted, &response) != nil || len(response.Items) != 1 || response.Items[0].Status != "deleted" || response.Items[0].Result == nil || response.Items[0].Result.ReleasedReservationBytes != 20*1024*1024+128*1024 {
				t.Fatalf("invalid public retention deletion %s", deleted)
			}
			t.Logf("retention_plan_stdout_bytes=%d saved_exact_stdout=true apply_and_replay=true plan_sha256=%s", len(stdout), retentionPlan.PlanSHA256)
		} else {
			deleteFile := filepath.Join(root, "delete.json")
			deletion := store.DiagnosticsDeleteRequest{Version: 1, TaskID: g.TaskID, AttemptID: g.AttemptID, RequestID: "delete-1", ExpectedRevision: result.NextRevision}
			if err = writeExclusiveJSON(deleteFile, deletion); err != nil {
				t.Fatal(err)
			}
			deleted := invoke("diagnostics-delete", "--state-dir", state, "--control-file", control, "--request", deleteFile)
			if again := invoke("task", "diagnostics-delete", "--handle", handle, "--request", deleteFile); !bytes.Equal(deleted, again) {
				t.Fatal("delete replay changed bytes")
			}
			var response store.DiagnosticsDeleteResult
			if json.Unmarshal(deleted, &response) != nil || response.Status != "deleted" || response.RemovedLogicalBytes <= 0 || response.ReleasedReservationBytes != 20*1024*1024+128*1024 {
				t.Fatalf("invalid public deletion %s", deleted)
			}
		}

		var out bytes.Buffer
		if err = run(ctx, readArgs, &out, &out); err == nil || err.Error() != "diagnostics_expired" || out.Len() != 0 {
			t.Fatal("deleted public read did not expire", err, out.String())
		}
		var phase, state string
		if err = db.SQL().QueryRow(`SELECT x.phase,r.state FROM diagnostic_deletions x JOIN managed_artifact_reservations r ON r.id=x.reservation_id`).Scan(&phase, &state); err != nil || phase != "removed" || state != "released" {
			t.Fatal("public deletion not durable", phase, state, err)
		}
	}
	t.Logf("original_host_pid=%d original_host_wait=0 cli_children_wait=0 lost_ack=durable_unread original_attempt=%s", owner.Process.Pid, g.AttemptID)
}

func TestDiagnosticsCLIRejectsUnsafeRequestBeforeControl(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "null", "empty", "oversize", "mode", "symlink", "hardlink", "relative"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "request.json")
			body := `{"version":1,"task_id":"task","attempt_id":"attempt","request_id":"read","expected_revision":1}`
			want := "diagnostics_request_file_invalid"
			switch kind {
			case "unknown":
				body = strings.TrimSuffix(body, "}") + `,"root":"/private/forged"}`
				want = "diagnostics_request_invalid"
			case "duplicate":
				body = strings.Replace(body, `"expected_revision":1`, `"expected_revision":1,"expected_revision":2`, 1)
				want = "diagnostics_request_invalid"
			case "null":
				body = strings.Replace(body, `"expected_revision":1`, `"expected_revision":null`, 1)
				want = "diagnostics_request_invalid"
			case "oversize":
				body = strings.Repeat("x", 8193)
			case "empty":
				body = ""
			}
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(root, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "hardlink":
				if err := os.Link(path, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "request.json"
			}
			var out bytes.Buffer
			err := run(context.Background(), []string{"diagnostics-read", "--state-dir", filepath.Join(root, "absent-state"), "--control-file", filepath.Join(root, "absent-control"), "--request", path}, &out, &out)
			if err == nil || err.Error() != want || out.Len() != 0 {
				t.Fatal("unsafe request reached control/runtime or leaked detail", err, out.String())
			}
			if _, err = os.Lstat(filepath.Join(root, "absent-state")); !os.IsNotExist(err) {
				t.Fatal("invalid request created runtime state", err)
			}
		})
	}
}
