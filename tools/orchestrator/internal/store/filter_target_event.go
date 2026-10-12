package store

import (
	"context"
	"database/sql"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func filterTargetTask(ctx context.Context, tx *sql.Tx, task string) (bool, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT adapter_payload FROM task_runtime WHERE task_id=?`, task).Scan(&raw); err != nil {
		return false, err
	}
	p, err := adapter.DecodeInvocationPayload(raw)
	return err == nil && (adapter.IsFilterTargetPreflight(p) || adapter.IsFilterIntegrate(p)), nil
}

// Only this persisted action can report success without a Provider process.
// Content remains Host-owned; Store authenticates the event and artifact slot.
func validateFilterTargetHostEvent(ctx context.Context, tx *sql.Tx, e contract.Event, verified bool) error {
	target, err := filterTargetTask(ctx, tx, e.TaskID)
	if err != nil || !target {
		return err
	}
	if e.Process != nil || e.ExitCode != nil || e.SessionID != "" || e.SessionKind != "" {
		return CodeError("filter_target_process_forbidden")
	}
	switch e.Kind {
	case contract.EventPrepared, contract.EventUnknown, contract.EventLaunchRejected, contract.EventFailed:
	case contract.EventStopped:
		if e.StopReason == "" {
			return CodeError("invalid_stop_reason")
		}
	case contract.EventResult:
		if !verified || e.Artifact == nil || e.Artifact.HostArtifact == nil {
			return CodeError("filter_target_result_unverified")
		}
	case contract.EventExited:
		if e.GitFilterCleanup == nil || e.GitFilterCleanup.Validate() != nil || e.ActiveMS < 1 {
			return CodeError("filter_target_terminal_unproved")
		}
	default:
		return CodeError("filter_target_process_forbidden")
	}
	return nil
}
