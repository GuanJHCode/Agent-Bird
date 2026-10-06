package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOwnerTransportEntryRequiresDedicatedFDs(t *testing.T) {
	for _, args := range [][]string{nil, {"--bootstrap-fd", "3"}, {"--bootstrap-fd", "4", "--control-fd", "4"}, {"--bootstrap-fd", "3", "--control-fd", "5"}, {"--launch-token", "secret"}} {
		if err := run(context.Background(), append([]string{"owner-transport"}, args...), &bytes.Buffer{}, &bytes.Buffer{}); err == nil || errorCode(err) != "invalid_args" {
			t.Fatalf("invalid inherited entry %v: %v", args, err)
		}
	}
}

// The server sockets below are controlled fixtures, not the real Coordinator.
// This proves the actual CLI/FD/identity/token/relay/Wait path without a provider.
func TestOwnerTransportActualCLIInheritedChannels(t *testing.T) {
	binary := os.Getenv("ORCHESTRATOR_NATIVE_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "orchestrator")
		buildCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		build := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
		out, err := build.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build actual CLI: %v %s", err, out)
		}
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	digest, err := process.ExecutableDigest(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("", "owner-transport-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(state)
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	state, err = filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	ownerListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(state, "owner.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ownerListener.Close()
	hostListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(state, "coordinator.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()
	parentBirth, err := process.BirthContext(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := contract.OriginalOwnerKeyID(pub)
	owner := contract.OriginalOwnerIdentity{PID: os.Getpid(), Birth: parentBirth, PublicKey: hex.EncodeToString(ownerPub)}
	scope := contract.OriginalOwnerScope{Version: 1, RunID: "run", LaunchID: "producer", ProducerID: "producer", OwnerID: "owner", Incarnation: 1, OriginalOriginContextID: "origin", OriginalOriginPID: os.Getpid(), OriginalOriginBirth: parentBirth, HostGeneration: "generation", Executable: binary, ExecutableSHA256: digest}
	old := contract.OriginalOwnerTransport{PID: 2147483, Birth: "old-transport"}
	intent := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: "bootstrap", BootstrapTokenSHA256: strings.Repeat("a", 64), CoordinatorKeyID: keyID, RequestID: "prepare", RequestSHA256: strings.Repeat("b", 64), StartupDeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), FirstHost: old, CoordinatorEpoch: 7}
	bootstrap := contract.OwnerTransportBootstrap{Version: 1, StateDir: state, Intent: intent, Owner: owner, CoordinatorPublicKey: hex.EncodeToString(pub)}
	if err = bootstrap.Validate(); err != nil {
		t.Fatal(err)
	}
	control, childControl, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	defer childControl.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	var output bytes.Buffer
	child := exec.Command(binary, "owner-transport", "--bootstrap-fd", "3", "--control-fd", "4")
	child.ExtraFiles = []*os.File{reader, childControl}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout = &output
	child.Stderr = &output
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = childControl.Close()
	birth, birthErr := process.BirthContext(ctx, child.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- child.Wait(); close(done) }()
	waited := false
	defer func() {
		_ = control.Close()
		if waited {
			return
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = child.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("native original Wait failed after Kill")
			}
			t.Error("native transport required forced cleanup")
		}
	}()
	if birthErr != nil {
		t.Fatal(birthErr)
	}
	identity := contract.OriginalOwnerTransport{PID: child.Process.Pid, Birth: birth}
	raw, _ := json.Marshal(bootstrap)
	_ = writer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err = writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(scope)
	ownerHash, _ := contract.OriginalOwnerIdentitySHA256(owner)
	ticket, err := contract.SignOwnerTransportTicket(contract.OwnerTransportTicket{Version: 1, OwnerID: scope.OwnerID, ScopeSHA256: scopeHash, OwnerIdentitySHA256: ownerHash, CoordinatorKeyID: keyID, CoordinatorEpoch: 7, Pending: contract.OwnerTransportPending{Version: 1, RecoverySlot: 1, RequestID: "replacement", ExpectedControlRevision: 1, OldHost: old, NewHost: identity}, Nonce: strings.Repeat("c", 64), ExpiresUnixMS: time.Now().Add(10 * time.Second).UnixMilli()}, key)
	if err != nil {
		t.Fatal(err)
	}
	sendConnect := func(ticket *contract.OwnerTransportTicket) {
		t.Helper()
		raw, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect", Ticket: ticket})
		_ = control.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := ipc.Write(control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "connect", Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	readControl := func() ipc.Envelope {
		t.Helper()
		_ = control.SetReadDeadline(time.Now().Add(3 * time.Second))
		frame, err := ipc.Read(control)
		if err != nil {
			t.Fatal("actual CLI control", err)
		}
		return frame
	}
	accept := func(listener *net.UnixListener) *net.UnixConn {
		t.Helper()
		_ = listener.SetDeadline(time.Now().Add(3 * time.Second))
		conn, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal("actual CLI did not connect", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		return conn
	}
	sendConnect(&ticket)
	exchange := accept(ownerListener)
	exchangeFrame, err := ipc.Read(exchange)
	if err != nil {
		t.Fatal(err)
	}
	exchanged, err := contract.DecodeOwnerTransportTicket(exchangeFrame.Payload)
	if err != nil || exchanged != ticket || exchangeFrame.Kind != ipc.KindOwnerTransportExchange {
		t.Fatal("actual CLI changed ticket", err)
	}
	raw, _ = json.Marshal(contract.OwnerTransportCredential{LaunchToken: "fixture-private-launch-token"})
	if err = ipc.Write(exchange, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: ticket.Pending.RequestID, Epoch: 7, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	_ = exchange.Close()
	hello := func() *net.UnixConn {
		t.Helper()
		conn := accept(hostListener)
		frame, err := ipc.Read(conn)
		if err != nil {
			t.Fatal(err)
		}
		var h contract.HostHello
		if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &h); err != nil || frame.Kind != ipc.KindHostHello || h.PID != identity.PID || h.Birth != identity.Birth || h.Executable != binary || h.LaunchToken != "fixture-private-launch-token" || h.OriginPID != os.Getpid() || h.OriginBirth != parentBirth {
			t.Fatal("actual ordinary HostHello mismatch", err)
		}
		raw, _ := json.Marshal(contract.OwnerHostPending{Version: 1, HostID: scope.ProducerID, CoordinatorEpoch: 7, CoordinatorPublicKey: bootstrap.CoordinatorPublicKey, OwnerID: scope.OwnerID})
		if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: scope.LaunchID, Epoch: 7, Payload: raw}); err != nil {
			t.Fatal(err)
		}
		pending := readControl()
		if pending.Kind != ipc.KindOwnerHostPending || !bytes.Equal(pending.Payload, raw) {
			t.Fatal("actual pending changed")
		}
		return conn
	}
	conn := hello()
	permit, err := contract.SignOwnerControlPermit(contract.OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: scope.OwnerID, ScopeSHA256: scopeHash, OwnerIdentitySHA256: ownerHash, CoordinatorKeyID: keyID, CoordinatorEpoch: 7, ControlRevision: 2, ExpectedControlRevision: 1, OldHost: &old, NewHost: identity, RequestID: "attach", RequestSHA256: strings.Repeat("d", 64), PermitID: "permit-2"}, key)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "terminal", RequestID: "terminal", Body: json.RawMessage(`{"reason":"host_retired"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(command)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: command.RequestID, Epoch: 7, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	relayed := readControl()
	if relayed.Kind != ipc.KindOwnerCommand || !bytes.Equal(relayed.Payload, raw) {
		t.Fatal("actual signed command changed")
	}
	_ = conn.Close()
	offline := readControl()
	notice, err := contract.DecodeOwnerTransportControl(offline.Payload)
	if err != nil || notice.Action != "offline" {
		t.Fatal("actual transport did not report offline", err)
	}
	_ = hostListener.SetDeadline(time.Now().Add(100 * time.Millisecond))
	if extra, err := hostListener.AcceptUnix(); err == nil {
		extra.Close()
		t.Fatal("actual CLI reconnected without owner")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
	sendConnect(nil)
	second := hello()
	defer second.Close()
	_ = control.Close()
	select {
	case err = <-done:
		waited = true
		if err != nil || child.ProcessState == nil || child.ProcessState.ExitCode() != 0 {
			t.Fatalf("actual original Wait: %v output=%s", err, output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("actual CLI did not exit after owner EOF")
	}
	if bytes.Contains(output.Bytes(), []byte("fixture-private-launch-token")) {
		t.Fatal("private transport credential leaked")
	}
	t.Logf("actual CLI transport: pid=%d birth=%s executable_sha256=%s inherited_FD3_FD4=true signed_relay=true owner_controlled_reconnect=true original_Wait=0 forced_cleanup=false", identity.PID, identity.Birth, digest)
}
