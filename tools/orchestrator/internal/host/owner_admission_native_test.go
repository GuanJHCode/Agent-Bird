package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type nativeAdmissionReceipt struct {
	Stage     string          `json:"stage"`
	CommandID string          `json:"command_id,omitempty"`
	Terminal  bool            `json:"terminal,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Deadline  bool            `json:"deadline,omitempty"`
	Event     *contract.Event `json:"event,omitempty"`
}

// Only this test binary recognizes the fixture switch. Product arguments,
// configuration and environment gain no fault-injection capability.
func TestOwnerNativeAdmissionChild(t *testing.T) {
	mode := os.Getenv("ORCHESTRATOR_TEST_BLOCKED_ADMISSION")
	if mode != "1" && mode != "publisher" && mode != "durable" {
		return
	}
	control, err := ipc.TakeOwnerControlFD(4)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := contract.DecodeOwnerRuntimeBootstrap(raw)
	if err != nil {
		t.Fatal(err)
	}
	proof := os.NewFile(5, "admission-proof")
	defer proof.Close()
	encoder := json.NewEncoder(proof)
	err = runOriginalOwner(context.Background(), OriginalOwnerConfig{
		Bootstrap: bootstrap,
		Control:   control,
		InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
			if mode != "1" {
				executable, err := os.Executable()
				if err != nil {
					return nil, err
				}
				return launchInvocation{args: []string{executable, "-test.run=^TestOwnerNativeStubbornTreeChild$"}, dir: bootstrap.StateDir, env: map[string]string{"ORCHESTRATOR_TEST_OWNER_TREE": "leader", "ORCHESTRATOR_TEST_OWNER_TREE_DIR": bootstrap.StateDir}}, nil
			}
			_ = os.WriteFile(filepath.Join(bootstrap.StateDir, "unexpected-provider"), []byte("invoked"), 0600)
			return nil, errors.New("unexpected provider invocation")
		},
	}, func(e *ownerExecution) {
		if mode != "1" {
			if mode == "durable" {
				e.run.host.afterSpoolAppend = func(event contract.Event) {
					if event.Kind != contract.EventExited {
						return
					}
					if err := encoder.Encode(nativeAdmissionReceipt{Stage: "durable_exit_blocked", CommandID: event.CommandID, Event: &event}); err != nil {
						panic(err)
					}
					// The actual append (file and directory sync) has returned;
					// the caller cannot finish or notify the publisher. There
					// is no release/context path and no physical fsync claim.
					select {}
				}
			}
			rpc := e.cfg.rpc
			e.cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				reply, err := rpc(ctx, revision, kind, id, body)
				if event, ok := body.(contract.Event); ok && kind == "event" && event.Kind == contract.EventRunning && err == nil && reply.Error == "" {
					var ack contract.DurableAck
					if contract.DecodeOwnerRuntimeStrict(reply.Body, &ack) != nil || ack.Status != "durable" || ack.ProducerID != event.ProducerID || ack.EventID != event.EventID || ack.PayloadHash != event.PayloadHash || ack.AckedThrough < event.Sequence {
						return reply, errors.New("fixture did not receive matching durable Running ACK")
					}
					stage := "publisher_entered"
					if mode == "durable" {
						stage = "durable_running"
					}
					if err := encoder.Encode(nativeAdmissionReceipt{Stage: stage, CommandID: event.CommandID, Event: &event}); err != nil {
						return reply, err
					}
					// Actual publisher holds its serialization lock. The real
					// signed durable reply has returned; this private wrapper
					// never returns, even on cancellation. Not a network or
					// physical filesystem failure simulation.
					if mode == "publisher" {
						select {}
					}
				}
				return reply, err
			}
			return
		}
		e.acceptGrant = func(grant contract.LaunchCommand) (bool, error) {
			if err := encoder.Encode(nativeAdmissionReceipt{Stage: "admission_entered", CommandID: grant.CommandID}); err != nil {
				return false, err
			}
			// Model an uninterruptible admission operation. There is deliberately
			// no release path, context cancellation, provider or filesystem claim.
			select {}
		}
	})
	var terminal sourceTerminalDisposition
	_ = errors.As(err, &terminal)
	if encodeErr := encoder.Encode(nativeAdmissionReceipt{Stage: "owner_returned", Terminal: ownerCommandDisposition(err) == ownerCommandTerminal, Reason: string(terminal), Deadline: errors.Is(err, context.DeadlineExceeded)}); encodeErr != nil {
		t.Fatal(encodeErr)
	}
	// This expected nonzero process exit must retain the actual runtime error.
	// The parent checks the receipt and original Wait, not the test log's text.
	t.Fatal(err)
}

func TestOwnerNativeTerminalBoundsPermanentlyBlockedAdmission(t *testing.T) {
	testOwnerNativeTerminalBoundsPermanentFault(t, "1")
}

func TestOwnerNativeTerminalBoundsPermanentlyBlockedPublisher(t *testing.T) {
	testOwnerNativeTerminalBoundsPermanentFault(t, "publisher")
}

func TestOwnerNativeTerminalBoundsPermanentlyBlockedDurableReturn(t *testing.T) {
	testOwnerNativeTerminalBoundsPermanentFault(t, "durable")
}

func testOwnerNativeTerminalBoundsPermanentFault(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := os.MkdirTemp("/private/tmp", "owner-fault-"+mode+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("retained native admission evidence", state)
			return
		}
		if err := os.RemoveAll(state); err != nil {
			t.Error(err)
		}
	})
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx) }()
	t.Cleanup(func() {
		stopServer()
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-serverDone:
			if err != nil {
				t.Error("coordinator Serve", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("coordinator did not join")
		}
	})
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	birth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := db.SubmitPlan(ctx, store.PlanSpec{
		Run:   store.RunSpec{ID: "admission-run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth},
		Host:  store.HostLaunchSpec{ExecutionOwnerMode: "supervised-v1", OriginContextID: "origin", HostGeneration: "gen", Executable: executable},
		Tasks: []store.TaskSpec{{ID: "admission-task", RunID: "admission-run", MaxAttempts: 1, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	_ = transport.SetDeadline(time.Now().Add(5 * time.Second))
	hello := contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "gen", PID: os.Getpid(), Birth: birth, Executable: executable}
	body, _ := json.Marshal(hello)
	if err := ipc.Write(transport, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: hello.LaunchID, Payload: body}); err != nil {
		t.Fatal(err)
	}
	pending, err := ipc.Read(transport)
	if err != nil || pending.Kind != ipc.KindOwnerHostPending {
		t.Fatal("owner pending", pending.Kind, err)
	}
	token := strings.Repeat("b", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	body, _ = json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "admission-native", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(15 * time.Second).UnixMilli()})
	if err := ipc.Write(transport, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "admission-prepare", Epoch: pending.Epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(transport)
	var prepared contract.OwnerPrepared
	if err != nil || frame.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared) != nil {
		t.Fatal("owner prepare", frame.Kind, err)
	}
	bootstrap := contract.OwnerRuntimeBootstrap{Version: 1, StateDir: state, SpoolRoot: filepath.Join(state, "host-spool", hello.LaunchID), Prepared: prepared, BootstrapToken: token}
	control, childFD, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	proof, childProof, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proof.Close()
		_ = reader.Close()
		_ = writer.Close()
		_ = childFD.Close()
		_ = childProof.Close()
	})
	cmd := exec.Command(executable, "-test.run=^TestOwnerNativeAdmissionChild$", "-test.v")
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_TEST_BLOCKED_ADMISSION="+mode)
	cmd.ExtraFiles = []*os.File{reader, childFD, childProof}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = childFD.Close()
	_ = childProof.Close()
	ownerDone := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(ownerDone) }()
	t.Cleanup(func() {
		select {
		case <-ownerDone:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			<-ownerDone
			t.Error("forced cleanup of original owner; not acceptance evidence")
		}
		if t.Failed() {
			t.Log(output.String())
		}
	})
	ownerBirth, err := process.BirthContext(ctx, cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(writer).Encode(bootstrap); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
	boundFrame, err := ipc.Read(control)
	if err != nil {
		t.Fatal("owner bound", err)
	}
	bound, err := VerifyInitialOwnerBound(bootstrap, contract.OriginalOwnerTransport{PID: cmd.Process.Pid, Birth: ownerBirth}, boundFrame)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := hex.DecodeString(prepared.CoordinatorPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	forward := func(kind string) contract.OwnerCommand {
		t.Helper()
		_ = transport.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			frame, err := ipc.Read(transport)
			if err != nil {
				t.Fatal("read real signed", kind, err)
			}
			if frame.Kind == ipc.KindHostReady {
				continue
			}
			command, err := contract.DecodeOwnerCommand(frame.Payload)
			if err != nil || frame.Kind != ipc.KindOwnerCommand || command.Kind != kind || frame.Epoch != command.Permit.CoordinatorEpoch || frame.RequestID != command.RequestID || contract.VerifyOwnerCommand(command, ed25519.PublicKey(keyBytes), bound.Permit) != nil {
				t.Fatal("unexpected or unauthenticated coordinator command", kind, frame.Kind, err)
			}
			_ = control.SetWriteDeadline(time.Now().Add(time.Second))
			if err := ipc.Write(control, frame); err != nil {
				t.Fatal("forward original command", err)
			}
			return command
		}
	}
	launch := forward("launch")
	decoder := json.NewDecoder(proof)
	_ = proof.SetReadDeadline(time.Now().Add(5 * time.Second))
	var entered nativeAdmissionReceipt
	expectedStage := "admission_entered"
	if mode == "publisher" {
		expectedStage = "publisher_entered"
	}
	if mode == "durable" {
		expectedStage = "durable_running"
	}
	if err := decoder.Decode(&entered); err != nil || entered.Stage != expectedStage || entered.CommandID != launch.RequestID {
		t.Fatal("real owner did not reach permanent fault", entered, err)
	}
	before, err := db.TaskSnapshot(ctx, "admission-task")
	if err != nil || before.AttemptID == "" || before.SegmentID == "" || (mode == "1" && before.Process != nil) {
		t.Fatal("missing original reserved claim", before, err)
	}
	var tree nativeOwnerTree
	if mode != "1" {
		if entered.Event == nil || entered.Event.Process == nil || before.Process == nil || *before.Process != *entered.Event.Process {
			t.Fatal("publisher was not processing the real Running worker", entered, before)
		}
		nativeOwnerPoll(t, 5*time.Second, func() bool {
			body, err := os.ReadFile(filepath.Join(state, "worker-tree.json"))
			return err == nil && json.Unmarshal(body, &tree) == nil && tree.Leader == before.Process.PID && tree.Child > 0
		}, "stubborn worker tree readiness")
		tree.LeaderBirth, err = process.BirthContext(ctx, tree.Leader)
		if err != nil || tree.LeaderBirth != before.Process.Birth {
			t.Fatal("worker identity drift", tree, err)
		}
		tree.ChildBirth, err = process.BirthContext(ctx, tree.Child)
		if err != nil {
			t.Fatal(err)
		}
		ancestors, err := process.AncestryContext(ctx, tree.Child)
		childGroup, groupErr := syscall.Getpgid(tree.Child)
		if err != nil || ancestors[tree.Leader] != tree.LeaderBirth || ancestors[cmd.Process.Pid] != ownerBirth || groupErr != nil || childGroup != before.Process.PGID {
			t.Fatal("worker tree is not owned by original owner/group", ancestors, childGroup, err, groupErr)
		}
		if err := nativeOwnerWriteJSON(filepath.Join(state, "actor-identities.json"), struct {
			Owner contract.OriginalOwnerTransport `json:"owner"`
			Tree  nativeOwnerTree                 `json:"tree"`
		}{contract.OriginalOwnerTransport{PID: cmd.Process.Pid, Birth: ownerBirth}, tree}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, actor := range []struct {
				pid   int
				birth string
			}{{tree.Leader, tree.LeaderBirth}, {tree.Child, tree.ChildBirth}} {
				observed, err := process.BirthContext(context.Background(), actor.pid)
				if err == nil && observed == actor.birth {
					_ = syscall.Kill(actor.pid, syscall.SIGKILL)
					t.Error("forced cleanup of recorded worker; not acceptance evidence", actor.pid)
				}
			}
		})
	}
	// Serve cancellation emits the product-signed terminal before closing its
	// listener. Do not close Server or signal/release the owner during acceptance.
	stopServer()
	started := time.Now()
	terminal := forward("terminal")
	var disposition contract.OwnerTerminal
	if err := contract.DecodeOwnerRuntimeStrict(terminal.Body, &disposition); err != nil || disposition.Reason != "coordinator_exit" {
		t.Fatal("wrong terminal trigger", disposition, err)
	}
	if mode != "1" {
		nativeOwnerPoll(t, 2*time.Second, func() bool {
			leader, lerr := os.ReadFile(filepath.Join(state, "leader-term.json"))
			child, cerr := os.ReadFile(filepath.Join(state, "descendant-term.json"))
			var ltime, ctime int64
			return lerr == nil && cerr == nil && json.Unmarshal(leader, &ltime) == nil && json.Unmarshal(child, &ctime) == nil && ltime >= started.UnixNano() && ctime >= started.UnixNano()
		}, "product TERM reached both original worker and descendant")
		time.Sleep(time.Until(started.Add(8 * time.Second)))
		for _, actor := range []struct {
			pid   int
			birth string
		}{{tree.Leader, tree.LeaderBirth}, {tree.Child, tree.ChildBirth}} {
			observed, err := process.BirthContext(ctx, actor.pid)
			if err != nil || observed != actor.birth {
				t.Fatal("stubborn tree did not survive TERM grace", actor, err)
			}
		}
	}
	_ = proof.SetReadDeadline(started.Add(13 * time.Second))
	var durable nativeAdmissionReceipt
	if mode == "durable" {
		if err := decoder.Decode(&durable); err != nil || durable.Stage != "durable_exit_blocked" || durable.CommandID != launch.RequestID || durable.Event == nil || durable.Event.Kind != contract.EventExited || durable.Event.Process == nil || *durable.Event.Process != *before.Process {
			t.Fatal("did not reach actual post-Append Exited boundary", durable, err)
		}
	}
	var returned nativeAdmissionReceipt
	if err := decoder.Decode(&returned); err != nil || returned.Stage != "owner_returned" || !returned.Terminal || returned.Reason != "coordinator_exit" || !returned.Deadline {
		t.Fatal("bounded owner return lost terminal or incomplete admission", returned, err)
	}
	select {
	case <-ownerDone:
	case <-time.After(time.Until(started.Add(13 * time.Second))):
		t.Fatal("original owner Wait exceeded 13 seconds")
	}
	elapsed := time.Since(started)
	var exitErr *exec.ExitError
	if elapsed > 13*time.Second || !errors.As(waitErr, &exitErr) || exitErr.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exitErr.ExitCode() != 1 {
		t.Fatal("owner did not exit itself with incomplete cleanup", elapsed, waitErr)
	}
	if !errors.Is(syscall.Kill(cmd.Process.Pid, 0), syscall.ESRCH) {
		t.Fatal("original owner still exists after original Wait")
	}
	if mode == "1" {
		for _, path := range []string{filepath.Join(state, "unexpected-provider"), filepath.Join(bootstrap.SpoolRoot, ".grants")} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("blocked admission invented provider/durable admission", path, err)
			}
		}
	} else {
		for _, pid := range []int{tree.Leader, tree.Child} {
			if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				t.Fatal("original worker tree survived product cleanup", pid)
			}
		}
		spool, err := events.Open(filepath.Join(bootstrap.SpoolRoot, before.AttemptID, before.SegmentID))
		if err != nil {
			t.Fatal(err)
		}
		records, err := spool.Read()
		if err != nil {
			t.Fatal(err)
		}
		running, exited := 0, 0
		var originalExit contract.Event
		for _, event := range records {
			if event.Kind == contract.EventRunning {
				running++
			}
			if event.Kind == contract.EventExited {
				originalExit = event
				exited++
				if event.Process == nil || *event.Process != *before.Process || event.ExitCode == nil || *event.ExitCode != -int(syscall.SIGKILL) {
					t.Fatal("local Exited lost original Wait or identity", event)
				}
			}
		}
		if running != 1 || exited != 1 {
			t.Fatal("missing or duplicate local Running/Exited", records)
		}
		var committed int
		if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_events WHERE segment_id=? AND json_extract(body_json,'$.kind')=?`, before.SegmentID, contract.EventExited).Scan(&committed); err != nil {
			t.Fatal(err)
		}
		if mode == "publisher" && committed != 0 {
			t.Fatal("permanent publisher unexpectedly committed Exited", committed)
		}
		if mode == "durable" {
			var body string
			var persisted contract.Event
			if committed != 1 {
				t.Fatal("final flush did not commit unique original Exited", committed)
			}
			if err := db.SQL().QueryRowContext(ctx, `SELECT body_json FROM runtime_events WHERE event_id=?`, originalExit.EventID).Scan(&body); err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal([]byte(body), &persisted) != nil || !reflect.DeepEqual(persisted, originalExit) || !reflect.DeepEqual(*durable.Event, originalExit) {
				t.Fatal("durable-return/flush changed original Exited", originalExit, persisted, durable)
			}
			if acked, err := spool.AckedThrough(); err != nil || acked < originalExit.Sequence {
				t.Fatal("final flush lacks durable ACK", acked, err)
			}
		}
	}
	after, err := db.TaskSnapshot(ctx, "admission-task")
	if err != nil || after.AttemptID != before.AttemptID || after.SegmentID != before.SegmentID || (mode == "1" && after.Process != nil) || (mode != "1" && (after.Process == nil || *after.Process != *before.Process)) {
		t.Fatal("blocked claim changed execution identity", after, err)
	}
	t.Logf("fault=%s original owner pid=%d birth=%s Wait=exit1 non-signaled terminal=coordinator_exit incomplete=true elapsed=%s tree=%+v", mode, cmd.Process.Pid, ownerBirth, elapsed, tree)
}

