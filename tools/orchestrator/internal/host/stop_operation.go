package host

import (
	"context"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// An unbound request is an intent, never a process-tree receipt. Its absolute
// deadline and monotonic remaining window survive the caller's deferred cancel.
type segmentStopIntent struct {
	ctx      context.Context
	deadline time.Time
	expires  time.Time
	bounded  bool
	cancel   context.CancelFunc // owns a local default window; never copied globally
	op       *hostStopOperation
	terminal bool // natural failure selected under stopMu; not an exit proof
}

type hostStopOperation struct {
	segmentID string
	handle    *process.Handle
	ctx       context.Context
	cancel    context.CancelFunc
	deadline  time.Time
	bounded   bool
	startOnce sync.Once
	done      chan struct{}
	err       error // published by closing done
	retire    bool  // protected by Host.stopMu
}

func stopIntent(ctx context.Context) *segmentStopIntent {
	return stopIntentAt(ctx, time.Now())
}

func stopIntentAt(ctx context.Context, now time.Time) *segmentStopIntent {
	deadline, bounded := ctx.Deadline()
	var cancel context.CancelFunc
	if !bounded && ctx.Err() == nil {
		// The first Background request keeps the existing graceful-stop API.
		// A follower never gets here; its original intent/result is reused.
		deadline = now.Add(gracefulStopWindow)
		ctx, cancel = context.WithDeadline(ctx, deadline)
		bounded = true
	}
	i := &segmentStopIntent{ctx: ctx, deadline: deadline, bounded: bounded, cancel: cancel}
	if bounded {
		i.expires = now.Add(deadline.Sub(now))
	}
	return i
}

// Caller holds stopMu. p is the original registered handle, never a PID lookup.
func (h *Host) bindStopLocked(segment string, i *segmentStopIntent, p *process.Handle, delayed bool) *hostStopOperation {
	if i.op != nil {
		return i.op
	}
	ctx, cancel := i.ctx, i.cancel
	if delayed && i.bounded {
		var delayedCancel context.CancelFunc
		ctx, delayedCancel = context.WithDeadline(context.Background(), i.expires)
		cancel = func() {
			delayedCancel()
			if i.cancel != nil {
				i.cancel()
			}
		}
	}
	i.op = &hostStopOperation{segmentID: segment, handle: p, ctx: ctx, cancel: cancel, deadline: i.deadline, bounded: i.bounded, done: make(chan struct{})}
	return i.op
}

func (h *Host) startStop(op *hostStopOperation) {
	if op == nil {
		return
	}
	op.startOnce.Do(func() {
		go func() {
			// No new unbounded wait is introduced for a deadline-free intent.
			// Immediate original-Wait plus absence proof can still succeed.
			if !op.bounded {
				if op.ctx.Err() != nil {
					op.err = process.ErrProcessTreeUnknown
				} else {
					op.err = op.handle.ConfirmTreeExited()
					if op.ctx.Err() != nil {
						op.err = process.ErrProcessTreeUnknown
					}
				}
			} else {
				op.err = op.handle.Stop(op.ctx)
			}
			if op.cancel != nil {
				op.cancel()
			}
			close(op.done)
			h.stopMu.Lock()
			if i := h.stopIntents[op.segmentID]; i != nil && i.op == op && op.retire {
				delete(h.stopIntents, op.segmentID)
				delete(h.stopRequested, op.segmentID)
			}
			h.stopMu.Unlock()
		}()
	})
}

func joinStop(op *hostStopOperation, observe func(*hostStopOperation)) error {
	if observe != nil {
		observe(op)
	}
	// The canceled model context is not the stop operation's bound. Preserve
	// the first Stop result including its existing KILL-confirmation stage.
	<-op.done
	return op.err
}

// A failed Start can still have an original process, including when normal
// segment registration is unavailable. Keep that ownership separately so it
// cannot overwrite another segment Handle or be erased by clearStop. Failed
// proof remains latched: StopAll joins this same operation without new grace.
func (h *Host) stopFailedStart(segment string, p *process.Handle, observe func(*hostStopOperation)) error {
	h.stopMu.Lock()
	if h.failedStarts == nil {
		h.failedStarts = make(map[*process.Handle]*hostStopOperation)
	}
	op := h.failedStarts[p]
	if op == nil {
		i := h.stopIntents[segment]
		if i != nil && i.terminal {
			i = nil
		}
		if i == nil && h.stopping {
			i = h.stopAllIntent
		}
		if i == nil {
			i = stopIntent(context.Background())
			op = h.bindStopLocked(segment, i, p, false)
		} else if i.op != nil && i.op.handle == p {
			op = i.op
		} else {
			// A foreign operation owns its Handle and cancellation. Inherit
			// only its original absolute window for this separate process.
			private := &segmentStopIntent{ctx: i.ctx, deadline: i.deadline, expires: i.expires, bounded: i.bounded}
			op = h.bindStopLocked(segment, private, p, true)
		}
		h.failedStarts[p] = op
	}
	h.stopMu.Unlock()
	h.startStop(op)
	err := joinStop(op, observe)
	if err == nil {
		h.stopMu.Lock()
		if h.failedStarts[p] == op {
			delete(h.failedStarts, p)
		}
		h.stopMu.Unlock()
	}
	return err
}

// Register before cancellation can wake execute. All map/handle operations
// are short; no process probe, signal, wait or callback occurs under these locks.
func (h *Host) registerOwnedHandle(attempt, segment string, p *process.Handle) error {
	h.stopMu.Lock()
	h.mu.Lock()
	if previous := h.activeBySeg[segment]; previous != nil && previous != p {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	if i := h.stopIntents[segment]; i != nil && i.terminal {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	if i := h.stopIntents[segment]; i != nil && i.op != nil && i.op.handle != p {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	h.active[attempt], h.activeBySeg[segment] = p, p
	cancel := h.segmentCancels[segment]
	i := h.stopIntents[segment]
	if i == nil && h.stopping {
		i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		h.stopIntents[segment] = i
	}
	var op *hostStopOperation
	if i != nil {
		op = h.bindStopLocked(segment, i, p, true)
	}
	h.mu.Unlock()
	h.stopMu.Unlock()
	if op != nil && op.handle != p {
		return process.ErrProcessTreeUnknown
	}
	h.startStop(op)
	if op != nil && cancel != nil {
		cancel()
	}
	return nil
}

func (h *Host) unregisterOwnedHandle(attempt, segment string, p *process.Handle) {
	h.stopMu.Lock()
	h.mu.Lock()
	if h.active[attempt] == p {
		delete(h.active, attempt)
	}
	if h.activeBySeg[segment] == p {
		delete(h.activeBySeg, segment)
	}
	h.mu.Unlock()
	h.stopMu.Unlock()
	h.clearStop(segment)
}

// create is false for a natural successful Wait: it must not invent a stop.
// A canceled wait without an earlier operation creates the old grace once.
func (h *Host) stopForExecution(segment string, p *process.Handle, fallback context.Context, create bool, observe func(*hostStopOperation)) (bool, error) {
	h.stopMu.Lock()
	i := h.stopIntents[segment]
	if i == nil && h.stopping {
		i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		h.stopIntents[segment] = i
	}
	if i == nil && create {
		if fallback == nil {
			// stopIntent owns the original default window and its cancellation.
			fallback = context.Background()
		}
		i = stopIntent(fallback)
		h.stopIntents[segment] = i
	}
	if i == nil || i.terminal {
		h.stopMu.Unlock()
		return false, nil
	}
	op := h.bindStopLocked(segment, i, p, false)
	h.stopMu.Unlock()
	if op.handle != p {
		return true, process.ErrProcessTreeUnknown
	}
	h.startStop(op)
	return true, joinStop(op, observe)
}

// The natural observation itself is outside stopMu. Selection then shares the
// registration lock: an earlier stop governs the result, while a failed natural
// observation with no earlier stop becomes terminal before later requests can
// install an operation. Successful proof leaves candidate-finalization cancel
// behavior intact; a later stop may still interrupt that work.
func (h *Host) stopAfterNaturalProof(segment string, p *process.Handle, proofErr error, observe func(*hostStopOperation)) (bool, error) {
	h.stopMu.Lock()
	i := h.stopIntents[segment]
	if i == nil && h.stopping {
		i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		h.stopIntents[segment] = i
	}
	if i == nil || i.terminal {
		if proofErr != nil && i == nil {
			h.stopIntents[segment] = &segmentStopIntent{terminal: true}
			h.advanceNaturalGenerationLocked()
		}
		h.stopMu.Unlock()
		return false, nil
	}
	op := h.bindStopLocked(segment, i, p, false)
	h.stopMu.Unlock()
	if op.handle != p {
		return true, process.ErrProcessTreeUnknown
	}
	h.startStop(op)
	return true, joinStop(op, observe)
}

// Caller holds stopMu. No per-segment tombstone survives registry retirement.
func (h *Host) advanceNaturalGenerationLocked() {
	if h.naturalGeneration != ^uint64(0) {
		h.naturalGeneration++
	}
}
