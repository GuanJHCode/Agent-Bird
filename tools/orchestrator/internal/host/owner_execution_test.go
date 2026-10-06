package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Signed identity fixtures exercise the execution contract only. They do not
// claim a kernel enrollment or a physical owner/transport replacement.
func ownerExecutionFixture(t *testing.T) (ownerExecutionConfig, contract.OwnerControlPermit, ed25519.PrivateKey) {
	t.Helper()
	coordinatorPublic, coordinatorPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := contract.OriginalOwnerScope{Version: 1, RunID: "run", LaunchID: "launch", ProducerID: "producer", OwnerID: "owner", Incarnation: 1, OriginalOriginContextID: "origin", OriginalOriginPID: 10, OriginalOriginBirth: "origin-birth", HostGeneration: "generation", Executable: "/owned/orchestrator", ExecutableSHA256: strings.Repeat("a", 64)}
	owner := contract.OriginalOwnerIdentity{PID: 30, Birth: "owner-birth", PublicKey: hex.EncodeToString(ownerPublic)}
	transport := contract.OriginalOwnerTransport{PID: 20, Birth: "transport-birth"}
	keyID, _ := contract.OriginalOwnerKeyID(coordinatorPublic)
	binding, err := contract.SignOriginalOwnerBinding(contract.OriginalOwnerBinding{Scope: scope, Owner: owner, CoordinatorKeyID: keyID, FirstHost: transport, BindingRevision: 1}, coordinatorPrivate)
	if err != nil {
		t.Fatal(err)
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(owner)
	permit, err := contract.SignOwnerControlPermit(contract.OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: scope.OwnerID, ScopeSHA256: scopeHash, OwnerIdentitySHA256: identityHash, CoordinatorKeyID: keyID, CoordinatorEpoch: 7, ControlRevision: 1, NewHost: transport, RequestID: "bind", RequestSHA256: strings.Repeat("b", 64), PermitID: "permit-1"}, coordinatorPrivate)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), scope.ProducerID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ownerExecutionConfig{host: h, binding: binding, privateKey: ownerPrivate, coordinatorKey: coordinatorPublic, reportSocket: filepath.Join(t.TempDir(), "coordinator.sock"), invocation: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		return nil, errors.New("fixture has no provider")
	}}
	cfg.rpc = func(_ context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		if revision != permit.ControlRevision {
			t.Error("wrong control revision")
		}
		status := "applied"
		if kind == "activate" {
			status = "active"
		}
		raw, _ := json.Marshal(map[string]string{"status": status})
		return contract.OwnerRPCReply{Version: 1, OwnerID: scope.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
	}
	return cfg, permit, coordinatorPrivate
}

func TestOwnerExecutionRetainsSignedStopAfterTransportLoss(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	p, err := process.Start(context.Background(), process.Command{Path: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if p.Stop(ctx) != nil {
			t.Error("original cleanup missing")
		}
	})
	a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	spool, err := cfg.host.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	identity := p.Identity()
	if err = publishLaunchPhase(spool, "running", a, "run", "task", process.Command{Path: "/bin/sleep"}, &identity, nil); err != nil {
		t.Fatal(err)
	}
	if err = cfg.host.registerOwnedHandle(a.ID, a.SegmentID, p); err != nil {
		t.Fatal(err)
	}
	defer cfg.host.unregisterOwnedHandle(a.ID, a.SegmentID, p)
	e.mu.Lock()
	s := e.session
	e.mu.Unlock()
	e.offline(s, errors.New("owned transport EOF"))
	probe, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if p.Wait(probe) == nil {
		t.Error("transport EOF stopped the original worker")
	}
	cancel()
	body, _ := json.Marshal(contract.StopCommand{CommandID: "stop-command", SegmentID: a.SegmentID, Reason: "canceled", DeadlineUnixMS: time.Now().Add(time.Second).UnixMilli()})
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "stop", RequestID: "stop-command", Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	if err = e.command(raw); err != nil {
		t.Fatal("transport loss erased signed stop authority", err)
	}
	if p.ConfirmTreeExited() != nil {
		t.Fatal("signed stop lost original Wait/tree proof")
	}
}

