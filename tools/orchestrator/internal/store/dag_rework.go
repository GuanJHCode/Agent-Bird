package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func (d *DB) queueDAGRework(ctx context.Context, spec, canonical ReworkSpec) (ReworkReceipt, error) {
	empty := ReworkReceipt{}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer owned.close()
	tx := owned.tx
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT revision_payload FROM retry_decisions WHERE action_slot=?`, spec.ActionSlot).Scan(&previous)
	if err == nil {
		saved, err := parseDAGRework(previous)
		if err != nil || !reflect.DeepEqual(saved.Spec, canonical) {
			return empty, ErrConflict
		}
		r, err := readAttemptDecision(ctx, tx, spec.ActionSlot)
		if err != nil {
			return empty, err
		}
		if _, err = dagReworkDecisionTargets(ctx, tx, r); err != nil {
			return empty, err
		}
		return saved.Receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var occupied int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM retry_decisions WHERE source_event_id=? OR command_id=?`, spec.Review.EventID, spec.CommandID).Scan(&occupied); err != nil {
		return empty, err
	}
	if occupied != 0 {
		return empty, ErrConflict
	}
	all, raw, run, plan, err := loadDAGReworkTasks(ctx, tx, spec.TaskID)
	if err != nil {
		return empty, err
	}
	chain, err := dagClosure(all, spec.TaskID)
	if err != nil {
		return empty, err
	}
	if err = validateDAGReworkTasks(chain, raw, spec); err != nil {
		return empty, err
	}
	if err = reworkBudgetsAndLifecycle(ctx, tx, chain); err != nil {
		return empty, err
	}
	// Unsettled historical reservations cannot be reset by changing revision.
	for _, t := range chain {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM budget_runtime b JOIN segments z ON z.id=b.segment_id JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=? AND b.status!='settled'`, t.id).Scan(&occupied); err != nil {
			return empty, err
		}
		if occupied != 0 {
			return empty, ErrConflict
		}
		if err = checkTaskProvider(ctx, tx, t.id, nil); err != nil {
			return empty, err
		}
	}
	feedbackEvent, feedback, err := readReworkEvent(ctx, tx, all[spec.ReviewTaskID], spec.Review, false)
	if err != nil || feedback.Review == nil || feedbackEvent.ActionSlot != spec.ActionSlot {
		return empty, ErrConflict
	}
	context := adapter.ReworkContext{Version: 1, RunID: run, TaskID: spec.TaskID, PreviousWorkRevision: spec.WorkRevision, PreviousCandidateOID: spec.CandidateOID, CandidateEventID: spec.Candidate.EventID, CandidateSHA256: spec.Candidate.EventHash, ReviewEventID: spec.Review.EventID, ReviewSHA256: spec.Review.EventHash, ReviewSummary: feedback.Review.Summary, Feedback: spec.Feedback, Acceptance: spec.Acceptance}
	saved := dagReworkDecision{Spec: canonical, Context: context, Receipt: ReworkReceipt{Version: 1, Status: "rework_queued", TaskID: spec.TaskID, WorkRevision: spec.WorkRevision + 1, CandidateOID: spec.CandidateOID, ReviewEventID: spec.Review.EventID, RevisionSHA256: context.Digest(), TaskRevisions: map[string]int{}}, Evidence: map[string]dagReworkEvidence{}, Boundary: map[string]contract.AcceptedInput{}, Targets: map[string]dagReworkTarget{}}
	if err = gatherDAGEvidence(ctx, tx, &saved, chain, all, raw); err != nil {
		return empty, err
	}
	artifacts, err := validateDAGEvidence(ctx, tx, saved, plan)
	if err != nil {
		return empty, err
	}
	for _, t := range chain {
		revised, err := revisedDAGAdapter(raw[t.id], t.id, t.revision, context)
		if err != nil {
			return empty, err
		}
		saved.Nodes = append(saved.Nodes, dagReworkNode{TaskID: t.id, Revision: t.revision, Dependencies: append([]string{}, t.dependencies...), Original: raw[t.id], Revised: revised})
		saved.Receipt.TaskRevisions[t.id] = t.revision + 1
		if t.action != nil {
			artifact, ok := artifacts[t.id]
			if !ok {
				// Unexecuted descendants can only reuse the exact authorizing target
				// contract. Different target paths/refs require their own Host proof.
				if !reflect.DeepEqual(t.action.Target, all[spec.ReviewTaskID].action.Target) {
					return empty, CodeError("rework_target_proof_missing")
				}
				artifact = feedback
			}
			target, err := targetFromArtifact(artifact, t.action.Target, plan)
			if err != nil {
				return empty, err
			}
			saved.Targets[t.id] = target
		}
	}
	if err = admitDAGReworkTargets(ctx, tx, saved); err != nil {
		return empty, err
	}
	saved.Receipt.ClosureSHA256 = saved.digest()
	encoded, err := json.Marshal(saved)
	if err != nil {
		return empty, err
	}
	if len(encoded) > 256*1024 {
		return empty, CodeError("rework_payload_too_large")
	}
	if _, err = parseDAGRework(string(encoded)); err != nil {
		return empty, err
	}
	cost := int64(len(encoded))
	bindings := []decisionAttemptBinding{}
	for _, t := range chain {
		cost += decisionBindingBytes(spec.ActionSlot, t.id, "")
		bindings = append(bindings, decisionAttemptBinding{slot: spec.ActionSlot, task: t.id, revision: t.revision + 1, reserve: true})
	}
	if err = d.storageAdmission(ctx, tx, run, cost, false); err != nil {
		return empty, err
	}
	ordinal, err := nextTaskAttemptOrdinal(ctx, tx, spec.TaskID)
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO retry_decisions(action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id,created_at,revision_payload) VALUES(?,?,?,?,?,?,?,?,0,?,?,?)`, spec.ActionSlot, spec.TaskID, spec.WorkRevision, spec.Review.EventID, spec.Review.EventRevision, spec.Review.EventHash, feedbackEvent.SegmentID, ordinal, spec.CommandID, time.Now().UTC().Format(time.RFC3339Nano), string(encoded)); err != nil {
		return empty, mapConflict(err)
	}
	if err = d.reserveDecisionBindings(ctx, tx, bindings); err != nil {
		return empty, err
	}
	members := map[string]bool{}
	for _, t := range chain {
		members[t.id] = true
	}
	for _, node := range saved.Nodes {
		status := "ready"
		for _, dep := range node.Dependencies {
			if members[dep] {
				status = "queued"
			}
		}
		var sequence any
		if status == "ready" {
			sequence, err = takeReadySequence(ctx, tx)
			if err != nil {
				return empty, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, status, node.TaskID); err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET work_revision=work_revision+1,adapter_payload=?,ready_sequence=? WHERE task_id=?`, string(node.Revised), sequence, node.TaskID); err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET status='revoked' WHERE task_id=?`, node.TaskID); err != nil {
			return empty, err
		}
	}
	if err = owned.commit(); err != nil {
		return empty, err
	}
	return saved.Receipt, nil
}