type nativeOwnerTree struct {
	Leader      int    `json:"leader"`
	Child       int    `json:"child"`
	LeaderBirth string `json:"leader_birth,omitempty"`
	ChildBirth  string `json:"child_birth,omitempty"`
}

func nativeOwnerPoll(t *testing.T, duration time.Duration, predicate func() bool, reason string) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(reason)
}

func nativeOwnerWriteJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", body, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Same-group descendants remain alive until the product escalates TERM to KILL.
// There is no release file, test signal or normal exit inside the 13s window.
func TestOwnerNativeStubbornTreeChild(t *testing.T) {
	role := os.Getenv("ORCHESTRATOR_TEST_OWNER_TREE")
	if role != "leader" && role != "descendant" {
		return
	}
	state := os.Getenv("ORCHESTRATOR_TEST_OWNER_TREE_DIR")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	childDone := make(chan error, 1)
	if role == "leader" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(executable, "-test.run=^TestOwnerNativeStubbornTreeChild$")
		child.Env = append(os.Environ(), "ORCHESTRATOR_TEST_OWNER_TREE=descendant")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { childDone <- child.Wait() }()
		nativeOwnerPoll(t, 5*time.Second, func() bool {
			data, err := os.ReadFile(filepath.Join(state, "descendant-ready.json"))
			var pid int
			return err == nil && json.Unmarshal(data, &pid) == nil && pid == child.Process.Pid
		}, "descendant readiness")
		if err := nativeOwnerWriteJSON(filepath.Join(state, "worker-tree.json"), nativeOwnerTree{Leader: os.Getpid(), Child: child.Process.Pid}); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := nativeOwnerWriteJSON(filepath.Join(state, "descendant-ready.json"), os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}
	for {
		select {
		case <-signals:
			if err := nativeOwnerWriteJSON(filepath.Join(state, role+"-term.json"), time.Now().UnixNano()); err != nil {
				t.Fatal(err)
			}
		case err := <-childDone:
			t.Fatal("descendant exited before product group stop", err)
		case <-time.After(5 * time.Minute):
			t.Fatal("fixture exceeded maximum lifetime")
		}
	}
}
