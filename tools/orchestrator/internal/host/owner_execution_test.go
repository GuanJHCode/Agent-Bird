package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

func ownerExecutionSignedLaunch(t *testing.T, permit contract.OwnerControlPermit, key ed25519.PrivateKey) []byte {
	t.Helper()
	grant := withHostArtifactsForTest(t, contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", HostID: "producer", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: permit.CoordinatorEpoch, GrantedActiveMS: 1000})
	body, _ := json.Marshal(grant)
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: grant.CommandID, Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	return raw
}

func TestOwnerExecutionSignedLaunchDoesNotDependOnActivateReplyOrder(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "command_before_reply"
		if lost {
			name = "command_after_lost_reply"
		}
		t.Run(name, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			var invoked atomic.Int32
			cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				invoked.Add(1)
				return nil, errors.New("no process started")
			}
			original := cfg.rpc
			activating, release := make(chan struct{}), make(chan struct{})
			cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				if kind == "activate" {
					// The Coordinator committed Activate and can dispatch on the
					// other socket before this reply is delivered (or lost).
					close(activating)
					<-release
					if lost {
						return contract.OwnerRPCReply{}, io.EOF
					}
				}
				return original(ctx, revision, kind, id, body)
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			done := make(chan error, 1)
			go func() { done <- e.reconcile(context.Background(), permit) }()
			<-activating
			raw := ownerExecutionSignedLaunch(t, permit, key)
			var commandErr, reconcileErr error
			if lost {
				close(release)
				reconcileErr = <-done
				commandErr = e.command(raw)
			} else {
				commandErr = e.command(raw)
				close(release)
				reconcileErr = <-done
			}
			e.run.workers.Wait()
			if commandErr != nil || invoked.Load() != 1 {
				t.Fatalf("durably claimed signed launch was lost: %v invoked=%d", commandErr, invoked.Load())
			}
			if lost != errors.Is(reconcileErr, io.EOF) || (!lost && reconcileErr != nil) {
				t.Fatal("unexpected activation outcome", reconcileErr)
			}
			if err = e.command(raw); err != nil {
				t.Fatal("duplicate signed command rejected", err)
			}
			e.run.workers.Wait()
			if invoked.Load() != 1 {
				t.Fatal("ACK ambiguity launched a second invocation")
			}
			e.beginStop(context.Background(), context.Canceled)
			if err = e.command(raw); err == nil {
				t.Fatal("signed launch crossed shutdown admission fence")
			}
		})
	}
}

func TestOwnerExecutionReportStoreRejectionDoesNotEnterRecovery(t *testing.T) {
	for _, scenario := range []string{"conflict", "invalid_report_capability"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			cfg, permit, key := ownerExecutionFixture(t)
			db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/pinned/orchestrator"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			producer, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/pinned/orchestrator"}, permit.CoordinatorEpoch)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := db.ClaimReady(ctx, producer, permit.CoordinatorEpoch, 1)
			if err != nil {
				t.Fatal(err)
			}
			cfg.host, err = NewIPC(filepath.Join(t.TempDir(), "spool"), producer)
			if err != nil {
				t.Fatal(err)
			}
			cfg.binding.Scope.ProducerID = producer
			cfg.binding.Signature = ""
			cfg.binding.BindingSHA256 = ""
			cfg.binding, err = contract.SignOriginalOwnerBinding(cfg.binding, key)
			if err != nil {
				t.Fatal(err)
			}
			permit.ScopeSHA256, _ = contract.OriginalOwnerScopeSHA256(cfg.binding.Scope)
			permit.Signature = ""
			permit, err = contract.SignOwnerControlPermit(permit, key)
			if err != nil {
				t.Fatal(err)
			}
			original := cfg.rpc
			calls := 0
			cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				if kind != "report_capability" {
					return original(ctx, revision, kind, id, body)
				}
				calls++
				registration := body.(contract.ReportCapabilityRegistration)
				if scenario == "conflict" {
					registered := registration
					registered.TokenHash = strings.Repeat("d", 64)
					if _, err := db.RegisterReportCapability(ctx, registered); err != nil {
						t.Fatal("could not establish real conflicting binding", err)
					}
				} else {
					registration.WorkRevision = 0
				}
				_, err := db.RegisterReportCapability(ctx, registration)
				if err == nil || err.Error() != scenario {
					t.Fatalf("unexpected Store result %v", err)
				}
				// The production client returns a populated reply only after
				// verifying its signature/request binding. This fixture uses a
				// real SQLite rejection, but does not claim native RPC proof.
				return contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Error: err.Error()}, err
			}
			e, err := newOwnerExecution(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(ctx, permit); err != nil {
				t.Fatal(err)
			}
			session := e.session
			deadline, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err = e.prepareReport(deadline, grant); err == nil || err.Error() != scenario {
				t.Fatalf("permanent Store error was hidden by recovery: %v", err)
			}
			if calls != 1 || e.session != session || !e.active || len(e.recovery) != 0 {
				t.Fatal("permanent report rejection changed session or requested recovery")
			}
		})
	}
}

