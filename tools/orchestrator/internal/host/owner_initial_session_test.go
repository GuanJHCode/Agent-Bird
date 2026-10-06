package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestOwnerTransportInitialSessionRetainsConnectionAndCredential(t *testing.T) {
	e, bound, key := ownerEnrollmentFixture(t)
	b := contract.OwnerTransportBootstrap{Version: 1, StateDir: e.bootstrap.StateDir, Intent: e.bootstrap.Prepared.Intent, Owner: bound.Binding.Owner, CoordinatorPublicKey: e.bootstrap.Prepared.CoordinatorPublicKey}
	// Short socket path is a fixture constraint, not relaxed bootstrap validation.
	state, err := os.MkdirTemp("", "owner-initial-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(state)
	b.StateDir = state
	listener, err := net.Listen("unix", filepath.Join(state, "coordinator.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	pending := contract.OwnerHostPending{Version: 1, HostID: b.Intent.Scope.ProducerID, CoordinatorEpoch: bound.Permit.CoordinatorEpoch, CoordinatorPublicKey: b.CoordinatorPublicKey}
	payload, _ := json.Marshal(pending)
	frame := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: b.Intent.Scope.LaunchID, Epoch: pending.CoordinatorEpoch, Payload: payload}
	control, owner := ownerRuntimeControlPair(t)
	connection, coordinator := net.Pipe()
	defer coordinator.Close()
	if err = connection.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	token := "initial-credential-only-in-transport"
	go func() {
		done <- runOwnerTransportLoopWithInitial(ctx, b, b.Intent.FirstHost, control, &ownerTransportInitialSession{connection: connection, pending: frame, token: token})
	}()
	// The real Coordinator emits this informational acknowledgement before
	// signed commands after Activate. It is not execution authority for FD4.
	ready, _ := json.Marshal(contract.HostReady{HostID: b.Intent.Scope.ProducerID, CoordinatorEpoch: bound.Permit.CoordinatorEpoch})
	if err := ipc.Write(coordinator, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: b.Intent.Scope.LaunchID, Epoch: bound.Permit.CoordinatorEpoch, Payload: ready}); err != nil {
		t.Fatal(err)
	}
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: bound.Permit, Kind: "terminal", RequestID: "command", Body: json.RawMessage(`{"reason":"host_retired"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	if err = ipc.Write(coordinator, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: command.RequestID, Epoch: bound.Permit.CoordinatorEpoch, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	_ = owner.SetReadDeadline(time.Now().Add(time.Second))
	forwarded, err := ipc.Read(owner)
	if err != nil || forwarded.Kind != ipc.KindOwnerCommand || !bytes.Equal(forwarded.Payload, raw) || bytes.Contains(forwarded.Payload, []byte(token)) {
		t.Fatal("initial connection was redialed or credential crossed owner control", err)
	}
	coordinator.Close()
	lost, err := ipc.Read(owner)
	if err != nil || lost.Kind != ipc.KindOwnerTransportControl {
		t.Fatal("initial EOF lost", err)
	}
	// Only an explicit owner request can reconnect; no ticket exchange is needed
	// because this same initial transport retains its original LaunchToken.
	connect, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect"})
	if err = ipc.Write(owner, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "reconnect", Payload: connect}); err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetDeadline(time.Now().Add(time.Second))
	resumed, err := listener.Accept()
	if err != nil {
		t.Fatal("initial reconnect tried owner ticket exchange", err)
	}
	defer resumed.Close()
	resumed.SetDeadline(time.Now().Add(time.Second))
	hello, err := ipc.Read(resumed)
	var h contract.HostHello
	if err != nil || hello.Kind != ipc.KindHostHello || json.Unmarshal(hello.Payload, &h) != nil || h.LaunchToken != token || h.PID != b.Intent.FirstHost.PID || h.Birth != b.Intent.FirstHost.Birth {
		t.Fatal("reconnect lost exact first transport/token", err)
	}
	pending.OwnerID = b.Intent.Scope.OwnerID
	frame.Payload, _ = json.Marshal(pending)
	if err = ipc.Write(resumed, frame); err != nil {
		t.Fatal(err)
	}
	if got, err := ipc.Read(owner); err != nil || got.Kind != ipc.KindOwnerHostPending || bytes.Contains(got.Payload, []byte(token)) {
		t.Fatal("pending did not reach original owner", err)
	}
	owner.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial relay did not exit")
	}
}

func TestInitialOwnerBoundPinsActualChildAndCompleteSignedReply(t *testing.T) {
	e, bound, _ := ownerEnrollmentFixture(t)
	child := contract.OriginalOwnerTransport{PID: bound.Binding.Owner.PID, Birth: bound.Binding.Owner.Birth}
	raw, _ := json.Marshal(bound)
	frame := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOriginalOwnerBound, RequestID: e.bootstrap.Prepared.Intent.RequestID, Epoch: bound.Permit.CoordinatorEpoch, Payload: raw}
	got, err := verifyInitialOwnerBound(e.bootstrap, child, frame)
	if err != nil || got.Binding != bound.Binding {
		t.Fatal(err)
	}
	for _, scenario := range []string{"pid", "birth", "id", "epoch", "kind", "missing_field", "signature"} {
		t.Run(scenario, func(t *testing.T) {
			wrong, identity := frame, child
			switch scenario {
			case "pid":
				identity.PID++
			case "birth":
				identity.Birth += "foreign"
			case "id":
				wrong.RequestID = "other"
			case "epoch":
				wrong.Epoch++
			case "kind":
				wrong.Kind = ipc.KindResponse
			case "missing_field":
				wrong.Payload = bytes.Replace(raw, []byte(`"old_host":null,`), nil, 1)
			case "signature":
				wrong.Payload = bytes.Replace(raw, []byte(bound.Binding.Signature), []byte("00"), 1)
			}
			if _, err := verifyInitialOwnerBound(e.bootstrap, identity, wrong); !errors.Is(err, errOwnerEnrollment) {
				t.Fatal("initial transport accepted foreign/incomplete owner", err)
			}
		})
	}
}

func TestInitialTransportRejectsForeignReady(t *testing.T) {
	for _, kind := range []string{"request", "epoch", "payload_epoch", "host", "unknown_field"} {
		t.Run(kind, func(t *testing.T) {
			e, bound, _ := ownerEnrollmentFixture(t)
			b := contract.OwnerTransportBootstrap{Version: 1, StateDir: e.bootstrap.StateDir, Intent: e.bootstrap.Prepared.Intent, Owner: bound.Binding.Owner, CoordinatorPublicKey: e.bootstrap.Prepared.CoordinatorPublicKey}
			pending, _ := json.Marshal(contract.OwnerHostPending{Version: 1, HostID: b.Intent.Scope.ProducerID, CoordinatorEpoch: b.Intent.CoordinatorEpoch, CoordinatorPublicKey: b.CoordinatorPublicKey})
			initial := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: b.Intent.Scope.LaunchID, Epoch: b.Intent.CoordinatorEpoch, Payload: pending}
			control, owner := ownerRuntimeControlPair(t)
			connection, coordinator := net.Pipe()
			defer coordinator.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- runOwnerTransportLoopWithInitial(ctx, b, b.Intent.FirstHost, control, &ownerTransportInitialSession{connection, initial, "initial-token"})
			}()
			ready := contract.HostReady{HostID: b.Intent.Scope.ProducerID, CoordinatorEpoch: b.Intent.CoordinatorEpoch}
			frame := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: b.Intent.Scope.LaunchID, Epoch: b.Intent.CoordinatorEpoch}
			switch kind {
			case "request":
				frame.RequestID = "foreign"
			case "epoch":
				frame.Epoch++
			case "payload_epoch":
				ready.CoordinatorEpoch++
			case "host":
				ready.HostID = "foreign"
			}
			frame.Payload, _ = json.Marshal(ready)
			if kind == "unknown_field" {
				frame.Payload = append(frame.Payload[:len(frame.Payload)-1], []byte(`,"launch":true}`)...)
			}
			if err := ipc.Write(coordinator, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, errOwnerTransportProtocol) {
					t.Fatal("foreign readiness accepted", err)
				}
			case <-time.After(time.Second):
				t.Fatal("foreign readiness did not terminate relay")
			}
			_ = owner.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := ipc.Read(owner); err == nil {
				t.Fatal("unsigned readiness crossed owner channel")
			}
		})
	}
}

func TestOwnerTransportInitialSessionRejectsUntrustedHandoff(t *testing.T) {
	for _, scenario := range []string{"identity", "kind", "epoch", "owner", "key", "token", "nil_connection"} {
		t.Run(scenario, func(t *testing.T) {
			e, bound, _ := ownerEnrollmentFixture(t)
			b := contract.OwnerTransportBootstrap{Version: 1, StateDir: e.bootstrap.StateDir, Intent: e.bootstrap.Prepared.Intent, Owner: bound.Binding.Owner, CoordinatorPublicKey: e.bootstrap.Prepared.CoordinatorPublicKey}
			pending := contract.OwnerHostPending{Version: 1, HostID: b.Intent.Scope.ProducerID, CoordinatorEpoch: b.Intent.CoordinatorEpoch, CoordinatorPublicKey: b.CoordinatorPublicKey}
			frame := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: b.Intent.Scope.LaunchID, Epoch: pending.CoordinatorEpoch}
			identity := b.Intent.FirstHost
			connection, peer := net.Pipe()
			defer connection.Close()
			defer peer.Close()
			initial := &ownerTransportInitialSession{connection: connection, token: "token"}
			switch scenario {
			case "identity":
				identity.PID++
			case "kind":
				frame.Kind = ipc.KindResponse
			case "epoch":
				frame.Epoch++
			case "owner":
				pending.OwnerID = "foreign"
			case "key":
				pending.CoordinatorPublicKey = "foreign"
			case "token":
				initial.token = "token\x00suffix"
			case "nil_connection":
				initial.connection = nil
			}
			frame.Payload, _ = json.Marshal(pending)
			initial.pending = frame
			control, _ := ownerRuntimeControlPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := runOwnerTransportLoopWithInitial(ctx, b, identity, control, initial); !errors.Is(err, errOwnerTransportProtocol) {
				t.Fatal("untrusted initial handoff entered relay", err)
			}
		})
	}
}
