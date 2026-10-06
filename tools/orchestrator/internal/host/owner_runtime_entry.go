package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// OriginalOwnerConfig is the deliberately small process boundary for the
// original owner.  The bootstrap arrives on FD3 and Control is the inherited
// FD4 socketpair.  Neither a launch token nor a coordinator connection can be
// supplied here: those remain in the transport parent.
type OriginalOwnerConfig struct {
	Bootstrap          contract.OwnerRuntimeBootstrap
	Control            *net.UnixConn
	InvocationProvider func(context.Context, contract.LaunchCommand) (contract.InvocationView, error)
	EnsureCoordinator  func(context.Context) error
}

var errOriginalOwnerRuntime = errors.New("original_owner_runtime_rejected")

// Only the serial owner workflow changes this current transport. A nil child
// denotes the inherited first parent, for which the owner cannot invent Wait.
// Every replacement retains the exact launcher object and every reader join.
type ownerRuntimeTransport struct {
	generation uint64
	control    *net.UnixConn
	identity   contract.OriginalOwnerTransport
	child      *ownerTransportChild
	readers    []<-chan struct{}
}

func (d *ownerRuntimeTransport) read(ctx context.Context, lifecycle *ownerRuntimeLifecycle) {
	d.readers = append(d.readers, lifecycle.readControl(ctx, d.generation, d.control))
}

// RunOriginalOwner creates the sole execution owner for a prepared initial
// transport.  It binds a memory-only owner key over a fresh kernel-authenticated
// RPC, relays the signed binding to the initial transport, then starts the
// control reader before applied/activate can admit work.
func RunOriginalOwner(parent context.Context, cfg OriginalOwnerConfig) error {
	if cfg.Control == nil || cfg.InvocationProvider == nil || cfg.Bootstrap.Validate() != nil {
		return errOriginalOwnerRuntime
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)
	defer cfg.Control.Close()

	enrollment, err := newOwnerEnrollment(ctx, cfg.Bootstrap)
	if err != nil {
		return err
	}
	bound, err := enrollment.bind(ctx)
	if err != nil {
		return err
	}
	// The initial transport must verify this complete signed reply before it
	// forwards any command.  Sending it before opening the reader also means the
	// parent can retain its already-authenticated HostHello session.
	body, err := json.Marshal(bound)
	if err != nil {
		return err
	}
	if err = writeOwnerTransportFrame(ctx, cfg.Control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOriginalOwnerBound, RequestID: cfg.Bootstrap.Prepared.Intent.RequestID, Epoch: bound.Permit.CoordinatorEpoch, Payload: body}); err != nil {
		return err
	}
	directories, err := openOwnerRuntimeDirectories(cfg.Bootstrap)
	if err != nil {
		return err
	}
	defer directories.close()
	if err = directories.verify(); err != nil {
		return err
	}
	h, err := NewIPC(cfg.Bootstrap.SpoolRoot, cfg.Bootstrap.Prepared.Intent.Scope.ProducerID)
	if err != nil {
		return err
	}
	if err = directories.verify(); err != nil {
		return err
	}
	execution, err := newOwnerExecution(ctx, ownerExecutionConfig{
		host:           h,
		binding:        bound.Binding,
		privateKey:     enrollment.client.PrivateKey,
		coordinatorKey: enrollment.client.CoordinatorKey,
		reportSocket:   cfg.Bootstrap.Prepared.RPCSocketPath,
		invocation:     cfg.InvocationProvider,
		rpc:            enrollment.client.Call,
	})
	if err != nil {
		return err
	}
	lifecycle := newOwnerRuntimeLifecycle(execution, cancel)
	recoveryScope, err := originalOwnerRecoveryScope(bound.Binding.Scope, bound.Binding.Owner)
	if err != nil {
		return err
	}
	recovery, err := openOwnerRecovery(cfg.Bootstrap.SpoolRoot, recoveryScope)
	if err != nil {
		return err
	}
	defer recovery.close()
	// Replacement-stage writes occur immediately after each durable signed RPC,
	// so Applied commit and Activate ACK-loss remain distinguishable on disk.
	execution.cfg.onApplied = func(permit contract.OwnerControlPermit, applied contract.OwnerPermitApplied) error {
		if recovery.record.OwnerPending == nil || recovery.record.OwnerPending.Phase != "attached" || recovery.record.OwnerPending.PermitSHA256 == "" {
			return nil
		}
		return recovery.markTransportApplied(applied)
	}
	execution.cfg.onActivated = func(permit contract.OwnerControlPermit, applied contract.OwnerPermitApplied) error {
		if recovery.record.OwnerPending == nil || recovery.record.OwnerPending.Phase != "applied" {
			return nil
		}
		return recovery.markTransportActivated(applied)
	}
	transport := &ownerRuntimeTransport{generation: 1, control: cfg.Control, identity: bound.Binding.FirstHost}
	transport.read(ctx, lifecycle)
	failures := lifecycle.watchFailures(ctx)
	origin := lifecycle.watchOrigin(ctx, bound.Binding.Scope)
	publisher := lifecycle.publish(ctx)
	// Reconcile on the same serial owner workflow only after the FD4 reader is
	// live, so a signed launch queued across the Activate reply is retained.
	if err = execution.reconcile(ctx, bound.Permit); err != nil {
		cancel(err)
	}
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-lifecycle.wake:
			// A transport notice is merely a hint.  Replacement is driven by the
			// owner recovery ledger, never by this reader or a stale connection.
			notice, ok := lifecycle.takeNotice(transport.generation)
			if !ok || (!notice.offline && notice.pending == nil && notice.cause == nil) {
				continue
			}
			if err = recoverCurrentOwnerTransport(ctx, cfg, enrollment, execution, lifecycle, recovery, transport, notice); err != nil {
				cancel(err)
			}
		}
	}
	cause := context.Cause(ctx)
	if cause == nil {
		cause = context.Canceled
	}
	cleanupErr := lifecycle.cleanup(cause, append(transport.readers, failures, origin, publisher))
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	if errors.Is(cause, context.Canceled) {
		return nil
	}
	return cause
}

