package host

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func recoveryEnrollmentFixture(t *testing.T) (*ownerEnrollment, contract.OwnerBindReply) {
	t.Helper()
	e, b, key := ownerEnrollmentFixture(t)
	b.Binding.Scope.Incarnation = 2
	b.Binding.Scope.OwnerID += "-next"
	b.Binding.BindingSHA256 = ""
	var err error
	b.Binding, err = contract.SignOriginalOwnerBinding(b.Binding, key)
	if err != nil {
		t.Fatal(err)
	}
	b.Permit.OwnerID = b.Binding.Scope.OwnerID
	b.Permit.ScopeSHA256, _ = contract.OriginalOwnerScopeSHA256(b.Binding.Scope)
	b.Permit.Signature = ""
	b.Permit, err = contract.SignOwnerControlPermit(b.Permit, key)
	if err != nil {
		t.Fatal(err)
	}
	e.bootstrap.Prepared.Intent.Scope = b.Binding.Scope
	e.client.Intent = e.bootstrap.Prepared.Intent
	if verifyOwnerEnrollment(e.bootstrap, e.client.Owner, ed25519.PublicKey(e.client.CoordinatorKey), b) != nil {
		t.Fatal("invalid successor enrollment fixture")
	}
	return e, b
}
func recoveryRPCSuccess(b contract.OwnerBindReply, id string) contract.OwnerRPCReply {
	return contract.OwnerRPCReply{Version: 1, OwnerID: b.Binding.Scope.OwnerID, CoordinatorEpoch: b.Permit.CoordinatorEpoch, RequestID: id, Body: json.RawMessage(`{"version":1,"status":"recovery_scope_applied"}`)}
}

