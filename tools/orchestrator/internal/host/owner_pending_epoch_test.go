package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ownerEpochReplacementFixture(t *testing.T) (ownerExecutionConfig, contract.OwnerControlPermit, contract.OwnerControlPermit, contract.OwnerTransportPending, ed25519.PrivateKey) {
	t.Helper()
	cfg, first, key := ownerExecutionFixture(t)
	pending := contract.OwnerTransportPending{Version: 1, RecoverySlot: 1, RequestID: "replacement", ExpectedControlRevision: first.ControlRevision, OldHost: first.NewHost, NewHost: contract.OriginalOwnerTransport{PID: 40, Birth: "replacement-birth"}}
	attach := contract.OwnerAttachRequest{ExpectedControlRevision: pending.ExpectedControlRevision, OldHost: pending.OldHost, NewHost: pending.NewHost, Pending: &pending}
	raw, _ := json.Marshal(attach)
	next := first
	next.ExpectedControlRevision, next.ControlRevision = first.ControlRevision, first.ControlRevision+1
	next.OldHost, next.NewHost = &pending.OldHost, pending.NewHost
	next.RequestID, next.RequestSHA256, next.PermitID = pending.RequestID, hashText(string(raw)), "replacement-permit"
	next.Signature = ""
	next, err := contract.SignOwnerControlPermit(next, key)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, first, next, pending, key
}

func TestOwnerExecutionHistoricalReplacementRequiresExactPending(t *testing.T) {
	for _, scenario := range []string{"exact", "old_same_tuple_guard", "slot", "request", "child", "predecessor", "signature", "scope", "body", "revision", "replay"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, first, next, pending, key := ownerEpochReplacementFixture(t)
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			e.observeEpoch(first.CoordinatorEpoch+1, &ownerExecutionRecoveryError{cause: errCoordinatorUnavailable})
			priorSession := e.session
			calls := 0
			e.cfg.rpc = func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
				calls++
				return contract.OwnerRPCReply{}, errors.New("historical proof must not publish")
			}
			switch scenario {
			case "slot":
				pending.RecoverySlot++
			case "request":
				pending.RequestID += "-foreign"
			case "child":
				pending.NewHost.PID++
			case "predecessor":
				next.OldHost = &contract.OriginalOwnerTransport{PID: 99, Birth: "foreign"}
			case "scope":
				next.OwnerID = "foreign"
			case "body":
				next.RequestSHA256 = hashText("foreign attach")
			case "revision":
				next.ExpectedControlRevision++
				next.ControlRevision++
			}
			if scenario != "signature" {
				next.Signature = ""
				next, err = contract.SignOwnerControlPermit(next, key)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				next.RequestID += "-unsigned"
			}
			if scenario == "old_same_tuple_guard" {
				err = e.rememberHistoricalPermit(next)
			} else {
				err = e.rememberHistoricalReplacementPermit(next, pending)
			}
			allowed := scenario == "exact" || scenario == "replay"
			if (err == nil) != allowed {
				t.Fatalf("historical replacement acceptance=%v want=%v: %v", err == nil, allowed, err)
			}
			want := first
			if allowed {
				want = next
			}
			gotProof, _ := ownerRecoveryProof(e.last.permit)
			wantProof, _ := ownerRecoveryProof(want)
			if gotProof != wantProof || e.session != priorSession || e.active || e.admitSignedLaunch || calls != 0 {
				t.Fatal("historical proof changed execution authority or rejected proof mutated the chain")
			}
			if scenario == "replay" && e.rememberHistoricalReplacementPermit(next, pending) == nil {
				t.Fatal("duplicate historical predecessor advanced the chain twice")
			}
		})
	}
}