func TestOwnerExecutionRejectsMalformedPrivateKeyBeforeOwnershipMutation(t *testing.T) {
	cfg, _, _ := ownerExecutionFixture(t)
	cfg.privateKey = append(ed25519.PrivateKey(nil), cfg.privateKey...)
	cfg.privateKey[0] ^= 1 // unchanged public half must not disguise another seed
	if _, err := newOwnerExecution(context.Background(), cfg); err == nil {
		t.Fatal("inconsistent signing seed accepted")
	}
}

func TestOwnerExecutionEpochRenewalReplaysReportsAndRejectsInvalidTransitions(t *testing.T) {
	cfg, first, key := ownerExecutionFixture(t)
	epoch, revision := first.CoordinatorEpoch, first.ControlRevision
	var registered []uint64
	var receipts []contract.OwnerPermitApplied
	calls := 0
	cfg.rpc = func(_ context.Context, rev uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		calls++
		if rev != revision {
			t.Error("unexpected control revision", rev)
		}
		var result any
		switch kind {
		case "report_capability":
			registration := body.(contract.ReportCapabilityRegistration)
			if registration.ExecutionEpoch != epoch {
				t.Error("report replay retained stale epoch")
			}
			registered = append(registered, registration.ExecutionEpoch)
			result = contract.DurableAck{Status: "durable", EventID: registration.CapabilityID, PayloadHash: registration.TokenHash}
		case "applied":
			receipts = append(receipts, body.(contract.OwnerPermitApplied))
			result = map[string]string{"status": "applied"}
		case "activate":
			result = map[string]string{"status": "active"}
		default:
			t.Fatalf("unexpected RPC %s", kind)
		}
		raw, _ := json.Marshal(result)
		return contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: epoch, RequestID: id, Body: raw}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.run.reports.remember(contract.ReportCapabilityRegistration{CapabilityID: "capability", TokenHash: strings.Repeat("c", 64), CapabilityDir: t.TempDir(), RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", ProducerID: "producer", WorkRevision: 1, ExecutionEpoch: epoch}); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcile(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	next := first
	next.ExpectedControlRevision, next.ControlRevision = 1, 2
	next.CoordinatorEpoch = first.CoordinatorEpoch + 1
	next.OldHost = &first.NewHost
	next.RequestID, next.PermitID = "renewal", "permit-2"
	next.Signature = ""
	next, err = contract.SignOwnerControlPermit(next, key)
	if err != nil {
		t.Fatal(err)
	}
	epoch, revision = next.CoordinatorEpoch, next.ControlRevision
	if err = e.reconcile(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 2 || registered[0] != first.CoordinatorEpoch || registered[1] != next.CoordinatorEpoch || len(receipts) != 2 || receipts[1].Permit.PermitID != next.PermitID {
		t.Fatal("renewal lost report or new permit application", registered, receipts)
	}
	before := calls
	if err = e.reconcile(context.Background(), first); err == nil || calls != before {
		t.Fatal("old permit rolled back runtime authority", err)
	}
	invalid := next
	invalid.ExpectedControlRevision, invalid.ControlRevision = 2, 3
	invalid.RequestID, invalid.PermitID = "same-epoch", "permit-3"
	invalid.Signature = ""
	invalid, err = contract.SignOwnerControlPermit(invalid, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcile(context.Background(), invalid); err == nil || calls != before {
		t.Fatal("same tuple/epoch invented another control revision", err)
	}
}

func TestOwnerExecutionAppliedACKLossReplaysOriginalReceiptAfterCursorAdvances(t *testing.T) {
	cfg, permit, _ := ownerExecutionFixture(t)
	var applied [][]byte
	var order []string
	failFirst := true
	cfg.rpc = func(_ context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		order = append(order, kind)
		if revision != permit.ControlRevision {
			t.Error("wrong revision")
		}
		var result any
		switch kind {
		case "event":
			event := body.(contract.Event)
			if event.ExecutionEpoch != permit.CoordinatorEpoch {
				t.Error("wrong event wire epoch")
			}
			result = contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "durable"}
		case "applied":
			value := body.(contract.OwnerPermitApplied)
			if contract.VerifyOwnerPermitApplied(value, cfg.privateKey.Public().(ed25519.PublicKey)) != nil {
				t.Error("invalid original-owner applied signature")
			}
			raw, _ := json.Marshal(value)
			applied = append(applied, raw)
			if failFirst {
				failFirst = false
				return contract.OwnerRPCReply{}, errors.New("fixture committed applied then lost ACK")
			}
			result = map[string]string{"status": "applied"}
		case "activate":
			result = map[string]string{"status": "active"}
		default:
			t.Fatalf("unexpected RPC %s", kind)
		}
		raw, _ := json.Marshal(result)
		return contract.OwnerRPCReply{Version: 1, OwnerID: cfg.binding.Scope.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	spool, err := cfg.host.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(label string) {
		if _, err := cfg.host.appendPhaseEvent(spool, "run", "task", a, launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 6}, contract.EventPrepared, hashText(label), nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("first")
	if err = e.reconcile(context.Background(), permit); err == nil {
		t.Fatal("lost applied ACK activated execution")
	}
	appendEvent("second")
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || !bytes.Equal(applied[0], applied[1]) {
		t.Fatal("ACK loss recomputed immutable applied receipt")
	}
	var receipt contract.OwnerPermitApplied
	if json.Unmarshal(applied[1], &receipt) != nil || receipt.Reconciled.PublishedThrough != 1 {
		t.Fatal("original receipt cursor changed")
	}
	if cursor, err := cfg.host.readPublished(); err != nil || cursor != 2 {
		t.Fatal("new event was not replayed", cursor, err)
	}
	if strings.Join(order, ",") != "event,applied,event,applied,activate" {
		t.Fatal("activation/replay order changed", order)
	}
	records, err := spool.Read()
	if err != nil || len(records) != 2 || records[0].ExecutionEpoch != 6 || records[1].ExecutionEpoch != 6 {
		t.Fatal("wire replay mutated original spool")
	}
}

func TestOwnerExecutionOnlyAdmitsActivatedExactSignedScopeAndDeduplicates(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	var invoked atomic.Int32
	cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		invoked.Add(1)
		return nil, errors.New("owned invocation failure before Start")
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if e.run.cfg.Hello.LaunchToken != "" {
		t.Fatal("owner execution received launch credential")
	}
	grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", HostID: "producer", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: permit.CoordinatorEpoch, GrantedActiveMS: 1000}
	sign := func(g contract.LaunchCommand) []byte {
		body, _ := json.Marshal(g)
		command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: g.CommandID, Body: body}, key)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(command)
		return raw
	}
	valid := sign(grant)
	if err = e.command(valid); err == nil {
		t.Fatal("unapplied permit launched execution")
	}
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if err = e.command(bytes.Replace(valid, []byte(`"task_id":"task"`), []byte(`"task_id":"drift"`), 1)); err == nil {
		t.Fatal("modified signed body accepted")
	}
	foreign := grant
	foreign.RunID = "foreign"
	if err = e.command(sign(foreign)); err == nil {
		t.Fatal("cross-scope signed launch accepted")
	}
	if _, err = os.Stat(filepath.Join(cfg.host.spoolRoot, grant.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("rejected command mutated attempt spool", err)
	}
	if err = e.command(valid); err != nil {
		t.Fatal(err)
	}
	e.run.workers.Wait()
	if err = e.command(valid); err != nil {
		t.Fatal(err)
	}
	e.run.workers.Wait()
	if invoked.Load() != 1 {
		t.Fatal("duplicate command reran invocation", invoked.Load())
	}
}
