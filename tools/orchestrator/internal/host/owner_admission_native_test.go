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
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type nativeAdmissionReceipt struct {
	Stage     string `json:"stage"`
	CommandID string `json:"command_id,omitempty"`
	Terminal  bool   `json:"terminal,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Deadline  bool   `json:"deadline,omitempty"`
}

// Only this test binary recognizes the fixture switch. Product arguments,
// configuration and environment gain no fault-injection capability.
func TestOwnerNativeAdmissionChild(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_TEST_BLOCKED_ADMISSION") != "1" {
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
			_ = os.WriteFile(filepath.Join(bootstrap.StateDir, "unexpected-provider"), []byte("invoked"), 0600)
			return nil, errors.New("unexpected provider invocation")
		},
	}, func(e *ownerExecution) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := os.MkdirTemp("/private/tmp", "owner-admission-")
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
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_TEST_BLOCKED_ADMISSION=1")
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
	if err := decoder.Decode(&entered); err != nil || entered.Stage != "admission_entered" || entered.CommandID != launch.RequestID {
		t.Fatal("real owner did not block at admission", entered, err)
	}
	before, err := db.TaskSnapshot(ctx, "admission-task")
	if err != nil || before.AttemptID == "" || before.SegmentID == "" || before.Process != nil {
		t.Fatal("missing original reserved claim", before, err)
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
	_ = proof.SetReadDeadline(started.Add(13 * time.Second))
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
	for _, path := range []string{filepath.Join(state, "unexpected-provider"), filepath.Join(bootstrap.SpoolRoot, ".grants")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("blocked admission invented provider/durable admission", path, err)
		}
	}
	after, err := db.TaskSnapshot(ctx, "admission-task")
	if err != nil || after.AttemptID != before.AttemptID || after.SegmentID != before.SegmentID || after.Process != nil {
		t.Fatal("blocked claim changed execution identity", after, err)
	}
	t.Logf("original owner pid=%d birth=%s Wait=exit1 non-signaled terminal=coordinator_exit incomplete_admission=true elapsed=%s", cmd.Process.Pid, ownerBirth, elapsed)
}