func TestOriginalOwnerRecoveryRenewalPreservesOriginalStart(t *testing.T) {
	for _, phase := range []string{"attached", "applied", "activated"} {
		t.Run(phase, func(t *testing.T) {
			cfg, _, replacement, pending, key := ownerEpochReplacementFixture(t)
			dir := privateRecoveryTestDirectory(t)
			s, err := openOwnerRecovery(dir, replacement.ScopeSHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.close() }()
			if _, err = s.reserveTransport(time.UnixMilli(100_000), pending.RequestID, pending.ExpectedControlRevision, pending.OldHost); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = s.recordTransportStarted(pending.NewHost); err != nil {
				t.Fatal(err)
			}
			if err = s.markTransportAttached(replacement); err != nil {
				t.Fatal(err)
			}
			applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: replacement, Reconciled: contract.HostReconciled{ProducerID: cfg.binding.Scope.ProducerID}}, cfg.privateKey)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "attached" {
				if err = s.markTransportReconciledApplied(replacement, applied); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "activated" {
				if err = s.markTransportActivated(applied); err != nil {
					t.Fatal(err)
				}
			}
			renewed := replacement
			renewed.ExpectedControlRevision, renewed.ControlRevision = replacement.ControlRevision, replacement.ControlRevision+1
			renewed.CoordinatorEpoch++
			renewed.OldHost = &pending.NewHost
			renewed.RequestID, renewed.PermitID, renewed.Signature = "renewal", "renewed-permit", ""
			renewed, err = contract.SignOwnerControlPermit(renewed, key)
			if err != nil {
				t.Fatal(err)
			}
			freshApplied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: renewed, Reconciled: applied.Reconciled}, cfg.privateKey)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(s.file.path)
			for _, invalid := range []string{"foreign_child", "mismatched_applied", "old_predecessor", "original_request", "old_revision"} {
				badPermit, badApplied := renewed, freshApplied
				switch invalid {
				case "foreign_child":
					badPermit.NewHost.PID++
				case "mismatched_applied":
					badApplied = applied
				case "old_predecessor":
					badPermit.OldHost = &pending.OldHost
				case "original_request":
					badPermit.RequestID = pending.RequestID
				case "old_revision":
					badPermit.ExpectedControlRevision, badPermit.ControlRevision = 1, 2
				}
				if invalid != "mismatched_applied" {
					badApplied.Permit = badPermit
				}
				if s.markTransportReconciledApplied(badPermit, badApplied) == nil {
					t.Fatal("invalid renewal accepted", invalid)
				}
				after, _ := os.ReadFile(s.file.path)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected renewal changed durable state", invalid)
				}
			}
			if err = s.markTransportReconciledApplied(renewed, freshApplied); err != nil {
				t.Fatal(err)
			}
			permitHash, _ := ownerRecoveryProof(renewed)
			appliedHash, _ := ownerRecoveryProof(freshApplied)
			p := s.record.OwnerPending
			if p.Phase != "applied" || p.PermitSHA256 != permitHash || p.AppliedSHA256 != appliedHash || p.StartSlot != 1 || p.RequestID != pending.RequestID || p.ExpectedControlRevision != pending.ExpectedControlRevision || p.OldHost != pending.OldHost || *p.NewHost != pending.NewHost || s.record.Attempts != 1 || s.record.Reservation != nil {
				t.Fatalf("renewal lost original Start or proof pair: %+v", s.record)
			}
			if err = s.markTransportActivated(freshApplied); err != nil {
				t.Fatal(err)
			}
			s.close()
			s, err = openOwnerRecovery(dir, replacement.ScopeSHA256)
			if err != nil {
				t.Fatal(err)
			}
			before, _ = os.ReadFile(s.file.path)
			if err = s.markTransportReconciledApplied(renewed, freshApplied); err != nil {
				t.Fatal("same proof replay rejected", err)
			}
			after, _ := os.ReadFile(s.file.path)
			if !bytes.Equal(before, after) || s.record.OwnerPending.Phase != "activated" {
				t.Fatal("replay regressed phase or rewrote ledger")
			}
			freshApplied.Reconciled.PublishedThrough++
			if s.markTransportReconciledApplied(renewed, freshApplied) == nil {
				t.Fatal("same permit accepted a different Applied receipt")
			}
		})
	}
}

func TestOwnerTransportPendingWaitIgnoresStaleNotices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lifecycle := newOwnerRuntimeLifecycle(nil, nil)
	lifecycle.activateGeneration(2)
	lifecycle.wake <- struct{}{} // No matching notice: a previously consumed wake.
	done := make(chan error, 1)
	go func() {
		pending, err := awaitOwnerTransportPending(ctx, lifecycle, 2, 8)
		if err == nil && pending.CoordinatorEpoch != 8 {
			err = errors.New("accepted an obsolete epoch")
		}
		done <- err
	}()
	for _, notice := range []ownerRuntimeNotice{{generation: 2, offline: true}, {generation: 2, cause: &ownerExecutionRecoveryError{cause: errCoordinatorUnavailable}}, {generation: 2, pending: &contract.OwnerHostPending{CoordinatorEpoch: 7}}} {
		lifecycle.notice(notice)
		for {
			lifecycle.mu.Lock()
			consumed := lifecycle.latest == nil
			lifecycle.mu.Unlock()
			if consumed {
				break
			}
			select {
			case err := <-done:
				t.Fatal("stale notice ended fresh pending wait", err)
			case <-time.After(time.Millisecond):
			}
		}
	}
	lifecycle.notice(ownerRuntimeNotice{generation: 1, pending: &contract.OwnerHostPending{CoordinatorEpoch: 9}})
	lifecycle.notice(ownerRuntimeNotice{generation: 2, pending: &contract.OwnerHostPending{CoordinatorEpoch: 8}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := errors.New("authenticated local failure")
	lifecycle.notice(ownerRuntimeNotice{generation: 2, cause: want})
	if _, err := awaitOwnerTransportPending(ctx, lifecycle, 2, 8); !errors.Is(err, want) {
		t.Fatal("fatal notice hidden", err)
	}
	cancel()
	if _, err := awaitOwnerTransportPending(ctx, lifecycle, 2, 8); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled wait ignored", err)
	}
}
