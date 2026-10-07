package coordinator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
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
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// TestOriginalOwnerRuntimeChild is a real owner process used by the recovery
// integration fixture below.  It intentionally does not speak the test RPC
// protocol: FD3/FD4 are the same inherited runtime channels used in product.
func TestOriginalOwnerRuntimeChild(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_OWNER_RUNTIME_CHILD") != "1" {
		return
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
	err = host.RunOriginalOwner(ctx, host.OriginalOwnerConfig{Bootstrap: bootstrap, Control: control, InvocationProvider: ownerRuntimeSleepInvocation, EnsureCoordinator: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
}

// A controlled worker from this test binary avoids shell aliases and platform
// specific directories. Production process pin/canonical-path checks stay on.
func TestOriginalOwnerRuntimeWorker(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_OWNER_RUNTIME_WORKER") != "1" {
		return
	}
	marker := os.Getenv("OWNER_RUNTIME_MARKER")
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("worker marker", err, closeErr)
	}
	time.Sleep(30 * time.Second)
}

type ownerRuntimeSleep struct{ marker, binary string }

func (i ownerRuntimeSleep) Args() []string {
	return []string{i.binary, "-test.run=^TestOriginalOwnerRuntimeWorker$"}
}
func (i ownerRuntimeSleep) WorkingDirectory() string { return filepath.Dir(i.marker) }
func (ownerRuntimeSleep) Stdin() []byte              { return nil }
func (i ownerRuntimeSleep) Environment() map[string]string {
	return map[string]string{"OWNER_RUNTIME_MARKER": i.marker, "ORCHESTRATOR_OWNER_RUNTIME_WORKER": "1"}
}

func ownerRuntimeSleepInvocation(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	return ownerRuntimeSleep{marker: os.Getenv("OWNER_RUNTIME_MARKER"), binary: binary}, nil
}

// startRealOwnerRuntimeChild gives recovery tests a real FD3/FD4 owner while
// leaving the first transport connection under the test's control.
type realOwnerRuntimeChild struct {
	command  *exec.Cmd
	identity contract.OriginalOwnerTransport
	done     chan struct{}
	mu       sync.Mutex
	waitErr  error
}

func startRealOwnerRuntimeChild(t *testing.T, bootstrap contract.OwnerRuntimeBootstrap, marker string) (*net.UnixConn, *realOwnerRuntimeChild) {
	t.Helper()
	parent, childFD, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOriginalOwnerRuntimeChild$", "-test.v")
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_OWNER_RUNTIME_CHILD=1", "OWNER_RUNTIME_MARKER="+marker)
	cmd.ExtraFiles = []*os.File{reader, childFD}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = childFD.Close()
	identity := contract.OriginalOwnerTransport{PID: cmd.Process.Pid}
	identity.Birth, err = process.BirthContext(context.Background(), identity.PID)
	if err != nil || identity.Validate() != nil {
		t.Fatal("owner child birth", err)
	}
	child := &realOwnerRuntimeChild{command: cmd, identity: identity, done: make(chan struct{})}
	go func() { child.mu.Lock(); child.waitErr = cmd.Wait(); child.mu.Unlock(); close(child.done) }()
	raw, _ := json.Marshal(bootstrap)
	if err = writer.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parent.Close()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-child.done
			t.Errorf("forced cleanup of original owner child")
		}
	})
	return parent, child
}

func TestOriginalOwnerNativeSameTupleRecovery(t *testing.T) {
	for _, scenario := range []string{"same_epoch_eof", "hard_restart", "lost_attach_ack_restart"} {
		t.Run(scenario, func(t *testing.T) { testOriginalOwnerNativeSameTupleRecovery(t, scenario) })
	}
}