// Recovery always follows the current transport's generation, FD and physical
// identity. Replacement children additionally require their original Wait
// before the persistent Start reservation can be retired.
func recoverCurrentOwnerTransport(ctx context.Context, cfg OriginalOwnerConfig, enrollment *ownerEnrollment, execution *ownerExecution, lifecycle *ownerRuntimeLifecycle, recovery *ownerRecoveryState, transport *ownerRuntimeTransport, notice ownerRuntimeNotice) error {
	if cfg.EnsureCoordinator == nil || (!notice.offline && notice.pending == nil && !ownerExecutionNeedsRecovery(notice.cause)) {
		return errOriginalOwnerRuntime
	}
	execution.mu.Lock()
	last := execution.last
	execution.mu.Unlock()
	if last == nil || last.permit.NewHost != transport.identity || notice.generation != transport.generation ||
		(transport.child == nil && transport.identity != execution.cfg.binding.FirstHost) {
		return errOriginalOwnerRuntime
	}
	// Socket EOF is not an exit proof. Unknown observations fail closed; an
	// alive tuple can only reconnect, never allocate a replacement Start.
	exited, observeErr := process.IdentityExitedContext(ctx, transport.identity.PID, transport.identity.Birth)
	if observeErr != nil {
		return observeErr
	}
	if exited {
		if transport.child != nil {
			waitCtx, stopWait := context.WithTimeout(ctx, time.Second)
			_ = transport.child.wait(waitCtx) // A killed transport has a real nonzero Wait.
			stopWait()
			if err := recovery.retireTransportAfterWait(transport.child); err != nil {
				return err
			}
			if err := lifecycle.forgetWaitedTransportChild(transport.child); err != nil {
				return err
			}
		}
		_ = transport.control.Close()
		return recoverDeadOwnerTransport(ctx, cfg, enrollment, execution, lifecycle, recovery, transport, last.permit)
	}
	// This local value is deliberately retained across lost RPC replies.  The
	// same request ID/revision/body is replayed after a new Coordinator epoch;
	// it is never replaced by a second renewal while its ACK is uncertain.
	var renewal *contract.OwnerAttachRequest
	var requestID string
	for {
		when, err := recovery.reserveCoordinator(time.Now())
		if err != nil {
			return err
		}
		if delay := time.Until(when); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
		}
		if err = recovery.beginCoordinatorEnsure(); err != nil {
			return err
		}
		execution.pausePublication() // before Ensure/reconnect/Attach RPCs
		err = cfg.EnsureCoordinator(ctx)
		finishErr := recovery.finishCoordinatorEnsure()
		if finishErr != nil {
			return finishErr
		}
		if err != nil {
			if transientSourceError(err) {
				continue
			}
			return err
		}
		control, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect"})
		if err = writeOwnerTransportFrame(ctx, transport.control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "owner-reconnect", Payload: control}); err != nil {
			// A failed local control write may have sent a partial frame. This
			// inherited stream is retired; reconnecting the Coordinator does
			// not create a fresh FD4 on which it would be safe to retry.
			return err
		}
		// The reader reports the authenticated pending session on the next wake.
		pending := false
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
			continue
		case <-lifecycle.wake:
			next, ok := lifecycle.takeNotice(transport.generation)
			pending = ok && next.pending != nil
		}
		if !pending {
			continue
		}
		// A fresh state RPC proves the new Coordinator epoch.  The outer signed
		// reply may have that epoch while its durable permit is still the old
		// revision, so state must be authenticated with the last verified permit.
		execution.mu.Lock()
		last := execution.last
		execution.mu.Unlock()
		if last == nil {
			return errOriginalOwnerRuntime
		}
		// It is legal only
		// after the parent transport has completed its ordinary HostHello.
		reply, callErr := enrollment.client.Call(ctx, last.permit.ControlRevision, "state", cfg.Bootstrap.Prepared.Intent.RequestID, struct{}{})
		if callErr != nil {
			if transientSourceError(callErr) {
				continue
			}
			return callErr
		}
		var state contract.OwnerRuntimeState
		if contract.DecodeOwnerRuntimeStrict(reply.Body, &state) != nil || state.Binding != execution.cfg.binding || state.Permit.NewHost != transport.identity {
			return errOriginalOwnerRuntime
		}
		// A transport-only EOF does not create a new Coordinator epoch.  Its
		// existing permit is still the exact durable authority: replay applied /
		// activate through reconcile and never manufacture an Attach revision.
		if reply.CoordinatorEpoch == state.Permit.CoordinatorEpoch {
			return execution.reconcile(ctx, state.Permit)
		}
		if renewal == nil {
			old := state.Permit.NewHost
			value := contract.OwnerAttachRequest{ExpectedControlRevision: state.Permit.ControlRevision, OldHost: old, NewHost: old}
			renewal = &value
			requestID = fmt.Sprintf("%s:renew:%d", state.Permit.PermitID, reply.CoordinatorEpoch)
		}
		reply, callErr = enrollment.client.Call(ctx, renewal.ExpectedControlRevision, "attach", requestID, *renewal)
		if callErr != nil {
			if transientSourceError(callErr) {
				continue
			}
			return callErr
		}
		permit, decodeErr := contract.DecodeOwnerControlPermit(reply.Body)
		if decodeErr != nil {
			return errOriginalOwnerRuntime
		}
		// An ACK-lost Attach can be replayed after a hard restart and returns its
		// historical permit (old epoch).  Do not install that stale permit over
		// the new challenge; clear only the local replay marker and issue a new
		// same-tuple renewal on the current epoch.
		if permit.CoordinatorEpoch < reply.CoordinatorEpoch {
			if err = execution.rememberHistoricalPermit(permit); err != nil {
				return err
			}
			renewal, requestID = nil, ""
			continue
		}
		return execution.reconcile(ctx, permit)
	}
}

