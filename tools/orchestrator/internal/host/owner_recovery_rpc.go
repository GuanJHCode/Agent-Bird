package host

import (
	"context"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Only passive RPC replay is permitted here. This does not Ensure a Coordinator,
// start another Host, reserve a recovery slot, or refresh the startup deadline.
func (e *ownerEnrollment) handoffRecoveryWith(parent context.Context, b contract.OwnerBindReply, rpc ownerExecutionRPC) error {
	if e == nil || rpc == nil || e.client.Intent != e.bootstrap.Prepared.Intent || verifyOwnerEnrollment(e.bootstrap, e.client.Owner, e.client.CoordinatorKey, b) != nil {
		return ownerRecoveryRejected()
	}
	if b.Binding.Scope.Incarnation == 1 {
		return nil
	}
	id, err := contract.OwnerRecoveryScopeRequestID(b.Binding.Scope, b.Binding.Owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(e.bootstrap.Prepared.Intent.StartupDeadlineUnixMS))
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		op, stop := context.WithTimeout(ctx, 5*time.Second)
		reply, callErr := rpc(op, 1, "recovery_scope", id, contract.OwnerRecoveryScopeRequest{Version: 1})
		stop()
		if callErr == nil {
			var status contract.OwnerRecoveryScopeReply
			if reply.Version != 1 || reply.OwnerID != b.Binding.Scope.OwnerID || reply.RequestID != id || reply.CoordinatorEpoch < b.Permit.CoordinatorEpoch || reply.Error != "" || contract.DecodeOwnerRuntimeStrict(reply.Body, &status) != nil || status.Version != 1 || status.Status != "recovery_scope_applied" {
				return ownerRecoveryRejected()
			}
			return nil
		}
		if reply.Version != 0 || !transientSourceError(callErr) {
			return callErr
		}
		err = callErr
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func openBoundOwnerRecovery(stateDir, spoolRoot string, b contract.OwnerBindReply) (*ownerRecoveryState, func(), error) {
	scope, err := originalOwnerRecoveryScope(b.Binding.Scope, b.Binding.Owner)
	if err != nil {
		return nil, nil, err
	}
	if b.Binding.Scope.Incarnation == 1 {
		recovery, err := openOwnerRecovery(spoolRoot, scope)
		if err != nil {
			return nil, nil, err
		}
		return recovery, recovery.close, nil
	}
	// A successor must never recreate a missing ledger after a handoff ACK.
	lease, err := OpenExistingOwnerRecoveryLease(stateDir, b.Binding.Scope, b.Binding.Owner)
	if err != nil {
		return nil, nil, err
	}
	return lease.recovery, lease.Close, nil
}
