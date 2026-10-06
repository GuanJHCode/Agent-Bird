package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ownerRuntimeControlPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	a, f, err := ipc.NewOwnerControlPair()
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b.(*net.UnixConn)
}

func TestOwnerRuntimeControlConsumesLaunchWhileActivateWaits(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	activated, release, invoked := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	original := cfg.rpc
	cfg.rpc = func(ctx context.Context, rev uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		if kind == "activate" {
			close(activated)
			select {
			case <-release:
			case <-ctx.Done():
				return contract.OwnerRPCReply{}, ctx.Err()
			}
		}
		return original(ctx, rev, kind, id, body)
	}
	cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		invoked <- struct{}{}
		return nil, errors.New("controlled noStart")
	}
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	defer e.run.cancel(context.Canceled)
	r := newOwnerRuntimeLifecycle(e, cancel)
	a, b := ownerRuntimeControlPair(t)
	reader := r.readControl(ctx, 1, a)
	reconciled := make(chan error, 1)
	go func() { reconciled <- e.reconcile(ctx, permit) }()
	<-activated
	raw := ownerExecutionSignedLaunch(t, permit, key)
	if err = ipc.Write(b, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: "command", Epoch: permit.CoordinatorEpoch, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-invoked:
	case <-time.After(time.Second):
		t.Fatal("control reader waited for Activate ACK and lost admission")
	}
	close(release)
	if err = <-reconciled; err != nil {
		t.Fatal(err)
	}
	b.Close()
	select {
	case <-reader:
	case <-time.After(time.Second):
		t.Fatal("control EOF not joined")
	}
	if ctx.Err() != nil || e.run.ctx.Err() != nil {
		t.Fatal("transport EOF canceled owner/worker lifetime")
	}
	notice, ok := r.takeNotice(1)
	if !ok || !notice.offline || !errors.Is(notice.cause, io.EOF) {
		t.Fatalf("missing transport loss: %+v %v", notice, ok)
	}
	e.beginStop(context.Background(), context.Canceled)
	if err = e.join(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerRuntimeNoticeGenerationCannotReplaceCurrentTransport(t *testing.T) {
	r := newOwnerRuntimeLifecycle(nil, func(error) {})
	r.activateGeneration(2)
	r.notice(ownerRuntimeNotice{generation: 2, pending: &contract.OwnerHostPending{Version: 1, CoordinatorEpoch: 9}})
	for i := 0; i < 100; i++ {
		r.notice(ownerRuntimeNotice{generation: 1, offline: true, cause: io.EOF})
	}
	n, ok := r.takeNotice(2)
	if !ok || n.offline || n.pending == nil || n.pending.CoordinatorEpoch != 9 {
		t.Fatalf("old EOF overwrote live transport: %+v", n)
	}
	if _, ok = r.takeNotice(1); ok {
		t.Fatal("old generation still visible")
	}
}

func TestOwnerRuntimeTerminalBypassesPendingNotice(t *testing.T) {
	cfg, permit, key := ownerExecutionFixture(t)
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	r := newOwnerRuntimeLifecycle(e, cancel)
	a, b := ownerRuntimeControlPair(t)
	done := r.readControl(ctx, 1, a)
	r.notice(ownerRuntimeNotice{generation: 1, offline: true})
	body, _ := json.Marshal(contract.OwnerTerminal{Reason: "host_retired"})
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: permit, Kind: "terminal", RequestID: "retire", Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	if err = ipc.Write(b, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: "retire", Epoch: permit.CoordinatorEpoch, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal blocked behind pending transport notice")
	}
	if ownerCommandDisposition(context.Cause(ctx)) != ownerCommandTerminal {
		t.Fatal(context.Cause(ctx))
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal reader not joined")
	}
}

func TestOwnerRuntimeCleanupBoundsBlockedAdmissionAndPublisher(t *testing.T) {
	for _, blocked := range []string{"admission", "publisher"} {
		t.Run(blocked, func(t *testing.T) {
			cfg, permit, key := ownerExecutionFixture(t)
			var starts atomic.Int32
			cfg.invocation = func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
				starts.Add(1)
				return nil, errors.New("unexpected provider")
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.reconcile(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
			r := newOwnerRuntimeLifecycle(e, func(error) {})
			r.limits = ownerRuntimeLimits{term: 30 * time.Millisecond, join: 40 * time.Millisecond, flush: 20 * time.Millisecond, total: 60 * time.Millisecond}
			var done <-chan struct{}
			release := make(chan struct{})
			if blocked == "admission" {
				entered, commandDone := make(chan struct{}), make(chan struct{})
				accept := e.acceptGrant
				e.acceptGrant = func(g contract.LaunchCommand) (bool, error) { close(entered); <-release; return accept(g) }
				go func() { defer close(commandDone); _ = e.command(ownerExecutionSignedLaunch(t, permit, key)) }()
				<-entered
				done = commandDone
			} else {
				e.run.host.publishMu.Lock()
				published := make(chan struct{})
				done = published
				go func() { <-release; e.run.host.publishMu.Unlock(); close(published) }()
			}
			started := time.Now()
			err = r.cleanup(context.Canceled, nil)
			elapsed := time.Since(started)
			close(release)
			<-done
			<-r.cleanupDone
			if !errors.Is(err, context.DeadlineExceeded) || elapsed > 300*time.Millisecond {
				t.Fatalf("cleanup hid incomplete wait or exceeded total bound: %v %s", err, elapsed)
			}
			if starts.Load() != 0 || e.run.ctx.Err() == nil {
				t.Fatal("shutdown admitted work or did not cancel execution")
			}
			if err = e.join(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnerRuntimeOriginObservationHonorsDeadlineAndCancellation(t *testing.T) {
	for _, scenario := range []string{"birth_change", "unknown", "deadline", "caller_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			r := newOwnerRuntimeLifecycle(nil, cancel)
			entered := make(chan struct{})
			done := r.watchOriginWith(ctx, contract.OriginalOwnerScope{OriginalOriginPID: 42, OriginalOriginBirth: "original"}, func(op context.Context, pid int) (string, error) {
				close(entered)
				deadline, ok := op.Deadline()
				if pid != 42 || !ok || time.Until(deadline) > 500*time.Millisecond {
					t.Error("unbounded/wrong origin observation")
				}
				switch scenario {
				case "birth_change":
					return "recycled", nil
				case "unknown":
					return "", errors.New("unknown")
				default:
					<-op.Done()
					return "", op.Err()
				}
			})
			<-entered
			if scenario == "caller_cancel" {
				cancel(context.Canceled)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("origin watcher blocked shutdown")
			}
			if ctx.Err() == nil {
				t.Fatal("lost origin did not close runtime")
			}
			if scenario == "caller_cancel" && !errors.Is(context.Cause(ctx), context.Canceled) {
				t.Fatal("watcher overwrote original cause")
			}
		})
	}
}

func TestOwnerRuntimeCleanupJoinsOriginalWorkerAndIsSingleLatch(t *testing.T) {
	cfg, permit, _ := ownerExecutionFixture(t)
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
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
			t.Error("original worker cleanup failed")
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
	var cancellations atomic.Int32
	r := newOwnerRuntimeLifecycle(e, func(error) { cancellations.Add(1) })
	started := time.Now()
	if err = r.cleanup(errors.New("origin_owner_lost"), nil); err != nil {
		t.Fatal(err)
	}
	<-r.cleanupDone
	if err = p.ConfirmTreeExited(); err != nil {
		t.Fatal("cleanup returned without original Wait/tree proof", err)
	}
	deadline := cfg.host.stopAllIntent.deadline
	if deadline.Before(started.Add(10*time.Second)) || deadline.After(started.Add(11*time.Second)) {
		t.Fatal("TERM budget no longer comes from single cleanup start")
	}
	if err = r.cleanup(errors.New("later terminal"), nil); err != nil || cancellations.Load() != 1 || !deadline.Equal(cfg.host.stopAllIntent.deadline) {
		t.Fatal("cleanup reentry renewed stop window", err)
	}
}

func TestOwnerRuntimeFatalRPCIsNotLostBehindRecoveryNotification(t *testing.T) {
	cfg, permit, _ := ownerExecutionFixture(t)
	e, err := newOwnerExecution(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.run.cancel(context.Canceled)
	if err = e.reconcile(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	session := e.session
	e.offline(session, &ownerExecutionRecoveryError{cause: io.EOF})
	if len(e.recovery) != 1 {
		t.Fatal("missing queued recovery")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	r := newOwnerRuntimeLifecycle(e, cancel)
	done := r.watchFailures(ctx)
	e.cfg.rpc = func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
		return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
	}
	if err = session.status(ctx, "progress", "progress", struct{}{}, "recorded"); err == nil {
		t.Fatal("authentication failure accepted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fatal reply lost behind recovery from previous session")
	}
	if context.Cause(ctx) == nil || ownerExecutionNeedsRecovery(context.Cause(ctx)) {
		t.Fatal("fatal error was converted to another Ensure", context.Cause(ctx))
	}
}

func TestOwnerRuntimePublisherReplaysAfterSameEpochPermitReplacement(t *testing.T) {
	for _, scenario := range []string{"signed_rejection", "attach_in_flight", "authentication_failure"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, first, key := ownerExecutionFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var oldCalls, newCalls atomic.Int32
			cfg.rpc = func(ctx context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
				reply := contract.OwnerRPCReply{Version: 1, OwnerID: first.OwnerID, CoordinatorEpoch: first.CoordinatorEpoch, RequestID: id}
				var value any
				switch kind {
				case "event":
					event := body.(contract.Event)
					if revision == 1 {
						if oldCalls.Add(1) == 1 {
							close(entered)
						}
						select {
						case <-release:
						case <-ctx.Done():
							return contract.OwnerRPCReply{}, ctx.Err()
						}
						if scenario == "authentication_failure" {
							return contract.OwnerRPCReply{}, errors.New("original_owner_reply_rejected")
						}
						reply.Error = "original_owner_rejected"
						return reply, errors.New(reply.Error)
					}
					newCalls.Add(1)
					value = contract.DurableAck{ProducerID: event.ProducerID, EventID: event.EventID, PayloadHash: event.PayloadHash, AckedThrough: event.Sequence, Status: "durable"}
				case "applied":
					value = map[string]string{"status": "applied"}
				case "activate":
					value = map[string]string{"status": "active"}
				default:
					t.Error("unexpected RPC", kind)
				}
				reply.Body, _ = json.Marshal(value)
				return reply, nil
			}
			e, err := newOwnerExecution(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.run.cancel(context.Canceled)
			if err = e.reconcile(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			spool, err := cfg.host.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = cfg.host.appendPhaseEvent(spool, "run", "task", a, launchMetadata{commandID: "command", workRevision: 1, executionEpoch: first.CoordinatorEpoch}, contract.EventPrepared, hashText("pending"), nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			r := newOwnerRuntimeLifecycle(e, cancel)
			publisher := r.publish(ctx)
			<-entered
			if scenario == "attach_in_flight" {
				e.pausePublication() // before the remote Attach can commit revision 2
				close(release)
			}
			next := first
			next.ExpectedControlRevision, next.ControlRevision = 1, 2
			next.OldHost = &first.NewHost
			next.NewHost.PID++
			next.RequestID, next.PermitID = "replace", "permit-2"
			next, err = contract.SignOwnerControlPermit(next, key)
			if err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			changed := e.changed
			e.mu.Unlock()
			reconciled := make(chan error, 1)
			go func() { reconciled <- e.reconcile(ctx, next) }()
			<-changed // verified revision 2 installed; PublishAll still waits for old RPC
			if scenario != "attach_in_flight" {
				close(release)
			}
			err = <-reconciled
			if scenario != "authentication_failure" {
				if err != nil || ctx.Err() != nil {
					t.Error("obsolete signed rejection killed valid owner", err, context.Cause(ctx))
				}
				if cursor, err := cfg.host.readPublished(); err != nil || cursor != 1 || newCalls.Load() != 1 {
					t.Error("new permit did not durably replay original event", cursor, err, newCalls.Load())
				}
			} else {
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Error("stale session concealed authentication failure")
				}
			}
			cancel(context.Canceled)
			select {
			case <-publisher:
			case <-time.After(time.Second):
				t.Fatal("publisher failed to join")
			}
		})
	}
}
