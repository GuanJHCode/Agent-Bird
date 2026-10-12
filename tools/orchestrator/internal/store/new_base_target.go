package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

var newBaseTargetTexts = []string{"run_id", "task_id", "target_key", "target_json", "status", "created_at", "updated_at"}

// Accepted complete integration is certain even before collection acknowledgement.
// Other terminal states use the existing tree/decision/ancestor release fence.
const newBaseTargetReleaseSQL = `((t.status='completed'
 AND EXISTS(SELECT 1 FROM review_decisions d JOIN runtime_events e ON e.event_id=d.event_id AND e.event_revision=d.event_revision AND e.payload_hash=d.event_hash JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id JOIN budget_runtime b ON b.id=s.reservation_id AND b.segment_id=s.segment_id
 WHERE d.task_id=t.id AND d.decision='accept' AND s.status='exited' AND s.outcome='result' AND b.status='settled' AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1))
 AND NOT EXISTS(SELECT 1 FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')))
 OR (` + releasableTaskPredicate + `))`

func refreshNewBaseTargets(ctx context.Context, tx *sql.Tx) error {
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	_, err := tx.ExecContext(ctx, `UPDATE new_base_target_reservations SET status='released',updated_at=? WHERE status='reserved' AND task_id IN (SELECT t.id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE `+newBaseTargetReleaseSQL+`)`, stamp)
	return err
}
func (d *DB) retainNewBaseTarget(ctx context.Context, tx *sql.Tx, p PlanSpec, a *newBaseAdmission) error {
	c := a.context
	target := newBaseContextTarget(c, p.Run.PlanRevision)
	raw, _ := json.Marshal(target)
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	task := c.TaskMap[c.SourceIntegrationTaskID]
	texts := []string{p.Run.ID, task, target.key(), string(raw), "reserved", stamp, stamp}
	cost := int64(0)
	for _, s := range texts {
		cost += int64(len(s))
	}
	if err := d.storageAdmission(ctx, tx, p.Run.ID, cost, false); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO new_base_target_reservations(run_id,task_id,target_key,target_json,status,created_at,updated_at)VALUES(?,?,?,?,'reserved',?,?)`, p.Run.ID, task, target.key(), string(raw), stamp, stamp)
	return mapConflict(err)
}

// Authenticate every ticket against immutable lineage, including released history.
// Missing/mutated tickets must not disappear when ordinary scope enumeration
// excludes already authenticated new generations.
func activeNewBaseTargets(ctx context.Context, tx *sql.Tx) (map[string]dagReworkTarget, error) {
	var orphan int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM new_base_target_reservations n WHERE NOT EXISTS(SELECT 1 FROM new_base_lineage l WHERE l.run_id=n.run_id)`).Scan(&orphan); err != nil {
		return nil, err
	}
	if orphan != 0 {
		return nil, CodeError("new_base_target_reservation_invalid")
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.run_id,l.context_json,u.plan_revision,COALESCE(n.task_id,''),COALESCE(n.target_json,''),COALESCE(n.target_key,''),COALESCE(n.status,''),EXISTS(SELECT 1 FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=n.task_id AND `+newBaseTargetReleaseSQL+`) FROM new_base_lineage l JOIN runs u ON u.id=l.run_id LEFT JOIN new_base_target_reservations n ON n.run_id=l.run_id ORDER BY l.run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]dagReworkTarget{}
	for rows.Next() {
		var run, contextRaw, task, raw, key, status string
		var plan, releasable int
		var target dagReworkTarget
		var c adapter.NewBaseReworkContext
		if err = rows.Scan(&run, &contextRaw, &plan, &task, &raw, &key, &status, &releasable); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &target) != nil || json.Unmarshal([]byte(contextRaw), &c) != nil || c.RunID != run || c.TaskMap[c.SourceIntegrationTaskID] != task || key != target.key() || !sameTargetReservation(target, newBaseContextTarget(c, plan)) || (status != "reserved" && status != "released") || (status == "released" && releasable == 0) {
			return nil, CodeError("new_base_target_reservation_invalid")
		}
		if status == "reserved" {
			out[task] = target
		}
	}
	return out, rows.Err()
}
func newBaseContextTarget(c adapter.NewBaseReworkContext, plan int) dagReworkTarget {
	target := dagReworkTarget{CommonDir: c.SourceCommonDir}
	target.Binding.TargetWorktreeIdentity = c.TargetWorktree
	target.Binding.TargetRef = c.TargetRef
	target.Binding.TargetBaseOID = c.IntegratedHEAD
	target.Binding.TargetDirectoryIdentity = gitops.DirectoryIdentity(c.TargetDirectoryIdentity)
	target.Binding.PlanRevision = int64(plan)
	return target
}

