package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
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
	when, err := recovery.reserveCoordinator(time.Now())
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
	if err = recovery.beginCoordinatorEnsure(); err != nil {
		return err
	}
	if err = cfg.EnsureCoordinator(ctx); err != nil {
		return err
	}
	if err = recovery.finishCoordinatorEnsure(); err != nil {
		return err
	}
	execution.pausePublication()
	control, _ := json.Marshal(contract.OwnerTransportControl{Version: 1, Action: "connect"})
	if err = ipc.Write(cfg.Control, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "owner-reconnect", Payload: control}); err != nil {
		return err
	}
	// The reader reports the authenticated pending session on the next wake.
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(5 * time.Second):
			return errCoordinatorUnavailable
		case <-lifecycle.wake:
			next, ok := lifecycle.takeNotice(1)
			if !ok || next.pending == nil {
				continue
			}
		}
		// A fresh state RPC proves the new Coordinator epoch.  It is legal only
		// after the parent transport has completed its ordinary HostHello.
		reply, callErr := enrollment.client.Call(ctx, 0, "state", cfg.Bootstrap.Prepared.Intent.RequestID, struct{}{})
		if callErr != nil {
			continue
		}
		var state contract.OwnerRuntimeState
		if contract.DecodeOwnerRuntimeStrict(reply.Body, &state) != nil || state.Binding != execution.cfg.binding {
			return errOriginalOwnerRuntime
		}
		old := state.Permit.NewHost
		requestID := fmt.Sprintf("%s:renew:%d", state.Permit.PermitID, reply.CoordinatorEpoch)
		attach := contract.OwnerAttachRequest{ExpectedControlRevision: state.Permit.ControlRevision, OldHost: old, NewHost: old}
		reply, callErr = enrollment.client.Call(ctx, state.Permit.ControlRevision, "attach", requestID, attach)
		if callErr != nil {
			return callErr
		}
		permit, decodeErr := contract.DecodeOwnerControlPermit(reply.Body)
		if decodeErr != nil {
			return errOriginalOwnerRuntime
		}
		return execution.reconcile(ctx, permit)
	}
}
