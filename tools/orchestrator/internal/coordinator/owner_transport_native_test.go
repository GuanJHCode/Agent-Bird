package coordinator

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The transport is a real child of the live signing owner. Only this process
// receives the token; its parent sees public identity and protocol outcomes.
func runOwnerTransportTestChild(t *testing.T, cfg ownerChildConfig, dec *json.Decoder, enc *json.Encoder) {
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := contract.OriginalOwnerIdentity{PID: os.Getpid(), Birth: birth}
	var token string
	var transport net.Conn
	defer func() {
		if transport != nil {
			_ = transport.Close()
		}
	}()
	for {
		var step ownerChildStep
		if err := dec.Decode(&step); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		var body json.RawMessage
		var callErr error
		switch step.Kind {
		case "identity":
		case "exchange":
			conn, err := net.Dial("unix", cfg.Socket)
			if err != nil {
				callErr = err
				break
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportExchange, RequestID: step.ID, Payload: step.Body})
			if step.DropReply {
				_ = conn.Close()
				callErr = err
				break
			}
			var reply ipc.Envelope
			if err == nil {
				reply, err = ipc.Read(conn)
			}
			_ = conn.Close()
			if err != nil {
				callErr = err
				break
			}
			if reply.Kind == ipc.KindError {
				var v ErrorResponse
				_ = json.Unmarshal(reply.Payload, &v)
				callErr = errors.New(v.Error)
				break
			}
			var value contract.OwnerTransportCredential
			if reply.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(reply.Payload, &value) != nil || value.LaunchToken == "" {
				callErr = errors.New("invalid_credential")
				break
			}
			if token != "" && token != value.LaunchToken {
				t.Fatal("escrow credential drift")
			}
			token = value.LaunchToken
		case "hello":
			if transport != nil {
				_ = transport.Close()
			}
			transport, callErr = net.Dial("unix", filepath.Join(filepath.Dir(cfg.Socket), "coordinator.sock"))
			if callErr != nil {
				break
			}
			_ = transport.SetDeadline(time.Now().Add(3 * time.Second))
			scope := cfg.Prepared.Intent.Scope
			hello := contract.HostHello{LaunchID: scope.LaunchID, LaunchToken: token, OriginContextID: scope.OriginalOriginContextID, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth, HostGeneration: scope.HostGeneration, PID: identity.PID, Birth: identity.Birth, Executable: scope.Executable}
			raw, _ := json.Marshal(hello)
			callErr = ipc.Write(transport, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: step.ID, Payload: raw})
			if callErr != nil {
				break
			}
			fallthrough
		case "read":
			if transport == nil {
				callErr = errors.New("missing_transport")
				break
			}
			_ = transport.SetDeadline(time.Now().Add(3 * time.Second))
			reply, err := ipc.Read(transport)
			callErr = err
			if err == nil {
				body, _ = json.Marshal(reply)
			}
		default:
			t.Fatal("unknown transport operation")
		}
		if err := enc.Encode(ownerChildReply{Error: errorCode(callErr), Body: body, Owner: identity}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOriginalOwnerNativeTransportTicketAttachAndReplay(t *testing.T) {
	for _, scenario := range []string{"replacement", "replay_reconnect", "replay_restart", "foreign_host", "successor_replacement"} {
		t.Run(scenario, func(t *testing.T) { testOriginalOwnerNativeTransportTicket(t, scenario) })
	}
}

func testOriginalOwnerNativeTransportTicket(t *testing.T, scenario string) {
	s, hello, stopServe := ownerRuntimeFixtureWithCancel(t)
	if scenario == "successor_replacement" {
		stopServe()
		_ = s.Close()
		s, hello = successorTransportFixture(t)
	}
	ctx := context.Background()
	// First-bind is a Store fixture only. Subsequent ticket/credential/Hello/
	// Attach are real private RPCs with fresh kernel peers and a real child tree.
	oldStep, oldFinish := startOwnerRPCChild(t, ownerChildConfig{})
	oldIdentity := oldStep(ownerChildStep{Kind: "identity"}).Owner
	old := hello
	old.PID, old.Birth = oldIdentity.PID, oldIdentity.Birth
	digest, err := process.ExecutableDigest(ctx, hello.Executable)
	if err != nil {
		t.Fatal(err)
	}
	var scope contract.OriginalOwnerScope
	var admission store.SuccessorOwnerAdmission
	if scenario == "successor_replacement" {
		admission, err = s.db.ResolveSuccessorOwnerAdmission(ctx, old, s.epoch, digest)
		scope = admission.Scope
	} else {
		if _, err = s.db.RegisterHost(ctx, old, s.epoch); err != nil {
			t.Fatal(err)
		}
		scope, err = s.db.PreviewOriginalOwnerScope(ctx, old, s.epoch, digest)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ownerKeys.saveLaunchCredential(s.stateDir, scope, hello.LaunchToken); err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.Repeat("a", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(bootstrap)
	spec := store.PrepareOriginalOwnerSpec{Host: old, Epoch: s.epoch, ExecutableSHA256: digest, BootstrapID: "transport-fixture", BootstrapTokenSHA256: hash, CoordinatorKeyID: s.ownerKeys.id, RequestID: "transport-intent", RequestSHA256: strings.Repeat("b", 64), StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()}
	var intent contract.OriginalOwnerIntent
	if scenario == "successor_replacement" {
		intent, err = s.db.PrepareSuccessorOwner(ctx, store.PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: spec, AuthorizationID: admission.AuthorizationID})
	} else {
		intent, err = s.db.PrepareOriginalOwner(ctx, spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	prepared := contract.OwnerPrepared{Intent: intent, CoordinatorPublicKey: hex.EncodeToString(s.ownerPublicKey()), RPCSocketPath: s.OwnerSocketPath()}
	step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: s.OwnerSocketPath(), Prepared: prepared, BootstrapToken: bootstrap})
	owner := step(ownerChildStep{Kind: "identity"}).Owner
	auth := store.OriginalOwnerAuthentication{Owner: owner, OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth, ExecutableSHA256: digest}
	bound, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: bootstrap, Owner: owner, FirstHost: intent.FirstHost, CoordinatorKeyID: s.ownerKeys.id}, Host: old, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(1)
	if got := step(ownerChildStep{Kind: "state", ID: "initial-state", Revision: &revision}); got.Error != "" {
		t.Fatal(got.Error)
	}
	child := step(ownerChildStep{Kind: "spawn_transport"}).Owner
	pending := contract.OwnerTransportPending{Version: 1, RecoverySlot: 1, RequestID: "replace-1", ExpectedControlRevision: 1, OldHost: bound.Permit.NewHost, NewHost: contract.OriginalOwnerTransport{PID: child.PID, Birth: child.Birth}}
	raw, _ := json.Marshal(pending)
	if got := step(ownerChildStep{Kind: "ticket", ID: pending.RequestID, Body: raw}); got.Error == "" {
		t.Fatal("live old transport authorized replacement")
	}
	oldFinish()
	if dead, err := hostIdentityExited(old.PID, old.Birth); err != nil || !dead {
		t.Fatal("old transport death missing", err)
	}

	if scenario == "foreign_host" {
		other, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "other-run", ControllerThread: "other-controller", PlanRevision: 1, OriginContextID: "other-origin", OriginPID: hello.OriginPID, OriginBirth: hello.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: "other-origin", HostGeneration: "other-gen", Executable: hello.Executable}, Tasks: []store.TaskSpec{{ID: "other-task", RunID: "other-run", MaxAttempts: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		otherHello := hello
		otherHello.LaunchID, otherHello.LaunchToken = other.LaunchID, other.LaunchToken
		otherHello.OriginContextID, otherHello.HostGeneration = "other-origin", "other-gen"
		otherHello.PID, otherHello.Birth = child.PID, child.Birth
		if _, err = s.db.RegisterHost(ctx, otherHello, s.epoch); err != nil {
			t.Fatal(err)
		}
		got := step(ownerChildStep{Kind: "ticket", ID: pending.RequestID, Body: raw})
		finish()
		if got.Error == "" {
			t.Fatal("foreign Host child received transport ticket")
		}
		return
	}
	getTicket := func() contract.OwnerTransportTicket {
		t.Helper()
		got := step(ownerChildStep{Kind: "ticket", ID: pending.RequestID, Body: raw})
		if got.Error != "" {
			t.Fatal("ticket", got.Error)
		}
		var ticket contract.OwnerTransportTicket
		if err := contract.DecodeOwnerRuntimeStrict(got.Body, &ticket); err != nil || contract.VerifyOwnerTransportTicket(ticket, s.ownerPublicKey(), s.epoch, time.Now().UnixMilli()) != nil {
			t.Fatal("invalid ticket", err)
		}
		return ticket
	}
	ticket := getTicket()
	if getTicket() != ticket {
		t.Fatal("same pending changed ticket before expiry")
	}
	for _, mutation := range []string{"slot", "birth", "request"} {
		bad := pending
		switch mutation {
		case "slot":
			bad.RecoverySlot++
		case "birth":
			bad.NewHost.Birth += "x"
		case "request":
			bad.RequestID += "x"
		}
		body, _ := json.Marshal(bad)
		if got := step(ownerChildStep{Kind: "ticket", ID: bad.RequestID, Body: body}); got.Error == "" {
			t.Fatal("pending drift accepted", mutation)
		}
	}
	childCall := func(c ownerChildStep) ownerChildReply {
		t.Helper()
		body, _ := json.Marshal(c)
		return step(ownerChildStep{Kind: "transport", Body: body})
	}
	ticketRaw, _ := json.Marshal(ticket)
	foreign, err := net.Dial("unix", s.OwnerSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	_ = foreign.SetDeadline(time.Now().Add(time.Second))
	_ = ipc.Write(foreign, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportExchange, RequestID: "foreign", Payload: ticketRaw})
	if response, err := ipc.Read(foreign); err == nil {
		t.Fatal("foreign peer received credential", response.Kind)
	}
	_ = foreign.Close()

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	stopServe()
	sPending, err := NewServer(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	pendingCtx, pendingCancel := context.WithCancel(ctx)
	defer pendingCancel()
	defer sPending.Close()
	go func() { _ = sPending.Serve(pendingCtx) }()
	s = sPending
	stopServe = pendingCancel
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "previous-epoch", Body: ticketRaw}); got.Error == "" {
		t.Fatal("previous epoch pending ticket redeemed")
	}
	priorTicket := ticket
	ticket = getTicket()
	if ticket.Pending != priorTicket.Pending || ticket.CoordinatorEpoch <= priorTicket.CoordinatorEpoch {
		t.Fatal("epoch reissue changed pending child")
	}
	ticketRaw, _ = json.Marshal(ticket)
	tampered := ticket
	tampered.Pending.NewHost.Birth += "x"
	badRaw, _ := json.Marshal(tampered)
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "tampered", Body: badRaw}); got.Error == "" {
		t.Fatal("tampered ticket redeemed")
	}
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "exchange", Body: ticketRaw, DropReply: true}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "exchange", Body: ticketRaw}); got.Error != "" {
		t.Fatal("exchange ACK replay", got.Error)
	}

	// Model wall-clock expiry without waiting ten seconds: preserve the exact
	// pending slot and redemption state, then prove rejection has no writes.
	if scenario == "replacement" {
		expired := ticket
		expired.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
		expired, err = contract.SignOwnerTransportTicket(expired, s.ownerKeys.sign)
		if err != nil {
			t.Fatal(err)
		}
		s.hostRegistrationMu.Lock()
		s.mu.Lock()
		s.ownerTickets[ticket.OwnerID].ticket = expired
		s.mu.Unlock()
		s.hostRegistrationMu.Unlock()
		expiredRaw, _ := json.Marshal(expired)
		snapshot := hostHelloDBSnapshot(t, s)
		retained, _ := s.ownerTicketSnapshot(ticket.OwnerID)
		if got := childCall(ownerChildStep{Kind: "exchange", ID: "expired", Body: expiredRaw}); got.Error == "" {
			t.Fatal("expired ticket redeemed")
		}
		rejected := childCall(ownerChildStep{Kind: "hello", ID: "expired-hello"})
		var frame ipc.Envelope
		if rejected.Error != "" || json.Unmarshal(rejected.Body, &frame) != nil || frame.Kind != ipc.KindError {
			t.Fatal("expired ticket admitted HostHello", rejected.Error, frame.Kind)
		}
		after, _ := s.ownerTicketSnapshot(ticket.OwnerID)
		if hostHelloDBSnapshot(t, s) != snapshot || after != retained {
			t.Fatal("expired admission mutated DB or recovery slot")
		}
		ticket = getTicket()
		ticketRaw, _ = json.Marshal(ticket)
		if got := childCall(ownerChildStep{Kind: "exchange", ID: "reissued-expiry", Body: ticketRaw}); got.Error != "" {
			t.Fatal("same child expiry reissue", got.Error)
		}
	}
	before := hostHelloDBSnapshot(t, s)
	readFrame := func(got ownerChildReply, want ipc.Kind) {
		t.Helper()
		var frame ipc.Envelope
		if got.Error != "" || json.Unmarshal(got.Body, &frame) != nil || frame.Kind != want {
			var failure ErrorResponse
			if frame.Kind == ipc.KindError {
				_ = json.Unmarshal(frame.Payload, &failure)
			}
			t.Fatalf("transport got %s %s (%s) want %s", got.Error, frame.Kind, failure.Error, want)
		}
	}
	readFrame(childCall(ownerChildStep{Kind: "hello", ID: "replacement-hello"}), ipc.KindOwnerHostPending)
	if hostHelloDBSnapshot(t, s) != before {
		t.Fatal("pending HostHello changed durable tuple/segment")
	}
	attach := contract.OwnerAttachRequest{ExpectedControlRevision: 1, OldHost: pending.OldHost, NewHost: pending.NewHost, Pending: &pending}
	attachRaw, _ := json.Marshal(attach)
	if got := step(ownerChildStep{Kind: "attach", ID: pending.RequestID, Body: attachRaw, DropReply: true}); got.Error != "" {
		t.Fatal(got.Error)
	}

	if scenario == "replay_reconnect" || scenario == "replay_restart" {
		// Wait for the lost-ACK transaction before tearing down its session/epoch.
		var saved contract.OwnerRuntimeState
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			saved, err = s.db.OriginalOwnerControl(ctx, intent.Scope.OwnerID, s.ownerPublicKey())
			if err == nil && saved.Permit.ControlRevision == 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil || saved.Permit.ControlRevision != 2 {
			t.Fatal("lost Attach did not commit", err)
		}
		if scenario == "replay_restart" {
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			stopServe()
			next, err := NewServer(s.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			nextCtx, nextCancel := context.WithCancel(ctx)
			defer nextCancel()
			defer next.Close()
			go func() { _ = next.Serve(nextCtx) }()
			s = next
			stopServe = nextCancel
		}
		readFrame(childCall(ownerChildStep{Kind: "hello", ID: "lost-attach-reconnect"}), ipc.KindOwnerHostPending)
		snapshot := hostHelloDBSnapshot(t, s)
		got := step(ownerChildStep{Kind: "attach", ID: pending.RequestID, Body: attachRaw})

		// A signed owner request with drift still cannot reuse historical authority.
		if got.Error == "" {
			oldRevision := uint64(1)
			for _, drift := range []string{"request", "body"} {
				id, body := pending.RequestID, attachRaw
				if drift == "request" {
					id += "-drift"
				} else {
					changed := attach
					changedPending := pending
					changedPending.RecoverySlot++
					changed.Pending = &changedPending
					body, _ = json.Marshal(changed)
				}
				rejected := step(ownerChildStep{Kind: "attach", ID: id, Body: body, Revision: &oldRevision})
				if rejected.Error == "" {
					t.Fatal("historical Attach drift accepted", drift)
				}
			}
		}
		after, inspectErr := s.db.OriginalOwnerControl(ctx, intent.Scope.OwnerID, s.ownerPublicKey())
		unchanged := hostHelloDBSnapshot(t, s) == snapshot
		finish()
		if got.Error != "" {
			t.Fatal("durable Attach exact replay rejected", got.Error)
		}
		var replayed contract.OwnerControlPermit
		savedBytes, _ := json.Marshal(saved.Permit)
		afterBytes, _ := json.Marshal(after.Permit)
		if contract.DecodeOwnerRuntimeStrict(got.Body, &replayed) != nil || !bytes.Equal(got.Body, savedBytes) || inspectErr != nil || !bytes.Equal(afterBytes, savedBytes) || !unchanged {
			t.Fatal("historical replay drifted permit or state", inspectErr)
		}
		return
	}
	if got := step(ownerChildStep{Kind: "attach", ID: pending.RequestID, Body: attachRaw}); got.Error != "" {
		t.Fatal("Attach ACK replay", got.Error)
	}
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "after-attach", Body: ticketRaw}); got.Error == "" {
		t.Fatal("attached ticket retained token authority")
	}
	for _, kind := range []string{"applied", "activate"} {
		if got := step(ownerChildStep{Kind: kind, ID: kind + "-replacement"}); got.Error != "" {
			t.Fatal(kind, got.Error)
		}
	}
	readFrame(childCall(ownerChildStep{Kind: "read"}), ipc.KindHostReady)
	if scenario == "successor_replacement" {
		// This scenario proves selector-free transport replacement, not task
		// Resume: its synthetic interrupted predecessor has no prior attempt.
		// Readiness and exact current transport still require Applied/Activate.
		current, err := s.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
		if err != nil || current.CurrentHost == nil || *current.CurrentHost != pending.NewHost || current.ControlRevision != 2 || current.AppliedControlRevision != 2 || current.Intent.Scope.Incarnation != 2 {
			t.Fatal("successor replacement did not reach exact applied transport", current, err)
		}
	} else {
		readFrame(childCall(ownerChildStep{Kind: "read"}), ipc.KindOwnerCommand)
	}
	// Hard Close precedes Serve cancellation to model a crash, not signed
	// normal shutdown (which deliberately fences this exact transport forever).
	// Current child reconnects from Store authority even after epoch-local ticket
	// memory is lost. It must renew the permit before regaining readiness.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	stopServe()
	s2, err := NewServer(s.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	c2, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s2.Close()
	go func() { _ = s2.Serve(c2) }()
	s = s2
	readFrame(childCall(ownerChildStep{Kind: "hello", ID: "after-restart"}), ipc.KindOwnerHostPending)
	if got := childCall(ownerChildStep{Kind: "exchange", ID: "old-epoch", Body: ticketRaw}); got.Error == "" {
		t.Fatal("old-epoch ticket redeemed")
	}
	attach = contract.OwnerAttachRequest{ExpectedControlRevision: 2, OldHost: pending.NewHost, NewHost: pending.NewHost}
	attachRaw, _ = json.Marshal(attach)
	if got := step(ownerChildStep{Kind: "attach", ID: "renew-current", Body: attachRaw}); got.Error != "" {
		t.Fatal("renew", got.Error)
	}
	for _, kind := range []string{"applied", "activate"} {
		if got := step(ownerChildStep{Kind: kind, ID: kind + "-renew"}); got.Error != "" {
			t.Fatal(kind, got.Error)
		}
	}
	readFrame(childCall(ownerChildStep{Kind: "read"}), ipc.KindHostReady)
	finish()
}