// Ordinary plan paths cannot prove that two different target paths use distinct
// common repositories. Matching refs with such unresolved scope defer safely;
// different refs are demonstrably independent and retain existing admission.
func ordinaryNewBaseTargetAdmission(ctx context.Context, tx *sql.Tx, p PlanSpec) error {
	targets, err := activeNewBaseTargets(ctx, tx)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	for _, task := range p.Tasks {
		payload, err := adapter.DecodeInvocationPayload(task.AdapterPayload)
		if err != nil {
			continue
		}
		a := payload.CandidateAction
		if a == nil || a.Operation != "integrate" {
			continue
		}
		for _, reserved := range targets {
			if a.Target.Ref != reserved.Binding.TargetRef {
				continue
			}
			if a.Target.Worktree == reserved.Binding.TargetWorktreeIdentity {
				return CodeError("new_base_target_reserved")
			}
			return CodeError("new_base_target_scope_unresolved")
		}
	}
	return nil
}
func newBaseIntegrationDispatchAllowed(ctx context.Context, tx *sql.Tx, task readyTask) (bool, error) {
	if err := refreshNewBaseTargets(ctx, tx); err != nil {
		return false, err
	}
	targets, err := activeNewBaseTargets(ctx, tx)
	if err != nil {
		return false, err
	}
	var own int
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM new_base_lineage WHERE run_id=?)`, task.runID).Scan(&own) != nil {
		return false, ErrConflict
	}
	if own != 0 {
		if _, ok := targets[task.taskID]; !ok {
			return false, CodeError("new_base_target_reservation_released")
		}
		return true, nil
	}
	if len(targets) == 0 {
		return true, nil
	}
	p, err := adapter.DecodeInvocationPayload([]byte(task.payload))
	if err != nil {
		return false, ErrConflict
	}
	for _, target := range targets {
		if p.CandidateAction.Target.Ref != target.Binding.TargetRef {
			continue
		}
		if p.CandidateAction.Target.Worktree == target.Binding.TargetWorktreeIdentity {
			return false, CodeError("new_base_target_reserved")
		}
		return false, CodeError("new_base_target_scope_unresolved")
	}
	return true, nil
}

// The exact durable rows consulted by historical scope resolution. File reads
// occur in the read-only gate; changes to these facts invalidate that gate.
func integrationReservationFacts(ctx context.Context, tx *sql.Tx, task string) (string, error) {
	var run string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM tasks WHERE id=?`, task).Scan(&run); err != nil {
		return "", err
	}
	queries := []string{
		`SELECT t.*,r.* FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.run_id=? ORDER BY t.id`,
		`SELECT a.*,s.* FROM attempts a JOIN tasks t ON t.id=a.task_id LEFT JOIN segment_runtime s ON s.attempt_id=a.id WHERE t.run_id=? ORDER BY a.id,s.segment_id`,
		`SELECT d.*,b.* FROM retry_decisions d JOIN tasks t ON t.id=d.task_id LEFT JOIN decision_attempt_bindings b ON b.action_slot=d.action_slot WHERE t.run_id=? ORDER BY d.action_slot,b.task_id`,
		`SELECT d.* FROM review_decisions d JOIN tasks t ON t.id=d.task_id WHERE t.run_id=? ORDER BY d.command_id`,
		`SELECT e.event_id,e.event_revision,e.payload_hash,e.action_slot,e.segment_id,e.body_json FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=? ORDER BY e.event_id`,
	}
	var data []byte
	for _, q := range queries {
		rows, err := tx.QueryContext(ctx, q, run)
		if err != nil {
			return "", err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if err = rows.Scan(p...); err != nil {
				rows.Close()
				return "", err
			}
			raw, err := json.Marshal(v)
			if err != nil {
				rows.Close()
				return "", err
			}
			data = append(data, raw...)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return runtimeHash(string(data)), nil
}
