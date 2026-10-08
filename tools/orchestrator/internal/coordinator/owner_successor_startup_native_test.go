package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The helper uses real socket backpressure only before calling the public Host
// runtime. No product env or hook can select this pause point.
func TestOriginalOwnerSuccessorStartupNativeChild(t *testing.T) {
	if os.Getenv("SUCCESSOR_STARTUP_NATIVE_CHILD") != "1" {
		t.Skip("child process helper")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	control, err := ipc.TakeOwnerControlFD(4)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	raw, err := ipc.ReadOwnerBootstrapPipe(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := contract.DecodeOwnerRuntimeBootstrap(raw)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := control.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	filled := 0
	var fillErr error
	err = fd.Control(func(descriptor uintptr) {
		if fillErr = syscall.SetsockoptInt(int(descriptor), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 4096); fillErr != nil {
			return
		}
		// A one-byte EAGAIN proves no smaller Bound-frame write can proceed.
		for filled < 1<<20 {
			n, e := syscall.Write(int(descriptor), []byte{0x55})
			if errors.Is(e, syscall.EAGAIN) || errors.Is(e, syscall.EWOULDBLOCK) {
				return
			}
			if e != nil || n != 1 {
				fillErr = fmt.Errorf("FD4 fill n=%d err=%v", n, e)
				return
			}
			filled += n
		}
		fillErr = errors.New("FD4 did not reach backpressure")
	})
	if err != nil || fillErr != nil || filled == 0 {
		t.Fatal("FD4 backpressure", err, fillErr, filled)
	}
	marker := os.Getenv("SUCCESSOR_STARTUP_NATIVE_MARKER")
	if err := os.WriteFile(marker, []byte(strconv.Itoa(filled)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err = host.RunOriginalOwner(ctx, host.OriginalOwnerConfig{Bootstrap: bootstrap, Control: control,
		InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
			_ = os.WriteFile(marker+".invocation", []byte("unexpected invocation\n"), 0600)
			return nil, errors.New("unexpected startup invocation")
		}, EnsureCoordinator: func(context.Context) error {
			f, e := os.OpenFile(marker+".ensure", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = f.WriteString("Ensure\n")
			return errors.Join(e, f.Close())
		}})
	if err != nil {
		t.Fatal("real successor runtime", err)
	}
}

func TestOriginalOwnerSuccessorStartupNativeEpochAdvance(t *testing.T) {
	exerciseOriginalOwnerSuccessorStartupNative(t, false)
}

func TestOriginalOwnerSuccessorStartupNativeRejectsMissingSession(t *testing.T) {
	exerciseOriginalOwnerSuccessorStartupNative(t, true)
}

func exerciseOriginalOwnerSuccessorStartupNative(t *testing.T, rejectSession bool) {
	t.Helper()
	s, hello := successorTransportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, pending := successorTransportConnect(t, s, hello)
	if pending.Kind != ipc.KindOwnerHostPending {
		t.Fatal("initial pending", pending.Kind)
	}
	prepare := successorPrepareFrame(s)
	if err := ipc.Write(first, prepare); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(first)
	var prepared contract.OwnerPrepared
	if err != nil || frame.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared) != nil || prepared.Intent.Scope.Incarnation != 2 {
		t.Fatal("successor Prepare", frame.Kind, err)
	}
	state, err := filepath.EvalSymlinks(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	path, prior := successorStartupPriorLedger(t, s, hello)
	boot := contract.OwnerRuntimeBootstrap{Version: 1, StateDir: state, SpoolRoot: filepath.Dir(path), Prepared: prepared, BootstrapToken: strings.Repeat("e", 64)}
	marker := filepath.Join(state, "successor-startup-filled")
	control, child, logPath := successorStartupChild(t, boot, marker)
	defer control.Close()
	var filled int
	var bound contract.OwnerBindReply
	for end := time.Now().Add(5 * time.Second); ; {
		raw, readErr := os.ReadFile(marker)
		if readErr == nil {
			filled, readErr = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		current, bindErr := s.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
		if readErr == nil && filled > 0 && bindErr == nil && current.Binding != nil {
			state, controlErr := s.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, s.ownerPublicKey())
			if controlErr == nil && state.Permit.ControlRevision == 1 && state.Permit.CoordinatorEpoch == pending.Epoch && state.AppliedControlRevision == 0 {
				bound.Binding, bound.Permit = *current.Binding, state.Permit
				break
			}
		}
		if time.Now().After(end) {
			t.Fatal("real Bind did not commit while FD4 was blocked", bindErr, readErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	external, err := sql.Open("sqlite", filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	var handoffs int
	if err := external.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&handoffs); err != nil || handoffs != 0 {
		t.Fatal("pause occurred after handoff", handoffs, err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, prior) {
		t.Fatal("pause changed prior ledger", err)
	}
	t.Logf("pause proof: Bind epoch=%d revision=1 Applied=0 handoff rows=0 FD4 fill=%d", pending.Epoch, filled)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	nextCtx, stopNext := context.WithCancel(context.Background())
	t.Cleanup(func() { stopNext(); _ = next.Close() })
	var traceMu sync.Mutex
	var requests []contract.OwnerRPCRequest
	releaseACK := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseACK) }) }
	defer release()
	next.ownerRPCReplyHook = func(request contract.OwnerRPCRequest) bool {
		traceMu.Lock()
		requests = append(requests, request)
		traceMu.Unlock()
		if rejectSession && request.Kind == "recovery_scope" {
			select {
			case <-releaseACK:
			case <-ctx.Done():
			}
		}
		return false
	}
	go func() { _ = next.Serve(nextCtx) }()
	second, epochPending := successorTransportConnect(t, next, hello)
	if epochPending.Kind != ipc.KindOwnerHostPending || epochPending.Epoch <= pending.Epoch {
		t.Fatal("same FirstHost did not register in next epoch", epochPending.Kind, epochPending.Epoch)
	}
	if err := control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	filler := make([]byte, filled)
	if n, err := io.ReadFull(control, filler); err != nil || n != filled || !bytes.Equal(filler, bytes.Repeat([]byte{0x55}, filled)) {
		t.Fatal("exact FD4 drain", n, filled, err)
	}
	boundFrame, err := ipc.Read(control)
	verified, verifyErr := host.VerifyInitialOwnerBound(boot, child.identity, boundFrame)
	if err != nil || verifyErr != nil || verified.Binding != bound.Binding || verified.Permit != bound.Permit {
		t.Fatal("actual Bound after exact drain", err, verifyErr)
	}
	_ = control.SetReadDeadline(time.Time{})
	t.Logf("exact FD4 drain=%d; original Bound epoch=%d; same HostPending epoch=%d", filled, verified.Permit.CoordinatorEpoch, epochPending.Epoch)
	for end := time.Now().Add(3 * time.Second); ; {
		if err := external.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs WHERE applied=1`).Scan(&handoffs); err != nil {
			t.Fatal("handoff observation", err)
		}
		if handoffs == 1 {
			raw, readErr := os.ReadFile(path)
			var ledger struct {
				Scope    string `json:"scope_sha256"`
				Attempts int    `json:"attempts"`
			}
			scope, scopeErr := contract.OriginalOwnerRecoveryScopeSHA256(verified.Binding.Scope, verified.Binding.Owner)
			if readErr != nil || scopeErr != nil || json.Unmarshal(raw, &ledger) != nil || ledger.Scope != scope || ledger.Attempts != 0 {
				t.Fatal("applied handoff file proof", readErr, scopeErr, ledger)
			}
			digest := sha256.Sum256(raw)
			t.Logf("real handoff applied row=1 canonical next SHA256=%x attempts=0", digest)
			break
		}
		select {
		case <-child.done:
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("real successor exited before handoff applied: rows=%d; child=%s", handoffs, logs)
		default:
		}
		if time.Now().After(end) {
			t.Fatal("runtime handoff did not apply")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rejectSession {
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(time.Second); ; {
			next.mu.Lock()
			absent := next.ownerTransports[hello.LaunchID] == nil
			next.mu.Unlock()
			if absent {
				break
			}
			if time.Now().After(end) {
				t.Fatal("closed epoch2 session remained registered")
			}
			time.Sleep(5 * time.Millisecond)
		}
		release()
		t.Log("applied handoff ACK released only after exact epoch2 Host session removal")
		select {
		case <-child.done:
			child.mu.Lock()
			waitErr, processState := child.waitErr, child.command.ProcessState
			child.mu.Unlock()
			logs, _ := os.ReadFile(logPath)
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) || processState == nil || processState.ExitCode() != 1 || !strings.Contains(string(logs), "original_owner_rejected") {
				t.Fatalf("missing session did not reject real runtime: original Wait=%v state=%v logs=%s", waitErr, processState, logs)
			}
			state, err := next.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, next.ownerPublicKey())
			if err != nil || state.Permit.ControlRevision != 1 || state.Permit.CoordinatorEpoch != 1 || state.AppliedControlRevision != 0 {
				t.Fatal("rejected startup changed control permit", state, err)
			}
			t.Logf("session rejected; original Owner PID=%d Cmd Wait=exit status 1; no control revision advanced", child.identity.PID)
		case <-time.After(3 * time.Second):
			t.Fatal("rejected original Owner did not naturally return Wait")
		}
	}
	for end := time.Now().Add(5 * time.Second); !rejectSession; {
		state, e := next.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, next.ownerPublicKey())
		presence, pe := next.db.InspectHostPresence(ctx, hello.LaunchID, hello.LaunchToken)
		if e == nil && pe == nil && state.Permit.ControlRevision == 2 && state.AppliedControlRevision == 2 && state.Permit.CoordinatorEpoch == epochPending.Epoch && presence.Status == "ready" {
			owner, inspectErr := next.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
			if state.Permit.NewHost != verified.Permit.NewHost || inspectErr != nil || owner.Binding == nil || *owner.Binding != verified.Binding {
				t.Fatal("startup replaced Owner/Host tuple")
			}
			break
		}
		select {
		case <-child.done:
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("real successor exited before epoch2 HostReady: control=%+v err=%v presence=%+v err=%v; child=%s", state, e, presence, pe, logs)
		default:
		}
		if time.Now().After(end) {
			t.Fatalf("same-tuple startup did not reach revision2/Applied/HostReady: control=%+v err=%v presence=%+v err=%v", state, e, presence, pe)
		}
		time.Sleep(5 * time.Millisecond)
	}
	traceMu.Lock()
	observed := append([]contract.OwnerRPCRequest(nil), requests...)
	traceMu.Unlock()
	seenState, seenAttach, seenApplied, seenActivate := false, false, false, false
	for _, request := range observed {
		t.Logf("real successful signed RPC kind=%s epoch=%d revision=%d", request.Kind, request.CoordinatorEpoch, request.ControlRevision)
		if request.CoordinatorEpoch != 2 {
			t.Fatal("epoch2 startup used another RPC epoch", request.Kind, request.CoordinatorEpoch)
		}
		switch request.Kind {
		case "state":
			if request.ControlRevision == 1 && !seenAttach {
				seenState = true
			}
		case "attach":
			var attach contract.OwnerAttachRequest
			if !seenState || seenAttach || request.ControlRevision != 1 || contract.DecodeOwnerRuntimeStrict(request.Body, &attach) != nil || attach.ExpectedControlRevision != 1 || attach.OldHost != verified.Permit.NewHost || attach.NewHost != verified.Permit.NewHost || attach.Pending != nil {
				t.Fatal("startup Attach did not renew the same tuple once", request.Kind, request.ControlRevision)
			}
			seenAttach = true
		case "applied":
			if !seenAttach || request.ControlRevision != 2 {
				t.Fatal("Applied preceded valid Attach", request.Kind, request.ControlRevision)
			}
			seenApplied = true
		case "activate":
			if !seenApplied || request.ControlRevision != 2 {
				t.Fatal("Activate preceded Applied", request.Kind, request.ControlRevision)
			}
			seenActivate = true
		}
	}
	if !seenState || (!rejectSession && (!seenAttach || !seenApplied || !seenActivate)) || (rejectSession && (seenAttach || seenApplied || seenActivate)) {
		t.Fatal("real signed startup sequence incomplete or admitted missing session")
	}
	if err := external.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs WHERE applied=1`).Scan(&handoffs); err != nil || handoffs != 1 {
		t.Fatal("runtime did not complete handoff", handoffs, err)
	}
	raw, err := os.ReadFile(path)
	var old, current map[string]any
	if err != nil || json.Unmarshal(prior, &old) != nil || json.Unmarshal(raw, &current) != nil {
		t.Fatal("canonical ledger unavailable", err)
	}
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(verified.Binding.Scope, verified.Binding.Owner)
	if current["scope_sha256"] != scope || current["attempts"] != float64(0) {
		t.Fatal("scope or recovery attempts changed", current)
	}
	old["scope_sha256"] = current["scope_sha256"]
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(current)
	if !bytes.Equal(a, b) {
		t.Fatal("startup changed retained non-scope recovery fields")
	}
	for _, suffix := range []string{".ensure", ".invocation"} {
		if _, err := os.Stat(marker + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("startup called Ensure or worker invocation", suffix, err)
		}
	}
	if birth, err := process.Birth(child.identity.PID); !rejectSession && (err != nil || birth != child.identity.Birth) {
		t.Fatal("original Owner tuple changed", err)
	}
	if birth, err := process.Birth(hello.PID); err != nil || birth != hello.Birth {
		t.Fatal("original FirstHost tuple changed", err)
	}
	if rejectSession {
		t.Log("rejected startup preserved handoff scope/budget; Ensure=0; no invocation or replacement Start")
		return
	}
	_ = second.SetDeadline(time.Now().Add(time.Second))
	if err := child.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
		child.mu.Lock()
		waitErr, processState := child.waitErr, child.command.ProcessState
		child.mu.Unlock()
		if waitErr != nil || processState == nil || processState.ExitCode() != 0 {
			t.Fatal("real Owner original Wait", waitErr, processState)
		}
		t.Log("real successor original Cmd Wait=0; Ensure=0; recovery attempts=0; unchanged Owner/FirstHost tuples")
	case <-time.After(5 * time.Second):
		t.Fatal("real Owner original Wait timeout")
	}
}

func successorStartupPriorLedger(t *testing.T, s *Server, hello contract.HostHello) (string, []byte) {
	t.Helper()
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	var priorID string
	if err := external.QueryRow(`SELECT predecessor_owner_id FROM owner_succession_authorizations WHERE command_id=?`, hello.ResumeCommandID).Scan(&priorID); err != nil {
		t.Fatal(err)
	}
	prior, err := s.db.InspectOriginalOwner(context.Background(), priorID)
	if err != nil || prior.Binding == nil {
		t.Fatal("synthetic predecessor binding", err)
	}
	dir := filepath.Join(s.stateDir, "host-spool", hello.LaunchID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".coordinator-recovery.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var directoryInfo, lockInfo syscall.Stat_t
	if err := syscall.Stat(dir, &directoryInfo); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(lock, &lockInfo); err != nil {
		t.Fatal(err)
	}
	authority := sha256.Sum256([]byte(fmt.Sprintf("original-owner-recovery-authority-v2\x00%d:%d:%d:%d", directoryInfo.Dev, directoryInfo.Ino, lockInfo.Dev, lockInfo.Ino)))
	scope, err := contract.OriginalOwnerRecoveryScopeSHA256(prior.Binding.Scope, prior.Binding.Owner)
	if err != nil {
		t.Fatal(err)
	}
	record := struct {
		Version     int    `json:"version"`
		Authority   string `json:"authority_sha256"`
		Scope       string `json:"scope_sha256"`
		Attempts    int    `json:"attempts"`
		Next        int64  `json:"next_attempt_unix_ms"`
		Unavailable bool   `json:"unavailable"`
	}{2, hex.EncodeToString(authority[:]), scope, 0, 0, false}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	path := filepath.Join(dir, ".coordinator-recovery.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := host.OpenExistingOwnerRecoveryLease(s.stateDir, prior.Binding.Scope, prior.Binding.Owner)
	if err != nil {
		t.Fatal("prior canonical ledger existing-only validation", err)
	}
	lease.Close()
	digest := sha256.Sum256(body)
	t.Logf("prior canonical ledger SHA256=%x (newline included), attempts=0", digest)
	return path, body
}

func successorStartupChild(t *testing.T, bootstrap contract.OwnerRuntimeBootstrap, marker string) (*net.UnixConn, *realOwnerRuntimeChild, string) {
	t.Helper()
	parent, childFD, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOriginalOwnerSuccessorStartupNativeChild$", "-test.v")
	cmd.Env = append(os.Environ(), "SUCCESSOR_STARTUP_NATIVE_CHILD=1", "SUCCESSOR_STARTUP_NATIVE_MARKER="+marker)
	cmd.ExtraFiles = []*os.File{reader, childFD}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(bootstrap.StateDir, "successor-startup-child.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = childFD.Close()
	child := &realOwnerRuntimeChild{command: cmd, identity: contract.OriginalOwnerTransport{PID: cmd.Process.Pid}, done: make(chan struct{})}
	go func() { child.mu.Lock(); child.waitErr = cmd.Wait(); child.mu.Unlock(); close(child.done) }()
	t.Cleanup(func() {
		select {
		case <-child.done:
		default:
			_ = child.command.Process.Signal(syscall.SIGTERM)
			select {
			case <-child.done:
			case <-time.After(5 * time.Second):
				_ = child.command.Process.Kill()
				<-child.done
				t.Error("forced original Owner cleanup")
			}
		}
		_ = parent.Close()
		_ = log.Close()
		child.mu.Lock()
		waitErr, processState := child.waitErr, child.command.ProcessState
		child.mu.Unlock()
		logs, _ := os.ReadFile(logPath)
		t.Logf("cleanup exact original Cmd PID=%d Wait=%v state=%v; raw child log:\n%s", child.identity.PID, waitErr, processState, logs)
	})
	child.identity.Birth, err = process.Birth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return parent, child, logPath
}