func TestOwnerRecoveryRPCStartupReplaysOnlyUnsignedTransientWithFixedDeadline(t *testing.T) {
	e, b := recoveryEnrollmentFixture(t)
	deadline := time.Now().Add(time.Second).UnixMilli()
	e.bootstrap.Prepared.Intent.StartupDeadlineUnixMS = deadline
	e.client.Intent = e.bootstrap.Prepared.Intent
	id, _ := contract.OwnerRecoveryScopeRequestID(b.Binding.Scope, b.Binding.Owner)
	calls := 0
	var firstDeadline time.Time
	rpc := func(ctx context.Context, revision uint64, kind, gotID string, body any) (contract.OwnerRPCReply, error) {
		calls++
		if revision != 1 || kind != "recovery_scope" || gotID != id {
			t.Fatal("mutable handoff identity")
		}
		raw, _ := json.Marshal(body)
		if string(raw) != `{"version":1}` {
			t.Fatal("caller-supplied authority", string(raw))
		}
		d, ok := ctx.Deadline()
		if !ok || d.After(time.UnixMilli(deadline)) {
			t.Fatal("deadline extended")
		}
		if calls == 1 {
			firstDeadline = d
		} else if !d.Equal(firstDeadline) {
			t.Fatal("original deadline renewed")
		}
		if calls < 3 {
			return contract.OwnerRPCReply{}, io.EOF
		}
		return recoveryRPCSuccess(b, id), nil
	}
	if err := e.handoffRecoveryWith(context.Background(), b, rpc); err != nil || calls != 3 {
		t.Fatal("same passive replay failed", calls, err)
	}
}
func TestOwnerRecoveryRPCStartupBoundsAttemptsAndDoesNotRetrySignedRefusal(t *testing.T) {
	for _, kind := range []string{"always_eof", "signed_eof", "semantic", "signature", "bad_reply", "expired", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			e, b := recoveryEnrollmentFixture(t)
			id, _ := contract.OwnerRecoveryScopeRequestID(b.Binding.Scope, b.Binding.Owner)
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "expired" {
				e.bootstrap.Prepared.Intent.StartupDeadlineUnixMS = time.Now().Add(-time.Second).UnixMilli()
				e.client.Intent = e.bootstrap.Prepared.Intent
			}
			if kind == "cancelled" {
				cancel()
			}
			rpc := func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
				calls++
				switch kind {
				case "signed_eof":
					r := recoveryRPCSuccess(b, id)
					r.Error = "scope_rejected"
					return r, io.EOF
				case "semantic":
					return contract.OwnerRPCReply{}, errors.New("owner_recovery_state_rejected")
				case "signature":
					return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
				case "bad_reply":
					r := recoveryRPCSuccess(b, id)
					r.OwnerID = "foreign"
					return r, nil
				default:
					return contract.OwnerRPCReply{}, io.EOF
				}
			}
			if err := e.handoffRecoveryWith(ctx, b, rpc); err == nil {
				t.Fatal("invalid handoff accepted")
			}
			want := 1
			if kind == "always_eof" {
				want = 3
			}
			if kind == "expired" || kind == "cancelled" {
				want = 0
			}
			if calls != want {
				t.Fatal("unexpected retry count", calls, want)
			}
		})
	}
}
func TestOwnerRecoveryRPCStartupRejectsMalformedStatus(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"version":1}`, `{"version":null,"status":"recovery_scope_applied"}`, `{"version":1,"status":"other"}`, `{"version":1,"status":"recovery_scope_applied","path":"/foreign"}`, `{"version":1,"version":1,"status":"recovery_scope_applied"}`} {
		t.Run(body, func(t *testing.T) {
			e, b := recoveryEnrollmentFixture(t)
			calls := 0
			err := e.handoffRecoveryWith(context.Background(), b, func(_ context.Context, _ uint64, _ string, id string, _ any) (contract.OwnerRPCReply, error) {
				calls++
				r := recoveryRPCSuccess(b, id)
				r.Body = json.RawMessage(body)
				return r, nil
			})
			if err == nil || calls != 1 {
				t.Fatal("malformed status accepted/retried", err, calls)
			}
		})
	}
}
func TestOwnerRecoveryRPCInitialOwnerDoesNotRequestHandoff(t *testing.T) {
	e, b, _ := ownerEnrollmentFixture(t)
	if err := e.handoffRecoveryWith(context.Background(), b, func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
		t.Fatal("initial owner used successor RPC")
		return contract.OwnerRPCReply{}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerExecutionRechecksRecoveryBeforeAppliedAndActivate(t *testing.T) {
	for _, phase := range []string{"applied", "activate", "replay"} {
		t.Run(phase, func(t *testing.T) {
			cfg, p, _ := ownerExecutionFixture(t)
			checks, calls := 0, []string{}
			reject := errors.New("changed-recovery-ledger")
			cfg.verifyRecovery = func() error {
				checks++
				if (phase == "applied" && checks == 1) || (phase == "activate" && checks == 2) || (phase == "replay" && checks == 3) {
					return reject
				}
				return nil
			}
			cfg.rpc = func(_ context.Context, _ uint64, kind, id string, _ any) (contract.OwnerRPCReply, error) {
				calls = append(calls, kind)
				status := "applied"
				if kind == "activate" {
					status = "active"
				}
				return contract.OwnerRPCReply{Version: 1, OwnerID: p.OwnerID, CoordinatorEpoch: p.CoordinatorEpoch, RequestID: id, Body: json.RawMessage(`{"status":"` + status + `"}`)}, nil
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if phase == "replay" {
				if err = e.reconcile(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				calls = nil
			}
			if err = e.reconcile(context.Background(), p); !errors.Is(err, reject) {
				t.Fatal("changed recovery authorized RPC", err)
			}
			if e.admitSignedLaunch || e.active {
				t.Fatal("ledger rejection retained signed-launch admission")
			}
			want := ""
			if phase == "activate" {
				want = "applied"
			}
			if strings.Join(calls, ",") != want {
				t.Fatal("RPC happened after failed ledger check", calls)
			}
		})
	}
}

func TestOwnerRecoveryRPCSuccessorOpenIsExistingOnly(t *testing.T) {
	for _, scenario := range []string{"current", "prior", "missing_ledger", "missing_lock", "missing_launch", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			bootstrap, binding, old := ownerRecoveryLeaseFixture(t)
			old.close()
			nextScope, nextOwner := handoffSuccessor(binding)
			bound := contract.OwnerBindReply{Binding: contract.OriginalOwnerBinding{Scope: nextScope, Owner: nextOwner}}
			if scenario != "prior" {
				lease, err := OpenExistingOwnerRecoveryHandoff(bootstrap.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, nil)
				if err != nil {
					t.Fatal(err)
				}
				facts, err := lease.Facts()
				if err != nil {
					t.Fatal(err)
				}
				if err = lease.CommitPrepared(handoffProof(facts, false)); err != nil {
					t.Fatal(err)
				}
				lease.Close()
			}
			switch scenario {
			case "missing_ledger":
				if err := os.Remove(filepath.Join(bootstrap.SpoolRoot, recoveryLedgerName)); err != nil {
					t.Fatal(err)
				}
			case "missing_lock":
				if err := os.Remove(filepath.Join(bootstrap.SpoolRoot, recoveryLockName)); err != nil {
					t.Fatal(err)
				}
			case "missing_launch":
				if err := os.RemoveAll(bootstrap.SpoolRoot); err != nil {
					t.Fatal(err)
				}
			case "busy":
				lease, err := OpenExistingOwnerRecoveryLease(bootstrap.StateDir, nextScope, nextOwner)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
			}
			before := recoveryLeaseSnapshot(t, bootstrap.StateDir)
			got, close, err := openBoundOwnerRecovery(bootstrap.StateDir, bootstrap.SpoolRoot, bound)
			if scenario == "current" {
				if err != nil || got.record.ScopeSHA256 == "" {
					t.Fatal("current existing ledger unavailable", err)
				}
				close()
			} else {
				if err == nil {
					close()
					t.Fatal("invalid successor ledger opened", scenario)
				}
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, bootstrap.StateDir)) {
				t.Fatal("successor repaired/rewrote existing-only state")
			}
		})
	}
}

func TestOwnerRecoveryRPCSuccessorHeldLeasePreservesBudgetWrites(t *testing.T) {
	bootstrap, binding, old := ownerRecoveryLeaseFixture(t)
	old.close()
	nextScope, nextOwner := handoffSuccessor(binding)
	lease, err := OpenExistingOwnerRecoveryHandoff(bootstrap.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := lease.Facts()
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.CommitPrepared(handoffProof(facts, false)); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	bound := contract.OwnerBindReply{Binding: contract.OriginalOwnerBinding{Scope: nextScope, Owner: nextOwner}}
	current, close, err := openBoundOwnerRecovery(bootstrap.StateDir, bootstrap.SpoolRoot, bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = current.reserveCoordinator(time.UnixMilli(100_000)); err != nil {
		t.Fatal(err)
	}
	if err = current.verifyCurrent(); err != nil {
		t.Fatal("retained lease prevented normal budget write", err)
	}
	if current.record.Attempts != 1 || current.record.Reservation == nil {
		t.Fatal("budget not preserved")
	}
	close()
	current, close, err = openBoundOwnerRecovery(bootstrap.StateDir, bootstrap.SpoolRoot, bound)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if current.record.Attempts != 1 || current.record.Reservation == nil || current.record.ScopeSHA256 != facts.ScopeSHA256 {
		t.Fatal("reopen reset budget")
	}
}
func TestOwnerRecoveryRPCStartupOperationDeadlineIsCapped(t *testing.T) {
	for _, callerBound := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller_%t", callerBound), func(t *testing.T) {
			e, b := recoveryEnrollmentFixture(t)
			ctx := context.Background()
			cancel := func() {}
			if callerBound {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			}
			defer cancel()
			now := time.Now()
			calls := 0
			err := e.handoffRecoveryWith(ctx, b, func(op context.Context, _ uint64, _ string, id string, _ any) (contract.OwnerRPCReply, error) {
				calls++
				d, ok := op.Deadline()
				if !ok || d.After(now.Add(5*time.Second+50*time.Millisecond)) {
					t.Fatal("operation exceeded five seconds")
				}
				if parent, ok := ctx.Deadline(); ok && d.After(parent) {
					t.Fatal("operation extended caller deadline")
				}
				return recoveryRPCSuccess(b, id), nil
			})
			if err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}
