package host

import (
	"context"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const codexHandleLeaseLimit = 64

// A command holder exists before Start. It survives a registry conflict and
// UNKNOWN; neither a disk record nor a deferred unregister is an exit proof.
type codexHandleLease struct {
	command, attempt, segment string
	cancel                    context.CancelFunc
	startEntered              bool
	handle                    *process.Handle
	launch                    process.DuplexLaunchState
	op                        *hostStopOperation
	registryConflict          bool
}

func (h *Host) reserveCodexHandleLease(g contract.LaunchCommand, cancel context.CancelFunc) (*codexHandleLease, error) {
	if cancel == nil || !codexSafeScopeID(g.CommandID) || !codexSafeScopeID(g.AttemptID) || !codexSafeScopeID(g.SegmentID) {
		return nil, process.ErrProcessTreeUnknown
	}
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping || len(h.codexLeases) >= codexHandleLeaseLimit || h.active[g.AttemptID] != nil || h.activeBySeg[g.SegmentID] != nil || h.segmentCancels[g.SegmentID] != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	for _, owned := range h.codexLeases {
		if owned.command == g.CommandID || owned.attempt == g.AttemptID || owned.segment == g.SegmentID {
			return nil, process.ErrProcessTreeUnknown
		}
	}
	if h.codexLeases == nil {
		h.codexLeases = make(map[string]*codexHandleLease)
	}
	lease := &codexHandleLease{command: g.CommandID, attempt: g.AttemptID, segment: g.SegmentID, cancel: cancel}
	h.codexLeases[g.CommandID] = lease
	h.segmentCancels[g.SegmentID] = cancel
	return lease, nil
}

func (h *Host) enterCodexLeaseStart(lease *codexHandleLease) error {
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if lease == nil || h.codexLeases[lease.command] != lease || lease.startEntered || h.stopping || h.stopRequested[lease.segment] || h.active[lease.attempt] != nil || h.activeBySeg[lease.segment] != nil {
		return process.ErrProcessTreeUnknown
	}
	lease.startEntered = true
	return nil
}

func (h *Host) attachCodexLease(lease *codexHandleLease, p *process.Handle) error {
	// Ownership precedes the metadata getter and every post-Start error.
	h.stopMu.Lock()
	h.mu.Lock()
	if lease != nil && lease.handle == nil {
		lease.handle = p
	}
	h.mu.Unlock()
	h.stopMu.Unlock()
	view, ok := p.DuplexLaunchState()
	if !ok {
		return process.ErrProcessTreeUnknown
	}
	return h.attachCodexLeaseSnapshot(lease, p, view)
}

// Private per-call core; production obtains view only from that returned
// original Handle. Synthetic pure tests do not construct OS authority.
func (h *Host) attachCodexLeaseSnapshot(lease *codexHandleLease, p *process.Handle, view process.DuplexLaunchState) error {
	h.stopMu.Lock()
	h.mu.Lock()
	if lease != nil && lease.handle == nil {
		lease.handle = p
	}
	if lease == nil || p == nil || h.codexLeases[lease.command] != lease || lease.handle != p || !lease.startEntered || view.SpawnedAt.IsZero() {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	lease.launch = view
	if view.StartupCleanup != nil {
		receipt := *view.StartupCleanup
		lease.launch.StartupCleanup = &receipt
	}
	foreign := h.activeBySeg[lease.segment] != nil && h.activeBySeg[lease.segment] != p || h.active[lease.attempt] != nil && h.active[lease.attempt] != p
	lease.registryConflict = foreign
	if !foreign {
		h.active[lease.attempt], h.activeBySeg[lease.segment] = p, p
	}
	i := h.stopIntents[lease.segment]
	if i == nil && h.stopping && h.stopAllIntent != nil {
		i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		h.stopIntents[lease.segment] = i
	}
	if lease.op == nil && view.StartupCleanup != nil {
		lease.op = codexCompletedStartupOperation(lease.segment, p, *view.StartupCleanup, i)
		if i == nil {
			i = &segmentStopIntent{ctx: lease.op.ctx, deadline: lease.op.deadline, expires: lease.op.deadline, bounded: lease.op.bounded}
			h.stopIntents[lease.segment] = i
		}
		if i.op == nil && !foreign {
			i.op = lease.op
		}
	} else if lease.op == nil && i != nil && !i.terminal {
		if i.op == nil || i.op.handle == p {
			lease.op = h.bindStopLocked(lease.segment, i, p, true)
		} else {
			lease.registryConflict = true
			// Both original handles remain held. This private operation shares
			// the earlier window without overwriting its foreign segment op.
			lease.op = codexLeaseStopOperation(lease.segment, p, i)
		}
	}
	op := lease.op
	cancel := lease.cancel
	conflict := lease.registryConflict || i != nil && i.terminal
	h.mu.Unlock()
	h.stopMu.Unlock()
	h.startStop(op)
	if (h.stopWasRequested(lease.segment) || conflict) && cancel != nil {
		cancel()
	}
	if conflict {
		return process.ErrProcessTreeUnknown
	}
	return nil
}

func (h *Host) retireCodexHandleLease(lease *codexHandleLease, exit codexOriginalExit) error {
	h.stopMu.Lock()
	h.mu.Lock()
	if lease == nil || h.codexLeases[lease.command] != lease {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	if lease.startEntered && (lease.handle == nil || exit.handle != lease.handle || exit.identity.PID < 1 || exit.identity.PGID != exit.identity.PID || exit.identity != lease.handle.Identity()) {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return process.ErrProcessTreeUnknown
	}
	delete(h.codexLeases, lease.command)
	if !lease.registryConflict {
		if h.active[lease.attempt] == lease.handle {
			delete(h.active, lease.attempt)
		}
		if h.activeBySeg[lease.segment] == lease.handle {
			delete(h.activeBySeg, lease.segment)
		}
		delete(h.segmentCancels, lease.segment)
	}
	h.mu.Unlock()
	h.stopMu.Unlock()
	if lease.cancel != nil {
		lease.cancel()
	}
	if !lease.registryConflict {
		h.clearStop(lease.segment)
	}
	return nil
}

func codexCompletedStartupOperation(segment string, p *process.Handle, receipt process.DuplexStartupCleanup, earlier *segmentStopIntent) *hostStopOperation {
	ctx, deadline, bounded := receipt.Context, receipt.Deadline, !receipt.Deadline.IsZero()
	result := receipt.Result
	if receipt.Context == nil || receipt.CompletedAt.IsZero() || !bounded {
		result = process.ErrProcessTreeUnknown
	}
	if earlier != nil {
		ctx, deadline, bounded = earlier.ctx, earlier.deadline, earlier.bounded
		if !earlier.bounded || earlier.expires.IsZero() || receipt.CompletedAt.After(earlier.expires.Add(time.Second)) {
			result = process.ErrProcessTreeUnknown
		}
	}
	op := &hostStopOperation{segmentID: segment, handle: p, ctx: ctx, deadline: deadline, bounded: bounded, done: make(chan struct{}), err: result}
	op.startOnce.Do(func() {}) // completed original operation; never invoke Stop again
	close(op.done)
	return op
}

// Caller holds stopMu. This is only for a retained Codex lease whose segment
// registry is occupied by another original handle. Preserve the first intent's
// remaining window; no foreign registry entry/result is changed.
func codexLeaseStopOperation(segment string, p *process.Handle, i *segmentStopIntent) *hostStopOperation {
	ctx := i.ctx
	var cancel context.CancelFunc
	if i.bounded {
		ctx, cancel = context.WithDeadline(context.Background(), i.expires)
	}
	return &hostStopOperation{segmentID: segment, handle: p, ctx: ctx, cancel: cancel, deadline: i.deadline, bounded: i.bounded, done: make(chan struct{})}
}

// stopMu is held. This is a bounded in-memory selection only. A retained
// command lease supplements the segment registry; it never replaces a foreign
// original handle or its first result/window. A terminal latch creates no stop.
func (h *Host) codexSegmentStopOperationsLocked(segment string, i *segmentStopIntent) []*hostStopOperation {
	ops := []*hostStopOperation{}
	seen := map[*hostStopOperation]bool{}
	add := func(op *hostStopOperation) {
		if op != nil && !seen[op] {
			seen[op] = true
			ops = append(ops, op)
		}
	}
	if i != nil {
		add(i.op)
	}
	for _, lease := range h.codexLeases {
		if lease.segment != segment || lease.handle == nil {
			continue
		}
		if lease.op == nil && i != nil && !i.terminal {
			if i.op == nil || i.op.handle == lease.handle {
				lease.op = h.bindStopLocked(segment, i, lease.handle, true)
			} else {
				lease.op = codexLeaseStopOperation(segment, lease.handle, i)
			}
		}
		add(lease.op)
	}
	return ops
}

func (h *Host) joinHostStopOperations(ops []*hostStopOperation, observe func(*hostStopOperation)) error {
	for _, op := range ops {
		h.startStop(op)
	}
	var result error
	for _, op := range ops {
		if err := joinStop(op, observe); err != nil {
			if result == nil {
				result = err
			} else {
				result = errors.Join(result, err)
			}
		}
	}
	return result
}
