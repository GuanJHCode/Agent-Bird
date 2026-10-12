package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func candidateRunRetired(ctx context.Context, tx *sql.Tx, run string) error {
	var retired int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE sources(run_id) AS (SELECT ? UNION SELECT l.source_run_id FROM new_base_lineage l JOIN sources s ON l.run_id=s.run_id) SELECT EXISTS(SELECT 1 FROM candidate_run_retirements r JOIN sources s ON s.run_id=r.run_id)`, run).Scan(&retired); err != nil {
		return err
	}
	if retired != 0 {
		return CodeError("candidate_resource_retired")
	}
	return nil
}

// Called only inside the authenticated original Host event transaction, after
// runtime_events insertion. Late history is retained without adopting new
// resources into a run whose consumption authority has already been retired.
func (d *DB) registerCandidateResource(ctx context.Context, tx *sql.Tx, e contract.Event, historical bool, now string) error {
	if e.Kind != contract.EventCandidateResourceRegistered {
		return nil
	}
	bad := CodeError("candidate_resource_registration_invalid")
	var header gitops.CandidateResourceHeader
	if strictDiagnosticsJSON(e.CandidateResource, &header) != nil || header.Validate() != nil {
		return bad
	}
	canonical, _ := json.Marshal(header)
	if string(canonical) != string(e.CandidateResource) {
		return bad
	}
	source := header.Source
	b := source.Binding
	if source.HostID != e.ProducerID || source.CommandID != e.CommandID || b.RunID != e.RunID || b.TaskID != e.TaskID || b.AttemptID != e.AttemptID || b.SegmentID != e.SegmentID || b.WorkRevision != e.WorkRevision {
		return bad
	}
	var previous string
	err := tx.QueryRowContext(ctx, `SELECT header_json FROM candidate_resources WHERE id=?`, header.ResourceID).Scan(&previous)
	if err == nil {
		if previous != string(canonical) {
			return bad
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if historical {
		return nil
	}
	if err = candidateRunRetired(ctx, tx, e.RunID); err != nil {
		if err == CodeError("candidate_resource_retired") {
			return nil
		}
		return err
	}
	var continued int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM continuation_decisions WHERE target_segment_id=?)`, e.SegmentID).Scan(&continued); err != nil {
		return err
	}
	if continued != 0 {
		return bad
	}
	var payload, status, outcome, budget string
	var plan int
	err = tx.QueryRowContext(ctx, `SELECT t.adapter_payload,s.status,s.outcome,b.status,r.plan_revision FROM task_runtime t JOIN segment_runtime s ON s.segment_id=? JOIN budget_runtime b ON b.id=s.reservation_id JOIN runs r ON r.id=? WHERE t.task_id=?`, e.SegmentID, e.RunID, e.TaskID).Scan(&payload, &status, &outcome, &budget, &plan)
	if err != nil {
		return err
	}
	if status != "exited" || outcome != contract.EventResult || budget != "settled" || plan != b.PlanRevision {
		return bad
	}
	p, err := adapter.DecodeInvocationPayload([]byte(payload))
	if err != nil || p.GitFilter != nil {
		return bad
	}
	switch source.Kind {
	case "producer":
		if p.CandidateWorkspace == nil || p.CandidateWorkspace.Version != 1 || !p.CandidateWorkspace.AutoDirectory || p.CandidateWorkspace.Rework != nil || p.CandidateWorkspace.NewBaseRework != nil || p.CandidateAction != nil || b.RevisionSHA256 != "" || b.NewBaseSHA256 != "" {
			return bad
		}
	case "compose":
		if p.CandidateWorkspace != nil || p.CandidateAction == nil || !isV2Compose([]byte(payload)) || !p.CandidateAction.AutoDirectory || b.RevisionSHA256 != "" || b.NewBaseSHA256 != "" {
			return bad
		}
	default:
		return bad
	}
	rows, err := tx.QueryContext(ctx, `SELECT body_json,host_artifact_verified FROM runtime_events WHERE segment_id=? AND json_extract(body_json,'$.kind') IN ('result','exited','failed','stopped','unknown','question','launch_rejected') ORDER BY sequence LIMIT 3`, e.SegmentID)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var raw []byte
		var verified int
		var prior contract.Event
		if err = rows.Scan(&raw, &verified); err != nil {
			break
		}
		if len(raw) > 65536 || contract.DecodeOwnerRuntimeStrict(raw, &prior) != nil || prior.ProducerID != e.ProducerID || prior.RunID != e.RunID || prior.TaskID != e.TaskID || prior.AttemptID != e.AttemptID || prior.SegmentID != e.SegmentID || prior.CommandID != e.CommandID || prior.WorkRevision != e.WorkRevision || prior.Sequence >= e.Sequence || prior.Failure != nil || prior.StopReason != "" {
			err = bad
			break
		}
		if count == 0 && (prior.Kind != contract.EventResult || prior.Artifact == nil || prior.Artifact.HostArtifact == nil || verified != 1) || count == 1 && (prior.Kind != contract.EventExited || prior.ExitCode == nil || *prior.ExitCode != 0) || count > 1 {
			err = bad
			break
		}
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if count != 2 {
		return bad
	}
	incoming := int64(4096 + len(canonical))
	if err = d.storageAdmission(ctx, tx, e.RunID, incoming, false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_resources(id,run_id,task_id,attempt_id,segment_id,host_id,command_id,source_event_id,header_json,receipt_sha256,projection_sha256,created_at,state,state_revision,generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'available',1,0)`, header.ResourceID, e.RunID, e.TaskID, e.AttemptID, e.SegmentID, e.ProducerID, e.CommandID, e.EventID, string(canonical), header.ReceiptSHA256, source.ProjectionSHA256, now)
	return mapConflict(err)
}