func TestOwnerExecutionReportLostACKRetainsBindingUntilReconcile(t *testing.T) {
	cfg, permit, _ := ownerExecutionFixture(t)
	original := cfg.rpc
	var calls atomic.Int32
	registrations := make(chan contract.ReportCapabilityRegistration, 4)
	cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		if kind != "report_capability" {
			return original(ctx, revision, kind, id, body)
		}
		registration := body.(contract.ReportCapabilityRegistration)
		registrations <- registration
		if calls.Add(1) == 1 {
			return contract.OwnerRPCReply{}, io.EOF
		}
		raw, _ := json.Marshal(contract.DurableAck{Status: "durable", EventID: registration.CapabilityID, PayloadHash: registration.TokenHash})
		return contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = e.reconcile(ctx, permit); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		path, err := e.prepareReport(ctx, contract.LaunchCommand{ReportCapabilityID: "capability", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1})
		if err == nil && path == "" {
			err = errors.New("acknowledged report has no capability path")
		}
		done <- err
	}()
	select {
	case err = <-e.recovery:
		if !ownerExecutionNeedsRecovery(err) || !errors.Is(err, io.EOF) {
			t.Fatal("lost ACK did not retain transport uncertainty", err)
		}
	case <-ctx.Done():
		t.Fatal("lost report ACK failed to request recovery")
	}
	select {
	case err = <-done:
		t.Fatal("report preparation completed before a new session", err)
	default:
	}
	if err = e.reconcile(ctx, permit); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("transient report registration did not resume", err)
	}
	close(registrations)
	first := <-registrations
	if calls.Load() != 3 {
		t.Fatal("expected original request, registry replay, and resumed preparation", calls.Load())
	}
	for replay := range registrations {
		if replay != first {
			t.Fatal("ACK loss changed immutable report registration")
		}
	}
}

