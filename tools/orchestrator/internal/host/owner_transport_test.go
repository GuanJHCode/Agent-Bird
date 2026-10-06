package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// These real Unix sockets exercise transport forwarding, not Coordinator kernel
// enrollment. Full runtime acceptance must independently perform the real Bind.
type ownerTransportLoopFixture struct {
	bootstrap                   contract.OwnerTransportBootstrap
	identity                    contract.OriginalOwnerTransport
	ticket                      contract.OwnerTransportTicket
	key                         ed25519.PrivateKey
	control                     *net.UnixConn
	ownerListener, hostListener *net.UnixListener
	done                        chan error
}

func newOwnerTransportLoopFixture(t *testing.T) *ownerTransportLoopFixture {
	t.Helper()
	b, _, _ := ownerTransportFixture(t)
	state, err := os.MkdirTemp("", "owner-transport-loop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	b.StateDir = state
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b.CoordinatorPublicKey = hex.EncodeToString(pub)
	b.Intent.CoordinatorKeyID, _ = contract.OriginalOwnerKeyID(pub)
	identity := contract.OriginalOwnerTransport{PID: os.Getpid(), Birth: b.Owner.Birth}
	scope, _ := contract.OriginalOwnerScopeSHA256(b.Intent.Scope)
	owner, _ := contract.OriginalOwnerIdentitySHA256(b.Owner)
	ticket, err := contract.SignOwnerTransportTicket(contract.OwnerTransportTicket{Version: 1, OwnerID: b.Intent.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: owner, CoordinatorKeyID: b.Intent.CoordinatorKeyID, CoordinatorEpoch: 7, Pending: contract.OwnerTransportPending{Version: 1, RecoverySlot: 1, RequestID: "replacement", ExpectedControlRevision: 1, OldHost: b.Intent.FirstHost, NewHost: identity}, Nonce: strings.Repeat("a", 64), ExpiresUnixMS: time.Now().Add(10 * time.Second).UnixMilli()}, key)
	if err != nil {
		t.Fatal(err)
	}
	ctl, file, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	child, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	f := &ownerTransportLoopFixture{bootstrap: b, identity: identity, ticket: ticket, key: key, control: ctl, done: make(chan error, 1)}
	f.ownerListener, err = net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(state, "owner.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	f.hostListener, err = net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(state, "coordinator.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ctl.Close()
		_ = child.Close()
		_ = f.ownerListener.Close()
		_ = f.hostListener.Close()
		select {
		case <-f.done:
		case <-time.After(2 * time.Second):
			t.Error("transport loop did not join")
		}
	})
	go func() { f.done <- runOwnerTransportLoop(ctx, b, identity, child.(*net.UnixConn)); close(f.done) }()
	return f
}

func (f *ownerTransportLoopFixture) request(t *testing.T, ticket *contract.OwnerTransportTicket) {
	t.Helper()
	raw, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect", Ticket: ticket})
	_ = f.control.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := ipc.Write(f.control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "connect", Payload: raw}); err != nil {
		t.Fatal(err)
	}
}
func (f *ownerTransportLoopFixture) read(t *testing.T) ipc.Envelope {
	t.Helper()
	_ = f.control.SetReadDeadline(time.Now().Add(2 * time.Second))
	v, err := ipc.Read(f.control)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func acceptOwnerTransportTest(t *testing.T, l *net.UnixListener) *net.UnixConn {
	t.Helper()
	_ = l.SetDeadline(time.Now().Add(2 * time.Second))
	conn, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	return conn
}
func (f *ownerTransportLoopFixture) exchange(t *testing.T) {
	t.Helper()
	conn := acceptOwnerTransportTest(t, f.ownerListener)
	defer conn.Close()
	request, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	got, err := contract.DecodeOwnerTransportTicket(request.Payload)
	if err != nil || got != f.ticket || request.Kind != ipc.KindOwnerTransportExchange {
		t.Fatal("ticket exchange altered proof", err)
	}
	raw, _ := json.Marshal(contract.OwnerTransportCredential{LaunchToken: "private-launch-token"})
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: f.ticket.CoordinatorEpoch, Payload: raw}); err != nil {
		t.Fatal(err)
	}
}
func (f *ownerTransportLoopFixture) hello(t *testing.T, mutate func(*contract.OwnerHostPending)) *net.UnixConn {
	t.Helper()
	conn := acceptOwnerTransportTest(t, f.hostListener)
	frame, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	var hello contract.HostHello
	if err = contract.DecodeOwnerRuntimeStrict(frame.Payload, &hello); err != nil || frame.Kind != ipc.KindHostHello || hello.LaunchToken != "private-launch-token" || hello.PID != f.identity.PID || hello.Birth != f.identity.Birth || hello.LaunchID != f.bootstrap.Intent.Scope.LaunchID {
		t.Fatal("ordinary HostHello lost credential/physical tuple", err)
	}
	pending := contract.OwnerHostPending{Version: 1, HostID: f.bootstrap.Intent.Scope.ProducerID, CoordinatorEpoch: 7, CoordinatorPublicKey: f.bootstrap.CoordinatorPublicKey, OwnerID: f.bootstrap.Intent.Scope.OwnerID}
	if mutate != nil {
		mutate(&pending)
	}
	raw, _ := json.Marshal(pending)
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: hello.LaunchID, Epoch: 7, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	return conn
}
func (f *ownerTransportLoopFixture) command(t *testing.T) ipc.Envelope {
	t.Helper()
	scope, _ := contract.OriginalOwnerScopeSHA256(f.bootstrap.Intent.Scope)
	owner, _ := contract.OriginalOwnerIdentitySHA256(f.bootstrap.Owner)
	old := f.bootstrap.Intent.FirstHost
	permit, err := contract.SignOwnerControlPermit(contract.OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: f.bootstrap.Intent.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: owner, CoordinatorKeyID: f.bootstrap.Intent.CoordinatorKeyID, CoordinatorEpoch: 7, ControlRevision: 2, ExpectedControlRevision: 1, OldHost: &old, NewHost: f.identity, RequestID: "attach", RequestSHA256: strings.Repeat("b", 64), PermitID: "permit-2"}, f.key)
	if err != nil {
		t.Fatal(err)
	}
	raw := ownerExecutionSignedLaunch(t, permit, f.key)
	return ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: "command", Epoch: 7, Payload: raw}
}

