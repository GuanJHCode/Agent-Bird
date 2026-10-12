package coordinator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The predecessor and death observations are synthetic trusted Store setup.
// Successor Hello/Prepare use a real socket and the test's kernel peer identity.
func successorTransportFixture(t *testing.T) (*Server, contract.HostHello) {
	return successorTransportFixtureWithPrior(t, nil)
}

func successorTransportFixtureWithPrior(t *testing.T, prior *contract.OriginalOwnerTransport) (*Server, contract.HostHello) {
	t.Helper()
	s, next := ownerRuntimeFixture(t)
	ctx := context.Background()
	old := next
	old.PID, old.Birth = 9, "synthetic-predecessor-host"
	if prior != nil {
		old.PID, old.Birth = prior.PID, prior.Birth
	}
	if _, err := s.db.RegisterHost(ctx, old, s.epoch); err != nil {
		t.Fatal(err)
	}
	pin, err := process.ExecutableDigest(ctx, old.Executable)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("b", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	i, err := s.db.PrepareOriginalOwner(ctx, store.PrepareOriginalOwnerSpec{Host: old, Epoch: s.epoch, ExecutableSHA256: pin, BootstrapID: "prior-bootstrap", BootstrapTokenSHA256: hash, CoordinatorKeyID: s.ownerKeys.id, RequestID: "prior-prepare", RequestSHA256: strings.Repeat("c", 64), StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("s", 32)))
	owner := contract.OriginalOwnerIdentity{PID: 11, Birth: "synthetic-prior-owner", PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	auth := store.OriginalOwnerAuthentication{Owner: owner, OriginPID: old.OriginPID, OriginBirth: old.OriginBirth, ExecutableSHA256: pin}
	b, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: i.Scope, BootstrapID: i.BootstrapID, BootstrapToken: token, Owner: owner, FirstHost: i.FirstHost, CoordinatorKeyID: i.CoordinatorKeyID}, Host: old, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: b.Permit, Reconciled: contract.HostReconciled{ProducerID: old.LaunchID}}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.CommitOriginalOwnerApplied(ctx, store.OriginalOwnerAppliedSpec{Applied: applied, Host: old, Epoch: s.epoch, Authentication: auth}, s.ownerPublicKey()); err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	var control string
	// Install a known control capability only inside this isolated fixture.
	control = strings.Repeat("d", 64)
	controlHash := sha256.Sum256([]byte(control))
	if _, err = external.Exec(`UPDATE tasks SET status='interrupted' WHERE id='owner-runtime-task'; UPDATE run_control SET token_hash=? WHERE run_id='owner-runtime-run'`, hex.EncodeToString(controlHash[:])); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r := store.OriginalOwnerResumeRequest{TaskID: "owner-runtime-task", ControllerThread: "controller", ControlToken: control, WorkRevision: 1, CommandID: "successor-command"}
	_, err = s.db.ResumeOriginalOwner(ctx, r, store.OriginalOwnerSuccessionObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: owner.PID, Birth: owner.Birth}, Host: contract.OriginalOwnerTransport{PID: old.PID, Birth: old.Birth}, Origin: store.RunOwnerBinding{OriginContextID: old.OriginContextID, OriginPID: old.OriginPID, OriginBirth: old.OriginBirth}, Epoch: s.epoch, ExecutableSHA256: pin, OwnerExitedAt: now, HostExitedAt: now}, s.ownerPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	next.ResumeCommandID = r.CommandID
	return s, next
}

func TestSuccessorPresenceReusedIdentityIsUnknown(t *testing.T) {
	pid, birth, _ := testHostIdentity(t)
	// A live kernel PID with an intentionally wrong recorded birth exercises
	// IdentityReused; this is not a claim of having induced OS PID recycling.
	s, h := successorTransportFixtureWithPrior(t, &contract.OriginalOwnerTransport{PID: pid, Birth: birth + "-stale"})
	body, _ := json.Marshal(HostStatusRequest{LaunchID: h.LaunchID, LaunchToken: h.LaunchToken})
	response, err := ipc.Call(context.Background(), s.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostStatus, RequestID: "reused-presence", Payload: body})
	var presence contract.HostPresenceResponse
	if err != nil || response.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(response.Payload, &presence) != nil {
		t.Fatal(response, err)
	}
	if presence.Presence != "unknown" || presence.OwnerPhase != "succession_pending" {
		t.Fatal("reused birth authorized cold startup", presence)
	}
}

