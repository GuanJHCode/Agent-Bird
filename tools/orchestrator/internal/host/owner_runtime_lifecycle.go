package host

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type ownerRuntimeNotice struct {
	generation uint64
	pending    *contract.OwnerHostPending
	offline    bool
	cause      error
}

type ownerRuntimeLimits struct{ term, join, flush, total time.Duration }

// The workflow alone writes the recovery ledger. Its command reader remains
// independent of RPC/disk waits and coalesces only unauthenticated transport
// hints. Authenticated terminal/local failure uses the cancellation latch.
type ownerRuntimeLifecycle struct {
	execution     *ownerExecution
	cancel        context.CancelCauseFunc
	limits        ownerRuntimeLimits
	mu            sync.Mutex
	generation    uint64
	latest        *ownerRuntimeNotice
	wake          chan struct{}
	cleanupOnce   sync.Once
	cleanupResult error
	cleanupDone   chan struct{}
}

func newOwnerRuntimeLifecycle(e *ownerExecution, cancel context.CancelCauseFunc) *ownerRuntimeLifecycle {
	return &ownerRuntimeLifecycle{execution: e, cancel: cancel, wake: make(chan struct{}, 1), limits: ownerRuntimeLimits{10 * time.Second, 11 * time.Second, 2 * time.Second, 13 * time.Second}}
}

func (r *ownerRuntimeLifecycle) activateGeneration(generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation > r.generation {
		r.generation, r.latest = generation, nil
	}
}

func (r *ownerRuntimeLifecycle) notice(n ownerRuntimeNotice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n.generation != r.generation {
		return
	}
	if n.pending != nil {
		copy := *n.pending
		n.pending = &copy
	}
	r.latest = &n
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *ownerRuntimeLifecycle) takeNotice(generation uint64) (ownerRuntimeNotice, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation != r.generation || r.latest == nil {
		return ownerRuntimeNotice{}, false
	}
	n := *r.latest
	r.latest = nil
	return n, true
}

func (r *ownerRuntimeLifecycle) readControl(ctx context.Context, generation uint64, conn *net.UnixConn) <-chan struct{} {
	r.activateGeneration(generation)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		lost := func(err error) { r.notice(ownerRuntimeNotice{generation: generation, offline: true, cause: err}) }
		for {
			frame, err := ipc.Read(conn)
			if err != nil {
				lost(err)
				return
			}
			switch frame.Kind {
			case ipc.KindOwnerCommand:
				command, err := contract.DecodeOwnerCommand(frame.Payload)
				if err != nil || frame.Epoch != command.Permit.CoordinatorEpoch || frame.RequestID != command.RequestID {
					lost(errOwnerTransportProtocol)
					return
				}
				err = r.execution.command(frame.Payload)
				switch ownerCommandDisposition(err) {
				case ownerCommandOK, ownerCommandStale, ownerCommandStopped:
				case ownerCommandRecover:
					r.notice(ownerRuntimeNotice{generation: generation, cause: err})
				case ownerCommandInvalid:
					lost(err)
					return
				default:
					r.cancel(err)
					return
				}
			case ipc.KindOwnerHostPending:
				pending, err := contract.DecodeOwnerHostPending(frame.Payload)
				binding := r.execution.cfg.binding
				if err != nil || frame.RequestID != binding.Scope.LaunchID || frame.Epoch != pending.CoordinatorEpoch || pending.HostID != binding.Scope.ProducerID || pending.OwnerID != binding.Scope.OwnerID || pending.CoordinatorPublicKey != hex.EncodeToString(r.execution.cfg.coordinatorKey) {
					lost(errOwnerTransportProtocol)
					return
				}
				r.notice(ownerRuntimeNotice{generation: generation, pending: &pending})
			case ipc.KindOwnerTransportControl:
				control, err := contract.DecodeOwnerTransportControl(frame.Payload)
				if err != nil || frame.Epoch != 0 || control.Action != "offline" {
					lost(errOwnerTransportProtocol)
					return
				}
				r.notice(ownerRuntimeNotice{generation: generation, offline: true})
			default:
				lost(errOwnerTransportProtocol)
				return
			}
		}
	}()
	return done
}

func (r *ownerRuntimeLifecycle) watchOrigin(ctx context.Context, scope contract.OriginalOwnerScope) <-chan struct{} {
	return r.watchOriginWith(ctx, scope, process.BirthContext)
}

func (r *ownerRuntimeLifecycle) watchFailures(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case cause := <-r.execution.fatal:
			r.cancel(cause)
		case <-ctx.Done():
		}
	}()
	return done
}

func (r *ownerRuntimeLifecycle) watchOriginWith(ctx context.Context, scope contract.OriginalOwnerScope, birth func(context.Context, int) (string, error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			op, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			observed, err := birth(op, scope.OriginalOriginPID)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil || observed != scope.OriginalOriginBirth {
				r.cancel(errors.New("origin_owner_lost"))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

func (r *ownerRuntimeLifecycle) publish(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			session, err := r.execution.available(ctx)
			if err != nil {
				return
			}
			r.execution.mu.Lock()
			changed := r.execution.changed
			r.execution.mu.Unlock()
			err = r.execution.run.host.PublishAll(ctx, session, session.permit.CoordinatorEpoch)
			if err != nil {
				if r.execution.obsoleteRejection(session, err) {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if !ownerExecutionNeedsRecovery(err) {
					r.cancel(err)
					return
				}
				r.execution.offline(session, err)
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			case <-r.execution.run.host.eventReady:
			}
		}
	}()
	return done
}

// One absolute stop window covers every original operation. Slow admission,
// publisher mutex or filesystem I/O can make completion uncertain, but cannot
// extend this boundary or be reported as successful original Wait/tree proof.
func (r *ownerRuntimeLifecycle) cleanup(cause error, joins []<-chan struct{}) error {
	r.cleanupOnce.Do(func() { r.cleanupResult = r.stop(cause, joins) })
	return r.cleanupResult
}

func (r *ownerRuntimeLifecycle) stop(cause error, joins []<-chan struct{}) error {
	started := time.Now()
	termCtx, stopTerm := context.WithDeadline(context.Background(), started.Add(r.limits.term))
	defer stopTerm()
	joinCtx, stopJoin := context.WithDeadline(context.Background(), started.Add(r.limits.join))
	defer stopJoin()
	e := r.execution
	e.beginStop(termCtx, cause)
	r.cancel(cause)
	var background sync.WaitGroup
	r.cleanupDone = make(chan struct{})
	defer func() { go func() { background.Wait(); close(r.cleanupDone) }() }()
	launch := func(fn func() error) <-chan error {
		done := make(chan error, 1)
		background.Add(1)
		go func() { defer background.Done(); done <- fn() }()
		return done
	}
	workers := launch(func() error { return e.join(joinCtx) })
	trees := launch(func() error { return e.run.host.stopAll(termCtx, nil) })
	wait := func(ctx context.Context, done <-chan error) error {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result := errors.Join(wait(joinCtx, workers), wait(joinCtx, trees))
	for _, done := range joins {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-joinCtx.Done():
			result = errors.Join(result, joinCtx.Err())
		}
	}
	deadline := time.Now().Add(r.limits.flush)
	if total := started.Add(r.limits.total); total.Before(deadline) {
		deadline = total
	}
	flushCtx, stopFlush := context.WithDeadline(context.Background(), deadline)
	defer stopFlush()
	flushed := launch(func() error { return e.flush(flushCtx) })
	return errors.Join(result, wait(flushCtx, flushed))
}