func TestOwnerExecutionRPCFailureClassification(t *testing.T) {
	for _, scenario := range []string{"signed_semantic", "signed_error_named_EOF", "higher_epoch", "canceled_higher_epoch", "canceled_activate_rejection", "lower_epoch", "transport_EOF", "invalid_signature", "canceled_parent"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			s := e.session
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			recoverable, unchanged := false, false
			s.owner.cfg.rpc = func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
				reply := contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: "request", Body: json.RawMessage(`{"status":"applied"}`)}
				switch scenario {
				case "signed_semantic", "signed_error_named_EOF":
					unchanged = true
					reply.Error = "conflict"
					if scenario == "signed_error_named_EOF" {
						reply.Error = "EOF"
					}
					return reply, errors.New(reply.Error)
				case "higher_epoch":
					recoverable = true
					reply.CoordinatorEpoch++
					reply.Error = "original_owner_rejected"
					return reply, errors.New(reply.Error)
				case "canceled_higher_epoch":
					recoverable = true
					cancel()
					reply.CoordinatorEpoch++
					reply.Error = "original_owner_rejected"
					return reply, errors.New(reply.Error)
				case "canceled_activate_rejection":
					unchanged = true // RPC remains usable, but launch admission closes.
					cancel()
					reply.Error = "original_owner_rejected"
					return reply, errors.New(reply.Error)
				case "lower_epoch":
					reply.CoordinatorEpoch--
					return reply, nil
				case "transport_EOF":
					recoverable = true
					return contract.OwnerRPCReply{}, io.EOF
				case "invalid_signature":
					return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
				case "canceled_parent":
					unchanged = true
					cancel()
					return contract.OwnerRPCReply{}, io.EOF
				}
				panic("unknown scenario")
			}
			kind := "applied"
			if scenario == "canceled_activate_rejection" {
				kind = "activate"
			}
			err = s.status(ctx, kind, "request", *e.applied, "applied")
			if err == nil || ownerExecutionNeedsRecovery(err) != recoverable {
				t.Fatal("wrong error classification", err)
			}
			if unchanged {
				if e.session != s || (scenario != "canceled_activate_rejection" && !e.active) || len(e.recovery) != 0 {
					t.Fatal("non-transport outcome requested recovery")
				}
			} else if e.session != nil || e.active || len(e.recovery) != 1 {
				t.Fatal("transport or authentication failure did not reach runtime")
			}
			if scenario == "higher_epoch" || scenario == "canceled_higher_epoch" || scenario == "canceled_activate_rejection" || scenario == "lower_epoch" || scenario == "invalid_signature" {
				if e.command(ownerExecutionSignedLaunch(t, permit, key)) == nil {
					e.run.workers.Wait()
					t.Fatal("revoked epoch or authentication failure retained launch authority")
				}
			}
			if scenario == "higher_epoch" {
				if err := e.reconcile(context.Background(), permit); err == nil || e.session != nil {
					t.Fatal("old permit reopened a session after newer epoch proof", err)
				}
			}
		})
	}
}

func TestOwnerExecutionProgressKeepsNetworkErrorsObservationalButFencesInvalidReplies(t *testing.T) {
	for _, scenario := range []string{"EOF", "semantic", "invalid_signature", "malformed_ACK", "wrong_status"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			session := e.session
			e.cfg.rpc = func(_ context.Context, _ uint64, _ string, id string, _ any) (contract.OwnerRPCReply, error) {
				reply := contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id}
				switch scenario {
				case "EOF":
					return contract.OwnerRPCReply{}, io.EOF
				case "semantic":
					reply.Error = "conflict"
					return reply, errors.New(reply.Error)
				case "invalid_signature":
					return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
				case "malformed_ACK":
					reply.Body = json.RawMessage(`{"status":"recorded","unknown":1}`)
				case "wrong_status":
					reply.Body = json.RawMessage(`{"status":"applied"}`)
				}
				return reply, nil
			}
			if err = e.progress(context.Background(), contract.Event{EventID: "progress"}); err == nil || ownerExecutionNeedsRecovery(err) {
				t.Fatal("progress outcome was lost or converted to transient recovery", err)
			}
			if scenario == "EOF" || scenario == "semantic" {
				if e.session != session || !e.admitSignedLaunch || len(e.recovery) != 0 {
					t.Fatal("best-effort progress failure changed live authority")
				}
			} else {
				if e.session != nil || e.admitSignedLaunch || len(e.recovery) != 1 {
					t.Fatal("invalid progress reply retained live authority")
				}
				if e.command(ownerExecutionSignedLaunch(t, permit, key)) == nil {
					e.run.workers.Wait()
					t.Fatal("invalid progress reply allowed another launch")
				}
			}
		})
	}
}

func TestOwnerExecutionSignedActivateRejectionRevokesAdmission(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	original := cfg.rpc
	cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		if kind == "activate" {
			return contract.OwnerRPCReply{Version: 1, OwnerID: permit.OwnerID, CoordinatorEpoch: permit.CoordinatorEpoch, RequestID: id, Error: "original_owner_rejected"}, errors.New("original_owner_rejected")
		}
		return original(ctx, revision, kind, id, body)
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.reconcile(context.Background(), permit); err == nil || err.Error() != "original_owner_rejected" {
		t.Fatal("expected verified activation rejection", err)
	}
	if e.command(ownerExecutionSignedLaunch(t, permit, key)) == nil {
		e.run.workers.Wait()
		t.Fatal("signed activation rejection retained launch authority")
	}
	if len(e.recovery) != 0 {
		t.Fatal("signed activation rejection was converted to transport recovery")
	}
}