// recoverDeadOwnerTransport creates one replacement only after the current
// transport's physical tuple is proven dead. The persistent reservation is
// written before Start and the returned *exec.Cmd remains owned by the live
// owner; no PID reconstruction can create another child.
func recoverDeadOwnerTransport(ctx context.Context, cfg OriginalOwnerConfig, enrollment *ownerEnrollment, execution *ownerExecution, lifecycle *ownerRuntimeLifecycle, recovery *ownerRecoveryState, transport *ownerRuntimeTransport, current contract.OwnerControlPermit) error {
	return recoverDeadOwnerTransportWithStart(ctx, cfg, enrollment, execution, lifecycle, recovery, transport, current, startOwnerTransport)
}

// The private launch seam preserves the production recovery sequence while
// exercising failures after the kernel has already created the owned child.
func recoverDeadOwnerTransportWithStart(ctx context.Context, cfg OriginalOwnerConfig, enrollment *ownerEnrollment, execution *ownerExecution, lifecycle *ownerRuntimeLifecycle, recovery *ownerRecoveryState, transport *ownerRuntimeTransport, current contract.OwnerControlPermit, start func(context.Context, contract.OwnerTransportBootstrap, ownerRecoveryPending, []string) (*ownerTransportChild, error)) error {
	requestID := fmt.Sprintf("%s:transport:%d", current.PermitID, current.ControlRevision)
	when, err := recovery.reserveTransport(time.Now(), requestID, current.ControlRevision, current.NewHost)
	if err != nil {
		return err
	}
	if delay := time.Until(when); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-timer.C:
		}
	}
	if err = recovery.beginTransportStart(); err != nil {
		return err
	}
	pendingRecord := *recovery.record.OwnerPending
	bootstrap := contract.OwnerTransportBootstrap{Version: 1, StateDir: cfg.Bootstrap.StateDir, Intent: cfg.Bootstrap.Prepared.Intent, Owner: execution.cfg.binding.Owner, CoordinatorPublicKey: cfg.Bootstrap.Prepared.CoordinatorPublicKey}
	child, startErr := start(ctx, bootstrap, pendingRecord, os.Environ())
	// Successful kernel Start owns a Cmd/Wait even when identity or bootstrap
	// observation subsequently fails. Retain it before handling that error.
	lifecycle.retainTransportChild(child)
	if startErr != nil {
		if child == nil {
			if retireErr := recovery.retireTransportNoStart(startErr); retireErr != nil {
				return retireErr
			}
			return startErr
		}
		// Post-Start uncertainty is durable: retain this exact child and do not
		// consume a second Start slot. Its Wait stays live until cleanup.
		return startErr
	}
	if err = recovery.recordTransportStarted(child.identity); err != nil {
		return err
	}
	transport.generation++
	transport.control, transport.identity, transport.child = child.control, child.identity, child
	transport.read(ctx, lifecycle)
	pending, err := recovery.ticketPending()
	if err != nil {
		return err
	}
	execution.pausePublication()
	// Ticket and Attach are independently signed by the existing owner key.
	// The new child can exchange only this exact ticket for the escrowed token.
	reply, err := enrollment.client.Call(ctx, current.ControlRevision, "ticket", pending.RequestID, pending)
	if err != nil {
		return err
	}
	ticket, err := contract.DecodeOwnerTransportTicket(reply.Body)
	if err != nil || ticket.Pending != pending {
		return errOriginalOwnerRuntime
	}
	raw, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect", Ticket: &ticket})
	if err = writeOwnerTransportFrame(ctx, child.control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: pending.RequestID, Payload: raw}); err != nil {
		return err
	}
	// Ignore stale wake tokens while the generation fence excludes old readers.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
pendingSession:
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-deadline.C:
			return errCoordinatorUnavailable
		case <-lifecycle.wake:
			notice, ok := lifecycle.takeNotice(transport.generation)
			if !ok {
				continue
			}
			if notice.pending == nil {
				return errCoordinatorUnavailable
			}
			break pendingSession
		}
	}
	attach := contract.OwnerAttachRequest{ExpectedControlRevision: current.ControlRevision, OldHost: pending.OldHost, NewHost: pending.NewHost, Pending: &pending}
	reply, err = enrollment.client.Call(ctx, current.ControlRevision, "attach", pending.RequestID, attach)
	if err != nil {
		return err
	}
	permit, err := contract.DecodeOwnerControlPermit(reply.Body)
	if err != nil {
		return errOriginalOwnerRuntime
	}
	if err = recovery.markTransportAttached(permit); err != nil {
		return err
	}
	if err = execution.reconcile(ctx, permit); err != nil {
		return err
	}
	return nil
}
