package store

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func (d *DB) continuationSummary(ctx context.Context, task string) (*ContinuationSourceBinding, bool, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT adapter_payload FROM task_runtime WHERE task_id=?`, task).Scan(&raw); err != nil {
		return nil, false, err
	}
	p, err := continuationPayload([]byte(raw))
	if p.Profile == nil || p.CandidateAction != nil || (p.Provider != "claude-code" && p.Provider != "antigravity-cli") {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var event, segment string
	if err = tx.QueryRowContext(ctx, `SELECT e.event_id,e.segment_id FROM runtime_events e JOIN segment_runtime r ON r.segment_id=e.segment_id AND r.host_id=e.producer_id AND r.command_id=json_extract(e.body_json,'$.command_id') WHERE e.task_id=? AND json_extract(e.body_json,'$.kind')='stopped' AND r.outcome='stopped' AND r.status='exited' AND e.segment_id=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) ORDER BY e.sequence DESC LIMIT 1`, task, task).Scan(&event, &segment); err != nil {
		return nil, true, CodeError("resume_source_unavailable")
	}
	e, sourceKind, err := readContinuationSource(ctx, tx, event, task, segment, false)
	if err != nil {
		return nil, true, err
	}
	if _, _, err = trustedContinuationRef(ctx, tx, e, contract.ContinuationInterrupted, sourceKind); err != nil {
		return nil, true, err
	}
	if err = checkContinuationBudget(ctx, tx, task, e.AttemptID); err != nil {
		return nil, true, err
	}
	return &ContinuationSourceBinding{ReviewableResultBinding: ReviewableResultBinding{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, WorkRevision: e.WorkRevision}, SegmentID: e.SegmentID}, true, nil
}
