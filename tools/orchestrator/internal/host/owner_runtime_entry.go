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
	if err = ipc.Write(cfg.Control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOriginalOwnerBound, RequestID: cfg.Bootstrap.Prepared.Intent.RequestID, Epoch: bound.Permit.CoordinatorEpoch, Payload: body}); err != nil {
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
	reader := lifecycle.readControl(ctx, 1, cfg.Control)
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
			notice, ok := lifecycle.takeNotice(1)
			if !ok || (!notice.offline && notice.pending == nil && notice.cause == nil) {
				continue
			}
			if err = recoverInitialOwnerTransport(ctx, cfg, enrollment, execution, lifecycle, recovery, notice); err != nil {
				cancel(err)
			}
		}
	}
	cause := context.Cause(ctx)
	if cause == nil {
		cause = context.Canceled
	}
	cleanupErr := lifecycle.cleanup(cause, []<-chan struct{}{reader, failures, origin, publisher})
	if cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	if errors.Is(cause, context.Canceled) {
		return nil
	}
	return cause
}

// recoverInitialOwnerTransport handles only the original physical source-host
// tuple.  A replacement child has a different proof and remains a separate
// driver.  The workflow is serial: reserve shared budget, re-establish the
// Coordinator, then ask the existing parent transport to reconnect before any
// fresh signed RPC can renew its permit.
func recoverInitialOwnerTransport(ctx context.Context, cfg OriginalOwnerConfig, enrollment *ownerEnrollment, execution *ownerExecution, lifecycle *ownerRuntimeLifecycle, recovery *ownerRecoveryState, notice ownerRuntimeNotice) error {
	if cfg.EnsureCoordinator == nil || (!notice.offline && notice.pending == nil && !ownerExecutionNeedsRecovery(notice.cause)) {
		return errOriginalOwnerRuntime
	}
	// The first transport is our parent, so an EOF alone proves nothing. Only a
	// bounded PID/birth observation proving that exact tuple changed can enter
	// the replacement-child path. Unknown and alive observations remain on the
	// same-tuple reconnect path and can never Start a replacement.
	if notice.offline {
		execution.mu.Lock()
		last := execution.last
		execution.mu.Unlock()
		if last != nil && last.permit.NewHost == execution.cfg.binding.FirstHost {
			observed, birthErr := process.BirthContext(ctx, last.permit.NewHost.PID)
			if birthErr != nil {
				return birthErr
			}
			if observed != last.permit.NewHost.Birth {
				return recoverDeadInitialTransport(ctx, cfg, enrollment, execution, lifecycle, recovery, last.permit)
			}
		}
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
		if err = ipc.Write(cfg.Control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "owner-reconnect", Payload: control}); err != nil {
			if transientSourceError(err) {
				continue
			}
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
			next, ok := lifecycle.takeNotice(1)
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
		if contract.DecodeOwnerRuntimeStrict(reply.Body, &state) != nil || state.Binding != execution.cfg.binding {
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

// recoverDeadInitialTransport creates one replacement only after the parent
// transport's physical tuple is proven dead. The persistent reservation is
// written before Start and the returned *exec.Cmd remains owned by the live
// owner; no PID reconstruction can create another child.
func recoverDeadInitialTransport(ctx context.Context, cfg OriginalOwnerConfig, enrollment *ownerEnrollment, execution *ownerExecution, lifecycle *ownerRuntimeLifecycle, recovery *ownerRecoveryState, current contract.OwnerControlPermit) error {
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
	child, startErr := startOwnerTransport(ctx, bootstrap, pendingRecord, os.Environ())
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
	// From successful Start onward this exact Cmd owns the only Wait. Register
	// it before any persistence/RPC can fail so cleanup never loses the child.
	lifecycle.retainTransportChild(child)
	if err = recovery.recordTransportStarted(child.identity); err != nil {
		return err
	}
	pending, err := recovery.ticketPending()
	if err != nil {
		return err
	}
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
	if err = ipc.Write(child.control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: pending.RequestID, Payload: raw}); err != nil {
		return err
	}
	// Generation fencing drops all notices from the dead parent reader before
	// accepting this child-owned pending session.
	reader := lifecycle.readControl(ctx, 2, child.control)
	_ = reader // lifecycle owns the FD; cleanup cancellation closes it.
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(5 * time.Second):
		return errCoordinatorUnavailable
	case <-lifecycle.wake:
		notice, ok := lifecycle.takeNotice(2)
		if !ok || notice.pending == nil {
			return errCoordinatorUnavailable
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
	return execution.reconcile(ctx, permit)
}
