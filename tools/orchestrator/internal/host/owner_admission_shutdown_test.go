package host

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestOwnerAdmissionDiskWaitDoesNotBlockStopOrStartWorkerAfterStop(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	var starts atomic.Int32
	cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		starts.Add(1)
		return nil, context.Canceled
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	e.acceptGrant = func(g contract.LaunchCommand) (bool, error) {
		close(entered)
		<-release
		return cfg.host.acceptGrant(g)
	}
	commandDone := make(chan error, 1)
	raw := ownerExecutionSignedLaunch(t, permit, key)
	go func() { commandDone <- e.command(raw) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("command did not enter durable admission")
	}
	stopped := make(chan struct{})
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { e.beginStop(stopCtx, context.Canceled); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		released.Store(true)
		close(release)
		<-commandDone
		<-stopped
		t.Fatal("durable grant wait blocked owner stop registration")
	}
	if starts.Load() != 0 {
		t.Fatal("worker started before durable admission")
	}
	short, stopShort := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := e.join(short); err != context.DeadlineExceeded {
		t.Fatal("join ignored unfinished durable admission", err)
	}
	stopShort()
	// A shutdown fence now forbids any subsequent workers.Add, even though
	// the original, already authenticated grant finishes its durable write.
	released.Store(true)
	close(release)
	select {
	case err := <-commandDone:
		if err == nil {
			t.Fatal("stopped admission reported a worker launch")
		}
	case <-time.After(time.Second):
		t.Fatal("admission did not finish after disk release")
	}
	waited := make(chan struct{})
	go func() { e.run.workers.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("stopped worker group did not join")
	}
	if starts.Load() != 0 {
		t.Fatal("in-flight grant started worker after stop")
	}
	joined, stopJoin := context.WithTimeout(context.Background(), time.Second)
	defer stopJoin()
	if err := e.join(joined); err != nil {
		t.Fatal("released admissions did not join", err)
	}
	events, err := cfg.host.Collect(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"})
	if err != nil || len(events) != 1 || events[0].Kind != contract.EventLaunchRejected {
		t.Fatalf("stopped admitted claim lost known noStart settlement: events=%+v err=%v", events, err)
	}

}

func TestOwnerAdmissionReceivedBeforeRenewalKeepsOriginalClaim(t *testing.T) {
	cfg, first, key := ownerExecutionFixture(t)
	var invoked atomic.Int32
	cfg.invocation = func(_ context.Context, g contract.LaunchCommand) (contract.InvocationView, error) {
		invoked.Add(1)
		if g.ExecutionEpoch != first.CoordinatorEpoch {
			t.Error("renewal rewrote admitted grant epoch")
		}
		return nil, context.Canceled
	}
	cfg.rpc = func(_ context.Context, _ uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		applied := body.(contract.OwnerPermitApplied)
		status := "applied"
		if kind == "activate" {
			status = "active"
		}
		raw, _ := json.Marshal(map[string]string{"status": status})
		return contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: applied.Permit.CoordinatorEpoch, RequestID: id, Body: raw}, nil
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.reconcile(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	e.acceptGrant = func(g contract.LaunchCommand) (bool, error) {
		close(entered)
		<-release
		return cfg.host.acceptGrant(g)
	}
	raw := ownerExecutionSignedLaunch(t, first, key)
	done := make(chan error, 1)
	go func() { done <- e.command(raw) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("admission did not enter")
	}
	next := first
	next.ExpectedControlRevision = 1
	next.ControlRevision = 2
	next.OldHost = &first.NewHost
	next.CoordinatorEpoch++
	next.RequestID = "renewal"
	next.PermitID = "permit-2"
	next, err = contract.SignOwnerControlPermit(next, key)
	if err != nil {
		t.Fatal(err)
	}
	renewed := make(chan error, 1)
	go func() { renewed <- e.reconcile(context.Background(), next) }()
	select {
	case err = <-renewed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		released.Store(true)
		close(release)
		<-done
		<-renewed
		t.Fatal("grant disk wait blocked permit renewal")
	}
	if err = e.command(raw); err == nil {
		t.Fatal("new old-permit admission crossed renewal")
	}
	released.Store(true)
	close(release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal("renewal erased already admitted claim", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admission did not finish")
	}
	e.run.workers.Wait()
	if invoked.Load() != 1 {
		t.Fatal("already admitted claim lost or duplicated", invoked.Load())
	}
}
