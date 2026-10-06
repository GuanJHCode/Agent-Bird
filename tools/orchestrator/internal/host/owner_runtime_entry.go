package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"

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
	h, err := NewIPC(cfg.Bootstrap.SpoolRoot, cfg.Bootstrap.Prepared.Intent.Scope.ProducerID)
	if err != nil {
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
			_, _ = lifecycle.takeNotice(1)
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
