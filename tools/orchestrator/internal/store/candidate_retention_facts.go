package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type candidateRetentionRow struct {
	id, run, task, attempt, segment, host, command, raw, created, state string
	revision, generation                                                int
	header                                                              gitops.CandidateResourceHeader
}

func loadCandidateRetention(ctx context.Context, tx *sql.Tx, run, id string) (r candidateRetentionRow, err error) {
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,task_id,attempt_id,segment_id,host_id,command_id,header_json,created_at,state,state_revision,generation FROM candidate_resources WHERE run_id=? AND id=?`, run, id).Scan(&r.id, &r.run, &r.task, &r.attempt, &r.segment, &r.host, &r.command, &r.raw, &r.created, &r.state, &r.revision, &r.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return r, CodeError("candidate_resource_unavailable")
	}
	if err != nil {
		return
	}
	if strictDiagnosticsJSON([]byte(r.raw), &r.header) != nil || r.header.Validate() != nil {
		return r, CodeError("candidate_retention_state_invalid")
	}
	b, _ := json.Marshal(r.header)
	s := r.header.Source
	if string(b) != r.raw || r.id != r.header.ResourceID || s.Binding.RunID != r.run || s.Binding.TaskID != r.task || s.Binding.AttemptID != r.attempt || s.Binding.SegmentID != r.segment || s.HostID != r.host || s.CommandID != r.command {
		return r, CodeError("candidate_retention_state_invalid")
	}
	return
}

// Retirement is run-wide: every current reader must finish before the permanent
// SQL fences close all future attempts, continuation and cross-run new-base use.
func candidateRunQuiescent(ctx context.Context, tx *sql.Tx, run string) error {
	var unsafe int
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM tasks WHERE run_id=? AND status NOT IN ('completed','failed','cancelled'))
 OR EXISTS(SELECT 1 FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segments s ON s.attempt_id=a.id LEFT JOIN segment_runtime sr ON sr.segment_id=s.id LEFT JOIN budget_runtime b ON b.segment_id=s.id WHERE t.run_id=? AND (s.status NOT IN ('exited','not_started') OR sr.status IS NULL OR sr.status!=s.status OR b.status IS NULL OR b.status!='settled'))
 OR EXISTS(SELECT 1 FROM runtime_questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=? AND q.status IN ('open','resume_queued','resume_started'))
 OR EXISTS(SELECT 1 FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=? AND e.delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.run_id=? AND e.delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM managed_artifact_intakes WHERE run_id=? AND phase='prepared')
 OR EXISTS(SELECT 1 FROM continuation_decisions c LEFT JOIN segment_runtime s ON s.segment_id=c.target_segment_id WHERE c.run_id=? AND (c.status='queued' OR s.status IS NULL OR s.status NOT IN ('exited','not_started')))
 OR EXISTS(SELECT 1 FROM decision_attempt_bindings d JOIN tasks t ON t.id=d.task_id WHERE t.run_id=? AND d.attempt_id IS NULL AND d.retired=0)
 OR EXISTS(SELECT 1 FROM new_base_lineage l JOIN tasks t ON t.run_id=l.run_id WHERE l.source_run_id=? AND (t.status NOT IN ('completed','failed','cancelled') OR EXISTS(SELECT 1 FROM attempts a JOIN segments s ON s.attempt_id=a.id LEFT JOIN segment_runtime sr ON sr.segment_id=s.id LEFT JOIN budget_runtime b ON b.segment_id=s.id WHERE a.task_id=t.id AND (s.status NOT IN ('exited','not_started') OR sr.status IS NULL OR sr.status!=s.status OR b.status IS NULL OR b.status!='settled'))))`, run, run, run, run, run, run, run, run, run).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe != 0 {
		return CodeError("candidate_retention_pinned")
	}
	return nil
}
func candidateRetentionFacts(ctx context.Context, tx *sql.Tx, r candidateRetentionRow, binding string, now time.Time) (string, error) {
	if r.state != "available" && r.state != "pending" {
		return "", CodeError("candidate_resource_unavailable")
	}
	if err := candidateRunQuiescent(ctx, tx, r.run); err != nil {
		return "", err
	}
	terminal, err := loadHostTerminalFacts(ctx, tx, hostRetentionRow{run: r.run, task: r.task, attempt: r.attempt, segment: r.segment, producer: r.host})
	if err != nil {
		return "", err
	}
	cutoff, err := hostRetentionTime(r.created)
	if err != nil {
		return "", err
	}
	for _, at := range []time.Time{terminal.received, terminal.consumed} {
		if at.After(cutoff) {
			cutoff = at
		}
	}
	// Include source and all existing downstream runs. Both received lifecycle and
	// consumption timestamps extend the retention period when a reader ends late.
	rows, err := tx.QueryContext(ctx, `SELECT e.event_id,e.created_at,COALESCE(d.created_at,'') FROM runtime_events e JOIN tasks t ON t.id=e.task_id LEFT JOIN runtime_decisions d ON d.event_id=e.event_id AND d.event_revision=e.event_revision AND d.event_hash=e.payload_hash AND d.action_slot=e.action_slot WHERE (t.run_id=? OR t.run_id IN (SELECT run_id FROM new_base_lineage WHERE source_run_id=?)) AND json_extract(e.body_json,'$.kind') IN ('exited','launch_rejected','result','question','failed') ORDER BY e.event_id,d.created_at LIMIT 4097`, r.run, r.run)
	if err != nil {
		return "", err
	}
	vector := [][]string{}
	for rows.Next() {
		var id, received, consumed string
		if err = rows.Scan(&id, &received, &consumed); err != nil {
			break
		}
		if len(vector) >= 4096 {
			err = CodeError("candidate_retention_terminal_unverified")
			break
		}
		for _, s := range []string{received, consumed} {
			if s == "" {
				continue
			}
			var at time.Time
			at, err = hostRetentionTime(s)
			if err != nil {
				break
			}
			if at.After(cutoff) {
				cutoff = at
			}
		}
		if err != nil {
			break
		}
		vector = append(vector, []string{id, received, consumed})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", err
	}
	var failed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE run_id=? AND status!='completed')`, r.run).Scan(&failed); err != nil {
		return "", err
	}
	resolution := ""
	ttl := 30 * 24 * time.Hour
	if failed || !terminal.success {
		var at string
		err = tx.QueryRowContext(ctx, `SELECT json_array(controller,token_hash,binding_hash,request_id,reason,resolution,created_at),created_at FROM candidate_run_resolutions WHERE run_id=? AND binding_hash=?`, r.run, binding).Scan(&resolution, &at)
		if errors.Is(err, sql.ErrNoRows) {
			return "", CodeError("candidate_retention_unresolved")
		}
		if err != nil {
			return "", err
		}
		resolved, e := hostRetentionTime(at)
		if e != nil {
			return "", e
		}
		if resolved.After(cutoff) {
			cutoff = resolved
		}
		ttl = 7 * 24 * time.Hour
	}
	if !cutoff.Add(ttl).Before(now) {
		return "", CodeError("candidate_retention_not_expired")
	}
	raw, _ := json.Marshal([]any{r.raw, r.state, r.revision, r.generation, binding, terminal.vector, terminal.success, vector, resolution, cutoff.UTC().Format(time.RFC3339Nano)})
	return runtimeHash(string(raw)), nil
}
func candidateRetentionExclusion(err error) bool {
	switch err.Error() {
	case "candidate_resource_unavailable", "candidate_retention_pinned", "candidate_retention_unresolved", "candidate_retention_not_expired", "candidate_retention_terminal_unverified", "host_retention_terminal_unverified", "host_retention_unconsumed":
		return true
	}
	return false
}