func successorTransportConnect(t *testing.T, s *Server, hello contract.HostHello) (net.Conn, ipc.Envelope) {
	t.Helper()
	c, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	body, _ := json.Marshal(hello)
	if err = ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "successor-host", Payload: body}); err != nil {
		t.Fatal(err)
	}
	r, err := ipc.Read(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestSuccessorTransportPublishesOnlyAfterPrepare(t *testing.T) {
	s, h := successorTransportFixture(t)
	ctx := context.Background()
	before, err := s.db.HostBinding(ctx, h.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	c, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal("successor was not privately admitted", r.Kind, string(r.Payload))
	}
	after, err := s.db.HostBinding(ctx, h.LaunchID)
	if err != nil || after != before {
		t.Fatal("Hello published physical Host", after, err)
	}
	if _, err = s.db.CurrentOriginalOwnerForLaunch(ctx, h.LaunchID); err != store.ErrNotFound {
		t.Fatal("Hello consumed authorization", err)
	}
	if _, err = s.db.ClaimReady(ctx, h.LaunchID, s.epoch, 1); err != store.ErrHostRejected {
		t.Fatal("pending successor dispatched", err)
	}
	token := strings.Repeat("e", 64)
	hash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	body, _ := json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "successor-bootstrap", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	frame := ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "successor-prepare", Epoch: s.epoch, Payload: body}
	if err = ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	r, err = ipc.Read(c)
	if err != nil || r.Kind != ipc.KindResponse {
		t.Fatal("Prepare rejected", r.Kind, string(r.Payload), err)
	}
	var prepared contract.OwnerPrepared
	if err = contract.DecodeOwnerRuntimeStrict(r.Payload, &prepared); err != nil || prepared.Intent.Scope.Incarnation != 2 {
		t.Fatal("wrong successor", prepared, err)
	}
	after, err = s.db.HostBinding(ctx, h.LaunchID)
	if err != nil || after.PID != h.PID || after.Birth != h.Birth {
		t.Fatal("Prepare did not publish exact physical Host", after, err)
	}
	if err = ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	again, err := ipc.Read(c)
	if err != nil || string(again.Payload) != string(r.Payload) {
		t.Fatal("Prepare replay drift", again, err)
	}
}

func waitSuccessorTransportState(t *testing.T, s *Server, launch string, wantPending, wantActive bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		p, a := s.successorTransports[launch] != nil, s.ownerTransports[launch] != nil
		s.mu.Unlock()
		if p == wantPending && a == wantActive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("transport state pending=%v active=%v", p, a)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func successorPrepareFrame(s *Server) ipc.Envelope {
	hash, _ := contract.OriginalOwnerBootstrapSHA256(strings.Repeat("e", 64))
	body, _ := json.Marshal(contract.OwnerPrepareRequest{Version: 1, BootstrapID: "successor-bootstrap", BootstrapTokenSHA256: hash, StartupDeadlineUnixMS: time.Now().Add(20 * time.Second).UnixMilli()})
	return ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerPrepare, RequestID: "successor-prepare", Epoch: s.epoch, Payload: body}
}

func TestSuccessorTransportDisconnectDoesNotFenceOrConsume(t *testing.T) {
	s, h := successorTransportFixture(t)
	before, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	c, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	waitSuccessorTransportState(t, s, h.LaunchID, true, false)
	_ = c.Close()
	waitSuccessorTransportState(t, s, h.LaunchID, false, false)
	after, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil || before != after {
		t.Fatal("pending EOF changed physical Host", after, err)
	}
	p, err := s.db.InspectHostPresence(context.Background(), h.LaunchID, h.LaunchToken)
	if err != nil || p.Status != "offline" || p.OwnerPhase != "succession_pending" {
		t.Fatal("pending EOF fenced/published state", p, err)
	}
}