func TestOwnerExecutionNewEpochProofRevokesEvenFromOldSessionOrProgress(t *testing.T) {
	for _, viaProgress := range []bool{false, true} {
		name := "old_session"
		if viaProgress {
			name = "progress"
		}
		t.Run(name, func(t *testing.T) {
			cfg, first, key := ownerExecutionFixture(t)
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			old := e.session
			if err = e.reconcile(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			e.cfg.rpc = func(_ context.Context, _ uint64, kind, id string, _ any) (contract.OwnerRPCReply, error) {
				return contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: first.CoordinatorEpoch + 1, RequestID: id, Error: "original_owner_rejected"}, errors.New("original_owner_rejected")
			}
			if viaProgress {
				err = e.progress(context.Background(), contract.Event{EventID: "progress"})
			} else {
				err = old.status(context.Background(), "applied", "late-call", *e.applied, "applied")
			}
			if !ownerExecutionNeedsRecovery(err) || e.session != nil || e.admitSignedLaunch {
				t.Fatal("newer epoch proof did not revoke the current session", err)
			}
			if e.reconcile(context.Background(), first) == nil {
				t.Fatal("old epoch reopened after signed revocation")
			}
			next := first
			next.ExpectedControlRevision, next.ControlRevision = 1, 2
			next.CoordinatorEpoch++
			next.OldHost = &first.NewHost
			next.RequestID, next.PermitID, next.Signature = "renewal", "permit-2", ""
			next, err = contract.SignOwnerControlPermit(next, key)
			if err != nil {
				t.Fatal(err)
			}
			e.cfg.rpc = func(_ context.Context, _ uint64, kind, id string, _ any) (contract.OwnerRPCReply, error) {
				status := "applied"
				if kind == "activate" {
					status = "active"
				}
				raw, _ := json.Marshal(map[string]string{"status": status})
				return contract.OwnerRPCReply{Version: 1, OwnerID: next.OwnerID, CoordinatorEpoch: next.CoordinatorEpoch, RequestID: id, Body: raw}, nil
			}
			if err = e.reconcile(context.Background(), next); err != nil || !e.active || !e.admitSignedLaunch {
				t.Fatal("verified renewal failed to reopen admission", err)
			}
		})
	}
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
	next := permit
	next.ExpectedControlRevision, next.ControlRevision = 1, 2
	next.CoordinatorEpoch++
	next.OldHost = &permit.NewHost
	next.RequestID, next.PermitID = "renewal", "permit-2"
	next, err = contract.SignOwnerControlPermit(next, key)
	if err != nil {
		t.Fatal(err)
	}
	if ownerCommandDisposition(e.command(ownerExecutionSignedLaunch(t, next, key))) != ownerCommandRecover {
		t.Fatal("verified newer command did not revoke epoch")
	}
	terminal, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "terminal", RequestID: "old-terminal", Body: json.RawMessage(`{"reason":"host_retired"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	staleRaw, _ := json.Marshal(terminal)
	if ownerCommandDisposition(e.command(staleRaw)) != ownerCommandStale || e.stopping {
		t.Fatal("old terminal terminated owner after newer epoch proof")
	}
	alive, stopAlive := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if p.Wait(alive) == nil {
		t.Fatal("stale terminal stopped original worker")
	}
	stopAlive()

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
	if err = e.command(ownerExecutionSignedLaunch(t, first, key)); err == nil {
		t.Fatal("epoch renewal retained old permit launch authority")
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
	grant := withHostArtifactsForTest(t, contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", HostID: "producer", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: permit.CoordinatorEpoch, GrantedActiveMS: 1000})
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
	original := e.cfg.rpc
	e.cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		if kind == "applied" {
			if err := e.command(valid); err == nil {
				t.Error("installing a permit admitted a launch before Applied confirmation")
			}
		}
		return original(ctx, revision, kind, id, body)
	}
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if err = e.command(bytes.Replace(valid, []byte(`"task_id":"task"`), []byte(`"task_id":"drift"`), 1)); err == nil {
		t.Fatal("modified signed body accepted")
	}
	foreign := grant
	foreign.RunID = "foreign"
	foreign = withHostArtifactsForTest(t, foreign)
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