func testOriginalOwnerNativeSameTupleRecovery(t *testing.T, scenario string) {
	s, hello, stop := ownerRuntimeFixtureWithCancel(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func(server *Server) net.Conn {
		t.Helper()
		conn, err := net.Dial("unix", server.SocketPath())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(hello)
		if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: hello.LaunchID, Payload: body}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	conn := dial(s)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	pending, err := ipc.Read(conn)
	if err != nil || pending.Kind != ipc.KindOwnerHostPending {
		t.Fatalf("pending %s %v", pending.Kind, err)
	}
	token := strings.Repeat("a", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	prepare := contract.OwnerPrepareRequest{Version: 1, BootstrapID: "runtime-native", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(15 * time.Second).UnixMilli()}
	body, _ := json.Marshal(prepare)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "runtime-prepare", Epoch: pending.Epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(conn)
	var prepared contract.OwnerPrepared
	if err != nil || frame.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared) != nil {
		t.Fatalf("prepare %s %v", frame.Kind, err)
	}
	stateDir, err := filepath.EvalSymlinks(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RPCSocketPath = filepath.Join(stateDir, "owner.sock")
	boot := contract.OwnerRuntimeBootstrap{Version: 1, StateDir: stateDir, SpoolRoot: filepath.Join(stateDir, "host-spool", hello.LaunchID), Prepared: prepared, BootstrapToken: token}
	marker := filepath.Join(t.TempDir(), "worker.pid")
	control, child := startRealOwnerRuntimeChild(t, boot, marker)
	boundFrame, err := ipc.Read(control)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := host.VerifyInitialOwnerBound(boot, child.identity, boundFrame)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the child-owned applied/activate RPCs to make the first session ready.
	deadline := time.Now().Add(5 * time.Second)
	activated := false
	for time.Now().Before(deadline) {
		state, e := s.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, s.ownerPublicKey())
		if e == nil && state.AppliedControlRevision == 1 {
			presence, _ := s.db.InspectHostPresence(ctx, hello.LaunchID, hello.LaunchToken)
			if presence.Status == "ready" {
				activated = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !activated {
		state, _ := s.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, s.ownerPublicKey())
		presence, _ := s.db.InspectHostPresence(ctx, hello.LaunchID, hello.LaunchToken)
		s.mu.Lock()
		session := s.ownerTransports[prepared.Intent.Scope.ProducerID]
		s.mu.Unlock()
		t.Logf("activate evidence host_status=%q transport_present=%t transport_closed=%t permit_epoch=%d revision=%d applied=%d", presence.Status, session != nil, session != nil && session.transportClosed.Load(), state.Permit.CoordinatorEpoch, state.Permit.ControlRevision, state.AppliedControlRevision)
		t.Fatalf("initial activate %+v", state)
	}
	// This is the Coordinator's real signed launch frame, read from the
	// original transport and forwarded unchanged to the owner FD4 reader.
	var launch ipc.Envelope
	for {
		frame, readErr := ipc.Read(conn)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if frame.Kind == ipc.KindOwnerCommand {
			launch = frame
			break
		}
	}
	if err = ipc.Write(control, launch); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var workerPID int
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(marker)
		if readErr == nil {
			workerPID, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if workerPID > 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if workerPID <= 1 {
		t.Fatal("controlled worker did not start")
	}
	markerLines, err := os.ReadFile(marker)
	if err != nil || len(strings.Fields(string(markerLines))) != 1 {
		t.Fatalf("worker Start count marker=%q err=%v", markerLines, err)
	}
	workerBirth, err := process.BirthContext(ctx, workerPID)
	if err != nil {
		t.Fatal(err)
	}
	next := s
	var attachCommitted chan struct{}
	var nextStop context.CancelFunc
	if scenario == "hard_restart" || scenario == "lost_attach_ack_restart" {
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		stop() // hard path: Close before Serve cancel
		next, err = NewServer(s.stateDir)
		if err != nil {
			t.Fatal(err)
		}
		nextCtx, cancelNext := context.WithCancel(context.Background())
		nextStop = cancelNext
		defer func() { nextStop(); _ = next.Close() }()
		go func() { _ = next.Serve(nextCtx) }()
		if scenario == "lost_attach_ack_restart" {
			attachCommitted = make(chan struct{})
			var once sync.Once
			next.ownerRPCReplyHook = func(request contract.OwnerRPCRequest) bool {
				if request.Kind == "attach" {
					once.Do(func() { close(attachCommitted) })
					return true // commit happened; deliberately lose only its ACK
				}
				return false
			}
		}
	} else {
		// Only the Coordinator-to-transport socket is lost. The durable epoch
		// stays unchanged, so recovery must replay the existing permit rather
		// than manufacture an Attach renewal.
		if err = conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	offline, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "offline"})
	if err = ipc.Write(control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "offline", Payload: offline}); err != nil {
		t.Fatal(err)
	}
	connect, err := ipc.Read(control)
	if err != nil {
		t.Fatal(err)
	}
	if connect.Kind != ipc.KindOwnerTransportControl {
		t.Fatalf("reconnect %s", connect.Kind)
	}
	conn2 := dial(next)
	defer conn2.Close()
	_ = conn2.SetDeadline(time.Now().Add(5 * time.Second))
	pending2, err := ipc.Read(conn2)
	if err != nil || pending2.Kind != ipc.KindOwnerHostPending {
		t.Fatalf("renew pending %s %v", pending2.Kind, err)
	}
	if (scenario == "hard_restart" || scenario == "lost_attach_ack_restart") && pending2.Epoch <= bound.Permit.CoordinatorEpoch {
		t.Fatalf("restart did not advance epoch: %d <= %d", pending2.Epoch, bound.Permit.CoordinatorEpoch)
	}
	if scenario == "same_epoch_eof" && pending2.Epoch != bound.Permit.CoordinatorEpoch {
		t.Fatalf("socket EOF advanced epoch: %d != %d", pending2.Epoch, bound.Permit.CoordinatorEpoch)
	}
	if err = ipc.Write(control, pending2); err != nil {
		t.Fatal(err)
	}
	if scenario == "lost_attach_ack_restart" {
		select {
		case <-attachCommitted:
		case <-time.After(5 * time.Second):
			t.Fatal("Attach did not commit before ACK loss")
		}
		committed, committedErr := next.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, next.ownerPublicKey())
		if committedErr != nil || committed.Permit.ControlRevision != 2 || committed.AppliedControlRevision != 1 {
			t.Fatalf("lost ACK was not a durable unapplied Attach: %+v %v", committed, committedErr)
		}
		if err = next.Close(); err != nil {
			t.Fatal(err)
		}
		nextStop()
		final, finalErr := NewServer(s.stateDir)
		if finalErr != nil {
			t.Fatal(finalErr)
		}
		finalCtx, finalStop := context.WithCancel(context.Background())
		defer func() { finalStop(); _ = final.Close() }()
		go func() { _ = final.Serve(finalCtx) }()
		next = final
		// One fresh connection carries both exact historical replay and the
		// distinct new-epoch renewal. Neither operation needs another Ensure.
		if err = control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		connect, readErr := ipc.Read(control)
		if readErr != nil || connect.Kind != ipc.KindOwnerTransportControl {
			t.Fatalf("lost ACK reconnect: %s %v", connect.Kind, readErr)
		}
		connReplay := dial(next)
		defer connReplay.Close()
		_ = connReplay.SetDeadline(time.Now().Add(5 * time.Second))
		pendingReplay, pendingErr := ipc.Read(connReplay)
		if pendingErr != nil || pendingReplay.Kind != ipc.KindOwnerHostPending || pendingReplay.Epoch != next.epoch {
			t.Fatalf("lost ACK pending: %s %v", pendingReplay.Kind, pendingErr)
		}
		if writeErr := ipc.Write(control, pendingReplay); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	wantRevision := uint64(2)
	if scenario == "same_epoch_eof" {
		wantRevision = 1
	}
	if scenario == "lost_attach_ack_restart" {
		wantRevision = 3
	}
	for time.Now().Before(deadline) {
		state, e := next.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, next.ownerPublicKey())
		if e == nil && state.AppliedControlRevision == wantRevision && state.Permit.CoordinatorEpoch == next.epoch {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, err := next.db.OriginalOwnerControl(ctx, prepared.Intent.Scope.OwnerID, next.ownerPublicKey())
	if err != nil || state.AppliedControlRevision != wantRevision || state.Permit.NewHost != bound.Permit.NewHost {
		t.Fatalf("same tuple renewal %+v %v", state, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var presence store.HostPresenceSnapshot
	for time.Now().Before(deadline) {
		presence, err = next.db.InspectHostPresence(ctx, hello.LaunchID, hello.LaunchToken)
		if err == nil && presence.Status == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || presence.Status != "ready" {
		t.Fatalf("renewed transport not activated: %+v %v", presence, err)
	}
	if birth, birthErr := process.BirthContext(ctx, workerPID); birthErr != nil || birth != workerBirth {
		t.Fatalf("worker changed across transport recovery pid=%d birth=%q err=%v", workerPID, birth, birthErr)
	}
	_ = control.Close()
	if err = child.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
		child.mu.Lock()
		waitErr := child.waitErr
		state := child.command.ProcessState
		child.mu.Unlock()
		if waitErr != nil || state == nil || state.ExitCode() != 0 {
			t.Fatalf("owner graceful Wait err=%v state=%v", waitErr, state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner original Wait timeout")
	}
	if _, err = process.BirthContext(context.Background(), workerPID); err == nil {
		t.Fatal("worker remained after owner cleanup")
	}
}

type ownerChildConfig struct {
	Transport      bool
	Socket         string
	Prepared       contract.OwnerPrepared
	BootstrapToken string
}
type ownerChildStep struct {
	Kind      string
	ID        string
	Body      json.RawMessage
	DropReply bool
	Revision  *uint64
}
type ownerChildReply struct {
	Error string
	Body  json.RawMessage
	Owner contract.OriginalOwnerIdentity
}

// The test child, like the runtime owner, receives enrollment only on FD3 and
// keeps its private signing key in memory. FD4 is a test-only result channel.
func TestOriginalOwnerRPCChild(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_OWNER_RPC_CHILD") != "1" {
		return
	}
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	in, out := os.NewFile(3, "owner-test-bootstrap"), os.NewFile(4, "owner-test-result")
	defer in.Close()
	defer out.Close()
	decoder, encoder := json.NewDecoder(in), json.NewEncoder(out)
	var config ownerChildConfig
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	if config.Transport {
		runOwnerTransportTestChild(t, config, decoder, encoder)
		return
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.OriginalOwnerIdentity{PID: os.Getpid(), Birth: birth, PublicKey: hex.EncodeToString(pub)}
	coordinatorKey, err := hex.DecodeString(config.Prepared.CoordinatorPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client := ipc.OwnerClient{SocketPath: config.Socket, Intent: config.Prepared.Intent, Owner: owner, PrivateKey: key, CoordinatorKey: ed25519.PublicKey(coordinatorKey)}
	var bound contract.OwnerBindReply
	var applied contract.OwnerPermitApplied
	var transportStep func(ownerChildStep) ownerChildReply
	var transportFinish func()
	defer func() {
		if transportFinish != nil {
			transportFinish()
		}
	}()
	for {
		var step ownerChildStep
		if err := decoder.Decode(&step); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		if step.Kind == "spawn_transport" {
			if transportStep != nil {
				t.Fatal("second transport Start")
			}
			childConfig := config
			childConfig.Transport = true
			childConfig.BootstrapToken = ""
			transportStep, transportFinish = startOwnerRPCChild(t, childConfig)
			reply := transportStep(ownerChildStep{Kind: "identity"})
			if err := encoder.Encode(reply); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if step.Kind == "transport" {
			var childStep ownerChildStep
			if transportStep == nil || json.Unmarshal(step.Body, &childStep) != nil {
				t.Fatal("missing transport step")
			}
			if err := encoder.Encode(transportStep(childStep)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if step.Kind == "identity" {
			if err := encoder.Encode(ownerChildReply{Owner: owner}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		kind, revision := step.Kind, bound.Permit.ControlRevision
		var body any = step.Body
		switch kind {
		case "bind", "bad_bootstrap":
			kind, revision = "bind", 0
			token := config.BootstrapToken
			if step.Kind == "bad_bootstrap" {
				token = strings.Repeat("b", 64)
			}
			body = contract.OriginalOwnerHello{Scope: config.Prepared.Intent.Scope, BootstrapID: config.Prepared.Intent.BootstrapID, BootstrapToken: token, Owner: owner, FirstHost: config.Prepared.Intent.FirstHost, CoordinatorKeyID: config.Prepared.Intent.CoordinatorKeyID}
		case "applied", "activate":
			if applied.Permit.PermitID != bound.Permit.PermitID {
				applied, err = contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: bound.Permit, Reconciled: contract.HostReconciled{ProducerID: config.Prepared.Intent.Scope.ProducerID}}, key)
				if err != nil {
					t.Fatal(err)
				}
			}
			body = applied
		case "state":
			body = struct{}{}
		}
		if step.Revision != nil {
			revision = *step.Revision
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var reply contract.OwnerRPCReply
		if step.DropReply {
			err = ownerChildDropReply(ctx, client, revision, kind, step.ID, body)
		} else {
			reply, err = client.Call(ctx, revision, kind, step.ID, body)
		}
		cancel()
		if err == nil && kind == "bind" && !step.DropReply {
			if err = contract.DecodeOwnerRuntimeStrict(reply.Body, &bound); err != nil {
				t.Fatal(err)
			}
			if contract.VerifyOriginalOwnerBinding(bound.Binding, client.CoordinatorKey) != nil || contract.VerifyOwnerControlPermit(bound.Permit, client.CoordinatorKey) != nil {
				t.Fatal("invalid binding/permit")
			}
		}
		if err == nil && !step.DropReply && kind == "state" {
			var state contract.OwnerRuntimeState
			if err = contract.DecodeOwnerRuntimeStrict(reply.Body, &state); err != nil {
				t.Fatal(err)
			}
			bound = contract.OwnerBindReply{Binding: state.Binding, Permit: state.Permit}
		}
		if err == nil && !step.DropReply && kind == "attach" {
			if err = contract.DecodeOwnerRuntimeStrict(reply.Body, &bound.Permit); err != nil {
				t.Fatal(err)
			}
		}
		if err = encoder.Encode(ownerChildReply{Error: errorCode(err), Body: reply.Body, Owner: owner}); err != nil {
			t.Fatal(err)
		}
	}
}

// Close the child's own authenticated socket immediately after sending the
// signed request. The parent proves durable commit separately before retry.
func ownerChildDropReply(ctx context.Context, client ipc.OwnerClient, revision uint64, kind, id string, value any) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", client.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	opening, _ := json.Marshal(contract.OwnerRPCOpen{Version: 1, OwnerID: client.Intent.Scope.OwnerID, Owner: client.Owner})
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerRPC, RequestID: id, Payload: opening}); err != nil {
		return err
	}
	frame, err := ipc.Read(conn)
	if err != nil {
		return err
	}
	challenge, err := contract.DecodeOwnerChallenge(frame.Payload)
	if err != nil || contract.VerifyOwnerChallenge(challenge, client.CoordinatorKey, client.Intent.Scope.OwnerID) != nil {
		return errors.New("challenge rejected")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	scope, _ := contract.OriginalOwnerScopeSHA256(client.Intent.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(client.Owner)
	request, err := contract.SignOwnerRPCRequest(contract.OwnerRPCRequest{Version: 1, OwnerID: client.Intent.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorEpoch: challenge.CoordinatorEpoch, ControlRevision: revision, RequestID: id, Kind: kind, Nonce: challenge.Nonce, Body: body}, client.PrivateKey)
	if err != nil {
		return err
	}
	body, _ = json.Marshal(request)
	return ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerRPC, RequestID: id, Epoch: challenge.CoordinatorEpoch, Payload: body})
}

func startOwnerRPCChild(t *testing.T, config ownerChildConfig) (func(ownerChildStep) ownerChildReply, func()) {
	t.Helper()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOriginalOwnerRPCChild$", "-test.v")
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_OWNER_RPC_CHILD=1")
	cmd.ExtraFiles = []*os.File{inRead, outWrite}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = inRead.Close()
	_ = outWrite.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	finished := false
	t.Cleanup(func() {
		_ = inWrite.Close()
		if !finished {
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				cancel()
				err = <-done
			}
			t.Logf("failed-fixture cleanup original Wait=%v logs=%s", err, logs.String())
		}
		_ = outRead.Close()
		cancel()
	})
	encoder, decoder := json.NewEncoder(inWrite), json.NewDecoder(outRead)
	if err = encoder.Encode(config); err != nil {
		t.Fatal(err)
	}
	step := func(step ownerChildStep) ownerChildReply {
		t.Helper()
		if err := encoder.Encode(step); err != nil {
			t.Fatal(err)
		}
		var reply ownerChildReply
		read := make(chan error, 1)
		go func() { read <- decoder.Decode(&reply) }()
		select {
		case err := <-read:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("owner child timeout")
		}
		return reply
	}
	finish := func() {
		t.Helper()
		_ = inWrite.Close()
		select {
		case err := <-done:
			finished = true
			if err != nil {
				t.Fatalf("original Wait failed: %v %s", err, logs.String())
			}
		case <-ctx.Done():
			t.Fatal("owner child original Wait timeout")
		}
		if ctx.Err() != nil {
			t.Fatal("owner child timed out")
		}
		t.Logf("owner child pid=%d original Wait=0, natural bootstrap EOF, no timeout/forced cleanup", cmd.Process.Pid)
		if strings.Contains(logs.String(), "original Wait=") {
			t.Logf("nested original child evidence:\n%s", logs.String())
		}
	}
	return step, finish
}

func TestOriginalOwnerNativeRPCActivationAndLostACK(t *testing.T) {
	for _, scenario := range []string{"delivered", "lost", "shutdown", "close"} {
		t.Run(scenario, func(t *testing.T) { testOriginalOwnerNativeRPC(t, scenario) })
	}
}

func testOriginalOwnerNativeRPC(t *testing.T, scenario string) {
	dropReply := scenario == "lost"
	s, hello, stopServe := ownerRuntimeFixtureWithCancel(t)
	conn, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	body, _ := json.Marshal(hello)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "transport", Payload: body}); err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindOwnerHostPending {
		t.Fatalf("pending: %+v %v", frame, err)
	}
	token := strings.Repeat("a", 64)
	tokenHash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	prepare := contract.OwnerPrepareRequest{Version: 1, BootstrapID: "native-bootstrap", BootstrapTokenSHA256: tokenHash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()}
	body, _ = json.Marshal(prepare)
	request := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "prepare-native", Epoch: s.epoch, Payload: body}
	var prepared contract.OwnerPrepared
	var first []byte
	for n := 0; n < 2; n++ {
		if err = ipc.Write(conn, request); err != nil {
			t.Fatal(err)
		}
		frame, err = ipc.Read(conn)
		if err != nil || frame.Kind != ipc.KindResponse {
			t.Fatalf("prepare: %+v %v", frame, err)
		}
		if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &prepared); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			first = append([]byte(nil), frame.Payload...)
		} else if !bytes.Equal(first, frame.Payload) {
			t.Fatal("Prepare replay changed intent")
		}
	}
	if bytes.Contains(first, []byte(hello.LaunchToken)) || bytes.Contains(first, []byte(token)) {
		t.Fatal("private transport secret leaked into owner intent")
	}
	step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: prepared.RPCSocketPath, Prepared: prepared, BootstrapToken: token})
	check := func(predicate func(store.OriginalOwnerState) bool) store.OriginalOwnerState {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			state, err := s.db.InspectOriginalOwner(context.Background(), prepared.Intent.Scope.OwnerID)
			if err != nil {
				t.Fatal(err)
			}
			if predicate(state) {
				return state
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("owner durable state did not reach expected checkpoint")
		return store.OriginalOwnerState{}
	}
	if got := step(ownerChildStep{Kind: "bad_bootstrap", ID: "wrong-bootstrap"}); got.Error != "original_owner_rejected" {
		t.Fatalf("wrong bootstrap response=%q", got.Error)
	}
	check(func(s store.OriginalOwnerState) bool { return s.Phase == "intended" })
	if got := step(ownerChildStep{Kind: "bind", ID: "bind-native", DropReply: dropReply}); got.Error != "" {
		t.Fatal(got.Error)
	}
	boundState := check(func(s store.OriginalOwnerState) bool { return s.Phase == "bound" })
	before, err := s.db.OriginalOwnerControl(context.Background(), prepared.Intent.Scope.OwnerID, s.ownerPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	got := step(ownerChildStep{Kind: "bind", ID: "bind-native"})
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	var bound contract.OwnerBindReply
	if err = contract.DecodeOwnerRuntimeStrict(got.Body, &bound); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Permit, bound.Permit) || !reflect.DeepEqual(*boundState.Binding, bound.Binding) {
		t.Fatal("lost bind ACK allocated new authority")
	}
	assertFenced := func() {
		t.Helper()
		if _, err := s.db.ClaimReady(context.Background(), hello.LaunchID, s.epoch, 1); err != store.ErrHostRejected {
			t.Fatalf("unactivated dispatch: %v", err)
		}
	}
	assertFenced()
	// The parent has the public identity and exact opening but is not the owner.
	foreign, err := net.Dial("unix", s.OwnerSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	_ = foreign.SetDeadline(time.Now().Add(time.Second))
	body, _ = json.Marshal(contract.OwnerRPCOpen{Version: 1, OwnerID: prepared.Intent.Scope.OwnerID, Owner: got.Owner})
	_ = ipc.Write(foreign, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerRPC, RequestID: "foreign-peer", Payload: body})
	if frame, err = ipc.Read(foreign); err == nil {
		t.Fatalf("foreign kernel peer received authority: %s", frame.Kind)
	}
	_ = foreign.Close()
	if got = step(ownerChildStep{Kind: "applied", ID: "applied-native", DropReply: dropReply}); got.Error != "" {
		t.Fatal(got.Error)
	}
	check(func(s store.OriginalOwnerState) bool { return s.AppliedControlRevision == 1 })
	assertFenced()
	if got = step(ownerChildStep{Kind: "applied", ID: "applied-native"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got = step(ownerChildStep{Kind: "activate", ID: "activate-native"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	frame, err = ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindHostReady {
		t.Fatalf("active ready: %s %v", frame.Kind, err)
	}
	frame, err = ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindOwnerCommand {
		t.Fatalf("signed grant: %s %v", frame.Kind, err)
	}
	command, err := contract.DecodeOwnerCommand(frame.Payload)
	if err != nil || contract.VerifyOwnerCommand(command, s.ownerPublicKey(), bound.Permit) != nil || command.Kind != "launch" {
		t.Fatalf("unauthenticated launch: %v", err)
	}
	if got = step(ownerChildStep{Kind: "activate", ID: "activate-native"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got = step(ownerChildStep{Kind: "state", ID: "read-state"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	var current contract.OwnerRuntimeState
	if err = contract.DecodeOwnerRuntimeStrict(got.Body, &current); err != nil {
		t.Fatal(err)
	}
	if current.Permit != bound.Permit || current.AppliedControlRevision != 1 {
		t.Fatal("activation replay drifted authority")
	}
	var grant contract.LaunchCommand
	if err = contract.DecodeOwnerRuntimeStrict(command.Body, &grant); err != nil {
		t.Fatal(err)
	}
	capabilityDir := t.TempDir()
	if err = os.Chmod(capabilityDir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := contract.ReportCapabilityRegistration{CapabilityID: grant.ReportCapabilityID, TokenHash: strings.Repeat("c", 64), CapabilityDir: capabilityDir, RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, ProducerID: grant.HostID, WorkRevision: grant.WorkRevision, ExecutionEpoch: s.epoch}
	body, _ = json.Marshal(registration)
	if got = step(ownerChildStep{Kind: "report_capability", ID: "register-report", Body: body}); got.Error != "" {
		t.Fatal(got.Error)
	}
	event := contract.Event{Version: 1, ProducerID: grant.HostID, EventID: "prepared-event", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: s.epoch, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: strings.Repeat("d", 64)}
	body, _ = json.Marshal(event)
	if scenario == "close" {
		// Hold the first lock in beginLifecycleExit's documented order. A real
		// authenticated request then waits for this registration barrier.
		s.hostRegistrationMu.Lock()
		got = step(ownerChildStep{Kind: "event", ID: "close-pending-event", Body: body, DropReply: true})
		if got.Error != "" {
			s.hostRegistrationMu.Unlock()
			t.Fatal(got.Error)
		}
		closeDone := make(chan error, 1)
		go func() { closeDone <- s.Close() }()
		select {
		case <-s.ownerAcceptDone:
		case <-time.After(time.Second):
			s.hostRegistrationMu.Unlock()
			t.Fatal("Close did not stop owner admission")
		}
		lifecycleAvailable := make(chan struct{})
		go func() { s.lifecycleMu.Lock(); s.lifecycleMu.Unlock(); close(lifecycleAvailable) }()
		select {
		case <-lifecycleAvailable:
		case <-time.After(200 * time.Millisecond):
			t.Error("Close holds lifecycle lock while waiting for RPC registration lock; graceful exit would deadlock")
		}
		s.hostRegistrationMu.Unlock()
		select {
		case err := <-closeDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close did not drain original RPC")
		}
		select {
		case <-lifecycleAvailable:
		case <-time.After(time.Second):
			t.Fatal("lifecycle lock leaked")
		}
		readback, err := store.Open(filepath.Join(s.stateDir, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := readback.TaskSnapshot(context.Background(), grant.TaskID)
		_ = readback.Close()
		if err != nil || snapshot.SegmentStatus != "launch_requested" {
			t.Errorf("RPC mutated after hard Close fence: %+v %v", snapshot, err)
		}
		finish()
		return
	}
	if scenario == "shutdown" {
		if got = step(ownerChildStep{Kind: "event", ID: "before-shutdown-prepared", Body: body}); got.Error != "" {
			t.Fatal(got.Error)
		}
		stopServe()
		// Consume pending command replays, then prove the signed terminal was
		// delivered before these final protocol-fixture Result/Exited events.
		for {
			frame, err = ipc.Read(conn)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := contract.DecodeOwnerCommand(frame.Payload)
			if err != nil || contract.VerifyOwnerCommand(terminal, s.ownerPublicKey(), bound.Permit) != nil {
				t.Fatal("invalid shutdown command", err)
			}
			if terminal.Kind == "terminal" {
				break
			}
		}
		for index, kind := range []string{contract.EventResult, contract.EventExited} {
			event.Kind, event.EventID, event.Sequence = kind, "final-"+kind, int64(index+2)
			event.PayloadHash = strings.Repeat("e", 64)
			if kind == contract.EventExited {
				code := 0
				event.ExitCode = &code
				event.ActiveMS = 1
			}
			body, _ = json.Marshal(event)
			if got = step(ownerChildStep{Kind: "event", ID: event.EventID, Body: body}); got.Error != "" {
				t.Fatal("final event", got.Error)
			}
		}
		s.dispatchAll() // Same entry used by late events and retry wakeups.
		snapshot, err := s.db.TaskSnapshot(context.Background(), "owner-runtime-dependent")
		if err != nil || snapshot.Status != "ready" || snapshot.AttemptID != "" {
			t.Errorf("terminal was followed by a new dependency claim: %+v %v", snapshot, err)
		}
		finish()
		return
	}
	wrongRevision := uint64(2)
	if got = step(ownerChildStep{Kind: "event", ID: "wrong-revision", Body: body, Revision: &wrongRevision}); got.Error != "original_owner_rejected" {
		t.Fatalf("wrong revision error=%q", got.Error)
	}
	// A fully formed valid event on the transport socket still has no business
	// authority. Its rejection fences dispatch without cancelling the owner.
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindEvent, RequestID: "transport-forgery", Epoch: s.epoch, Payload: body}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := s.db.TaskSnapshot(context.Background(), grant.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.HostStatus == "reconciling" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, err := s.db.TaskSnapshot(context.Background(), grant.TaskID)
	if err != nil || snapshot.HostStatus != "reconciling" || snapshot.SegmentStatus != "launch_requested" {
		t.Fatalf("transport event mutated execution: %+v %v", snapshot, err)
	}
	stopServe()
	deadline = time.Now().Add(time.Second)
	mainClosed := false
	for time.Now().Before(deadline) {
		probe, e := net.DialTimeout("unix", s.SocketPath(), 100*time.Millisecond)
		if e != nil {
			mainClosed = true
			break
		}
		_ = probe.Close()
		time.Sleep(10 * time.Millisecond)
	}
	if !mainClosed {
		t.Fatal("main listener remained open after Serve cancellation")
	}
	if got = step(ownerChildStep{Kind: "event", ID: "owner-prepared", Body: body}); got.Error != "" {
		t.Fatalf("live owner final event channel: %s", got.Error)
	}
	if got = step(ownerChildStep{Kind: "event", ID: "owner-prepared", Body: body}); got.Error != "" {
		t.Fatalf("event ACK replay: %s", got.Error)
	}
	snapshot, err = s.db.TaskSnapshot(context.Background(), grant.TaskID)
	if err != nil || snapshot.HostStatus != "reconciling" || snapshot.SegmentStatus != "prepared" {
		t.Fatalf("owner event not durable: %+v %v", snapshot, err)
	}
	body, _ = json.Marshal(registration)
	if got = step(ownerChildStep{Kind: "report_capability", ID: "register-report", Body: body}); got.Error != "" {
		t.Fatalf("registry replay after transport EOF: %s", got.Error)
	}
	if got = step(ownerChildStep{Kind: "activate", ID: "no-transport"}); got.Error != "original_owner_rejected" {
		t.Fatalf("activated without transport: %q", got.Error)
	}
	finish()
}