func TestSuccessorTransportOldEOFKeepsReplacementPending(t *testing.T) {
	s, h := successorTransportFixture(t)
	c1, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	s.mu.Lock()
	old := s.successorTransports[h.LaunchID]
	s.mu.Unlock()
	c2, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	_ = c1.Close()
	s.disconnectOriginalOwnerTransport(old)
	s.mu.Lock()
	fresh := s.successorTransports[h.LaunchID]
	active := s.ownerTransports[h.LaunchID]
	s.mu.Unlock()
	if fresh == nil || fresh == old || active != nil {
		t.Fatal("old EOF removed new pending session")
	}
	_ = c2.Close()
	waitSuccessorTransportState(t, s, h.LaunchID, false, false)
}

func TestSuccessorTransportPrepareFailureDoesNotPublish(t *testing.T) {
	s, h := successorTransportFixture(t)
	c, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	before, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	external, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.Exec(`CREATE TRIGGER fail_successor_prepare BEFORE INSERT ON execution_owners WHEN NEW.incarnation=2 BEGIN SELECT RAISE(ABORT,'injected_prepare_failure'); END`); err != nil {
		t.Fatal(err)
	}
	frame := successorPrepareFrame(s)
	if err = ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	r, err = ipc.Read(c)
	if err != nil || r.Kind != ipc.KindError {
		t.Fatal("failed Prepare reported success", r, err)
	}
	waitSuccessorTransportState(t, s, h.LaunchID, true, false)
	after, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil || before != after {
		t.Fatal("failed txn changed physical Host", after, err)
	}
	if _, err = s.db.CurrentOriginalOwnerForLaunch(context.Background(), h.LaunchID); err != store.ErrNotFound {
		t.Fatal("failed txn consumed authorization", err)
	}
	if _, err = external.Exec(`DROP TRIGGER fail_successor_prepare`); err != nil {
		t.Fatal(err)
	}
	if err = ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	r, err = ipc.Read(c)
	if err != nil || r.Kind != ipc.KindResponse {
		t.Fatal("exact retry after failed txn", r, err)
	}
	waitSuccessorTransportState(t, s, h.LaunchID, false, true)
}

func TestSuccessorTransportPrepareACKLossReconnectsSameFirstHost(t *testing.T) {
	s, h := successorTransportFixture(t)
	c, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	frame := successorPrepareFrame(s)
	if err := ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	// Do not read the reply. Wait for the committed promotion, then discard
	// the socket and retain this exact physical FirstHost and request bytes.
	waitSuccessorTransportState(t, s, h.LaunchID, false, true)
	prior, err := s.db.CurrentOriginalOwnerForLaunch(context.Background(), h.LaunchID)
	if err != nil || prior.Phase != "intended" {
		t.Fatal(prior, err)
	}
	_ = c.Close()
	waitSuccessorTransportState(t, s, h.LaunchID, false, false)
	c, r = successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal("reconnect rejected", r.Kind, string(r.Payload))
	}
	if err = ipc.Write(c, frame); err != nil {
		t.Fatal(err)
	}
	r, err = ipc.Read(c)
	var prepared contract.OwnerPrepared
	if err != nil || r.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(r.Payload, &prepared) != nil || prepared.Intent != prior.Intent {
		t.Fatal("ACK loss allocated a different intent", r, err)
	}
}

func TestSuccessorTransportRejectsForeignSelectorBeforePublication(t *testing.T) {
	s, h := successorTransportFixture(t)
	before, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"stale-command", " leading", strings.Repeat("x", 129)} {
		h.ResumeCommandID = id
		c, r := successorTransportConnect(t, s, h)
		_ = c.Close()
		if r.Kind != ipc.KindError {
			t.Fatal("foreign selector admitted", id, r.Kind)
		}
	}
	after, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil || after != before {
		t.Fatal("foreign selector published Host", after, err)
	}
	waitSuccessorTransportState(t, s, h.LaunchID, false, false)
}

func TestSuccessorTransportCloseReleasesOnlyPendingConnection(t *testing.T) {
	s, h := successorTransportFixture(t)
	c, r := successorTransportConnect(t, s, h)
	if r.Kind != ipc.KindOwnerHostPending {
		t.Fatal(r.Kind, string(r.Payload))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ipc.Read(c); err == nil {
		t.Fatal("closed server retained pending connection")
	}
	s.mu.Lock()
	pending := len(s.successorTransports)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatal("Close retained pending map")
	}
}