func TestOwnerTransportForwardsSignedCommandsAndWaitsForOwnerReconnect(t *testing.T) {
	f := newOwnerTransportLoopFixture(t)
	f.request(t, &f.ticket)
	f.exchange(t)
	conn := f.hello(t, nil)
	pending := f.read(t)
	if pending.Kind != ipc.KindOwnerHostPending || bytes.Contains(pending.Payload, []byte("private-launch-token")) {
		t.Fatal("pending leaked credentials or changed kind")
	}
	command := f.command(t)
	if err := ipc.Write(conn, command); err != nil {
		t.Fatal(err)
	}
	got := f.read(t)
	if got.Kind != command.Kind || got.RequestID != command.RequestID || got.Epoch != command.Epoch || !bytes.Equal(got.Payload, command.Payload) {
		t.Fatal("signed command changed in transit")
	}
	_ = conn.Close()
	offline := f.read(t)
	notice, err := contract.DecodeOwnerTransportControl(offline.Payload)
	if err != nil || offline.Kind != ipc.KindOwnerTransportControl || notice.Action != "offline" || notice.Ticket != nil {
		t.Fatal("transport loss was not a pure notification", err)
	}
	_ = f.hostListener.SetDeadline(time.Now().Add(100 * time.Millisecond))
	if c, err := f.hostListener.AcceptUnix(); err == nil {
		c.Close()
		t.Fatal("transport independently reconnected")
	} else if n, ok := err.(net.Error); !ok || !n.Timeout() {
		t.Fatal(err)
	}
	f.request(t, nil)
	reconnected := f.hello(t, nil)
	defer reconnected.Close()
	if f.read(t).Kind != ipc.KindOwnerHostPending {
		t.Fatal("explicit owner reconnect failed")
	}
	_ = f.control.Close()
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatal("owner EOF failed to terminate transport", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner EOF did not terminate transport")
	}
}

func TestOwnerTransportRejectsUnboundTicketBeforeDial(t *testing.T) {
	for _, kind := range []string{"scope", "owner", "child", "expiry", "signature"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnerTransportLoopFixture(t)
			ticket := f.ticket
			switch kind {
			case "scope":
				ticket.ScopeSHA256 = strings.Repeat("c", 64)
			case "owner":
				ticket.OwnerIdentitySHA256 = strings.Repeat("c", 64)
			case "child":
				ticket.Pending.NewHost.PID++
			case "expiry":
				ticket.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
			case "signature":
				ticket.Signature = strings.Repeat("0", 128)
			}
			if kind != "signature" {
				var err error
				ticket, err = contract.SignOwnerTransportTicket(ticket, f.key)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.request(t, &ticket)
			select {
			case err := <-f.done:
				if !errors.Is(err, errOwnerTransportProtocol) {
					t.Fatal("unbound ticket was not rejected", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("unbound ticket began network exchange")
			}
		})
	}
}

func TestOwnerTransportRejectsPendingIdentityAndUnsignedCommands(t *testing.T) {
	for _, kind := range []string{"pending_host", "pending_owner", "pending_key", "pending_epoch", "unsigned", "outer_epoch", "outer_id", "body"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnerTransportLoopFixture(t)
			f.request(t, &f.ticket)
			f.exchange(t)
			conn := f.hello(t, func(p *contract.OwnerHostPending) {
				switch kind {
				case "pending_host":
					p.HostID = "foreign"
				case "pending_owner":
					p.OwnerID = "foreign"
				case "pending_key":
					p.CoordinatorPublicKey = strings.Repeat("c", 64)
				case "pending_epoch":
					p.CoordinatorEpoch++
				}
			})
			if !strings.HasPrefix(kind, "pending_") {
				_ = f.read(t)
				command := f.command(t)
				switch kind {
				case "unsigned":
					command.Kind = ipc.KindLaunch
				case "outer_epoch":
					command.Epoch++
				case "outer_id":
					command.RequestID = "foreign"
				case "body":
					command.Payload = bytes.Replace(command.Payload, []byte(`"task_id":"task"`), []byte(`"task_id":"foreign"`), 1)
				}
				if err := ipc.Write(conn, command); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-f.done:
				if !errors.Is(err, errOwnerTransportProtocol) {
					t.Fatal("untrusted frame was not rejected", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("untrusted frame entered owner control channel")
			}
		})
	}
}