func TestSuccessorTransportFirstHostRequiresSelectorAfterPrepare(t *testing.T) {
	for _, phase := range []string{"intended", "bound"} {
		t.Run(phase, func(t *testing.T) {
			s, h := successorTransportFixture(t)
			c, r := successorTransportConnect(t, s, h)
			if r.Kind != ipc.KindOwnerHostPending {
				t.Fatal(r.Kind, string(r.Payload))
			}
			frame := successorPrepareFrame(s)
			if err := ipc.Write(c, frame); err != nil {
				t.Fatal(err)
			}
			r, err := ipc.Read(c)
			var prepared contract.OwnerPrepared
			if err != nil || r.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(r.Payload, &prepared) != nil {
				t.Fatal(r, err)
			}
			if phase == "bound" {
				step, finish := startOwnerRPCChild(t, ownerChildConfig{Socket: prepared.RPCSocketPath, Prepared: prepared, BootstrapToken: strings.Repeat("e", 64)})
				defer finish()
				if reply := step(ownerChildStep{Kind: "bind", ID: "successor-bind"}); reply.Error != "" {
					t.Fatal(reply.Error)
				}
			}
			_ = c.Close()
			waitSuccessorTransportState(t, s, h.LaunchID, false, false)
			before, err := s.db.HostBinding(context.Background(), h.LaunchID)
			if err != nil {
				t.Fatal(err)
			}
			h.ResumeCommandID = ""
			c, r = successorTransportConnect(t, s, h)
			_ = c.Close()
			if r.Kind != ipc.KindError {
				t.Fatal("missing selector bypassed successor admission", phase, r.Kind, string(r.Payload))
			}
			waitSuccessorTransportState(t, s, h.LaunchID, false, false)
			after, err := s.db.HostBinding(context.Background(), h.LaunchID)
			if err != nil || after != before {
				t.Fatal("rejected selector mutated physical Host", after, err)
			}
			h.ResumeCommandID = "successor-command"
			c, r = successorTransportConnect(t, s, h)
			if r.Kind != ipc.KindOwnerHostPending {
				t.Fatal("correct selector reconnect rejected", phase, r.Kind, string(r.Payload))
			}
			if err = ipc.Write(c, frame); err != nil {
				t.Fatal(err)
			}
			r, err = ipc.Read(c)
			var replay contract.OwnerPrepared
			if err != nil || r.Kind != ipc.KindResponse || contract.DecodeOwnerRuntimeStrict(r.Payload, &replay) != nil || replay != prepared {
				t.Fatal("correct selector Prepare replay changed", phase, r, err)
			}
		})
	}
}

func TestSuccessorTransportRejectsMalformedSelectorWire(t *testing.T) {
	s, h := successorTransportFixture(t)
	before, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(h)
	base := string(body)
	for _, wire := range []string{
		strings.Replace(base, `"resume_command_id":"successor-command"`, `"resume_command_id":null`, 1),
		strings.Replace(base, `"resume_command_id":"successor-command"`, `"resume_command_id":""`, 1),
		strings.Replace(base, `"resume_command_id":"successor-command"`, `"resume_command_id":42`, 1),
		strings.TrimSuffix(base, "}") + `,"resume_command_id":"successor-command"}`,
		strings.TrimSuffix(base, "}") + `,"authorization_id":"caller-selected"}`,
	} {
		c, err := net.Dial("unix", s.SocketPath())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		err = ipc.Write(c, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostHello, RequestID: "malformed-selector", Payload: json.RawMessage(wire)})
		if err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		r, err := ipc.Read(c)
		_ = c.Close()
		if err == nil && r.Kind != ipc.KindError {
			t.Fatal("malformed selector admitted", wire, r.Kind)
		}
	}
	after, err := s.db.HostBinding(context.Background(), h.LaunchID)
	if err != nil || after != before {
		t.Fatal("malformed selector mutated Store", after, err)
	}
	waitSuccessorTransportState(t, s, h.LaunchID, false, false)
}
