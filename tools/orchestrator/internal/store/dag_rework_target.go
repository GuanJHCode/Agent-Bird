package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func targetFromArtifact(a reworkArtifact, want adapter.CandidateTarget, plan int) (dagReworkTarget, error) {
	b := a.Target
	if !filepath.IsAbs(a.Candidate.CommonDir) || filepath.Clean(a.Candidate.CommonDir) != a.Candidate.CommonDir || b.TargetWorktreeIdentity != want.Worktree || b.TargetRef != want.Ref || b.TargetBaseOID != want.BaseOID || b.FinalCandidateOID != a.Candidate.CandidateOID || b.PlanRevision != int64(plan) || b.TargetDirectoryIdentity.Device < 0 || b.TargetDirectoryIdentity.Inode <= 0 || b.TargetDirectoryIdentity.UID < 0 || !os.FileMode(b.TargetDirectoryIdentity.Mode).IsDir() || !reflect.DeepEqual(b.OrderedInputOIDs, a.Candidate.OrderedInputOIDs) {
		return dagReworkTarget{}, CodeError("rework_target_proof_invalid")
	}
	b.OrderedInputOIDs = append([]string{}, b.OrderedInputOIDs...)
	return dagReworkTarget{CommonDir: a.Candidate.CommonDir, Binding: b}, nil
}
func (t dagReworkTarget) key() string {
	body, _ := json.Marshal([]string{t.CommonDir, t.Binding.TargetRef})
	return runtimeHash(string(body))
}
func sameTargetReservation(a, b dagReworkTarget) bool {
	return a.CommonDir == b.CommonDir && a.Binding.TargetWorktreeIdentity == b.Binding.TargetWorktreeIdentity && a.Binding.TargetRef == b.Binding.TargetRef && a.Binding.TargetBaseOID == b.Binding.TargetBaseOID && a.Binding.TargetDirectoryIdentity == b.Binding.TargetDirectoryIdentity && a.Binding.PlanRevision == b.Binding.PlanRevision
}

func integrationScope(ctx context.Context, tx *sql.Tx, task string, requireCurrent bool) (dagReworkTarget, error) {
	current, err := readDecisionTask(ctx, tx, task)
	if err != nil {
		return dagReworkTarget{}, err
	}
	p, err := adapter.DecodeInvocationPayload([]byte(current.payload))
	if err != nil || p.CandidateAction == nil || p.CandidateAction.Operation != "integrate" {
		return dagReworkTarget{}, ErrConflict
	}
	r, err := dagDecisionForTask(ctx, tx, task, current.revision)
	if err != nil {
		return dagReworkTarget{}, err
	}
	var reserved *dagReworkTarget
	if r != nil {
		s, e := parseDAGRework(r.payload)
		if e != nil {
			return dagReworkTarget{}, e
		}
		target, ok := s.Targets[task]
		if !ok {
			return dagReworkTarget{}, ErrConflict
		}
		reserved = &target
		if !requireCurrent {
			return target, nil
		}
	}
	if reserved == nil {
		old, ok, err := legacyPendingIntegrationTarget(ctx, tx, current, p.CandidateAction.Target)
		if err != nil {
			return dagReworkTarget{}, err
		}
		if ok {
			reserved = &old
			if !requireCurrent {
				return old, nil
			}
		}
	}
	input, err := integrationAcceptedInput(ctx, tx, current, p.CandidateAction.SourceTask)
	if err != nil {
		return dagReworkTarget{}, CodeError("rework_target_scope_unresolved")
	}
	_, a, err := readReworkEvent(ctx, tx, &reworkTask{id: input.Event.TaskID, revision: input.Event.WorkRevision}, reworkEventTuple(input.Event), true)
	if err != nil || a.Stage != "review" || a.Review == nil || a.Review.Decision != "approve" {
		return dagReworkTarget{}, CodeError("rework_target_scope_unresolved")
	}
	var plan int
	if tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, current.run).Scan(&plan) != nil {
		return dagReworkTarget{}, ErrConflict
	}
	scope, err := targetFromArtifact(a, p.CandidateAction.Target, plan)
	if err != nil {
		return scope, err
	}
	if reserved != nil && !sameTargetReservation(*reserved, scope) {
		return scope, CodeError("rework_target_changed")
	}
	return scope, nil
}

// Includes pending rework promises and every integration with uncertain live
// ownership. A legacy scope with no original Host proof blocks the v1 action
// globally; raw payload paths cannot prove it belongs to a different repository.
func integrationReservations(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT t.id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE json_extract(r.adapter_payload,'$.candidate_action.operation')='integrate' AND (t.status='ready' OR EXISTS(SELECT 1 FROM attempts a JOIN segment_runtime s ON s.attempt_id=a.id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')) OR EXISTS(SELECT 1 FROM decision_attempt_bindings b WHERE b.task_id=t.id AND b.attempt_id IS NULL AND b.retired=0)) ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func admitDAGReworkTargets(ctx context.Context, tx *sql.Tx, s dagReworkDecision) error {
	if err := validateDAGTargetPromises(s); err != nil {
		return err
	}
	members := map[string]bool{}
	keys := map[string]bool{}
	for _, n := range s.Nodes {
		members[n.TaskID] = true
		if target, ok := s.Targets[n.TaskID]; ok {
			keys[target.key()] = true
		}
	}
	// Shared descendants already promised to another decision cannot be revised.
	for id := range members {
		var pending int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_attempt_bindings WHERE task_id=? AND attempt_id IS NULL AND retired=0`, id).Scan(&pending) != nil || pending != 0 {
			return ErrConflict
		}
	}
	ids, err := integrationReservations(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if members[id] {
			continue
		}
		scope, err := integrationScope(ctx, tx, id, false)
		if err != nil {
			return CodeError("rework_target_scope_unresolved")
		}
		if keys[scope.key()] {
			return CodeError("rework_target_reserved")
		}
	}
	return nil
}

func integrationDispatchAllowed(ctx context.Context, tx *sql.Tx, task readyTask) (bool, error) {
	var p adapter.InvocationPayload
	if json.Unmarshal([]byte(task.payload), &p) != nil || p.CandidateAction == nil || p.CandidateAction.Operation != "integrate" {
		return true, nil
	}
	// Existing v0-only runs keep their previous compatibility contract.
	reservations, err := outstandingDAGTargets(ctx, tx)
	if err != nil {
		return false, err
	}
	if len(reservations) == 0 {
		return true, nil
	}
	own, err := integrationScope(ctx, tx, task.taskID, true)
	if err != nil {
		return false, err
	}
	ownDecision, err := dagDecisionForTask(ctx, tx, task.taskID, task.revision)
	if err != nil {
		return false, err
	}
	ids, err := integrationReservations(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == task.taskID {
			continue
		}
		other, err := integrationScope(ctx, tx, id, false)
		if err != nil {
			return false, CodeError("rework_target_scope_unresolved")
		}
		if own.key() != other.key() {
			continue
		}
		var active, pending int
		var otherSeq, ownSeq sql.NullInt64
		if tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts a JOIN segment_runtime s ON s.attempt_id=a.id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')),(SELECT COUNT(*) FROM decision_attempt_bindings b WHERE b.task_id=? AND b.attempt_id IS NULL AND b.retired=0),(SELECT ready_sequence FROM task_runtime WHERE task_id=?),(SELECT ready_sequence FROM task_runtime WHERE task_id=?)`, id, id, id, task.taskID).Scan(&active, &pending, &otherSeq, &ownSeq) != nil {
			return false, ErrConflict
		}
		if active > 0 {
			return false, nil
		}
		if pending > 0 {
			otherTask, err := readDecisionTask(ctx, tx, id)
			if err != nil {
				return false, err
			}
			otherDecision, err := dagDecisionForTask(ctx, tx, id, otherTask.revision)
			if err != nil {
				return false, err
			}
			if otherDecision != nil {
				if ownDecision != nil {
					return false, CodeError("rework_target_reserved")
				}
				return false, nil
			}
			if ownDecision != nil {
				return false, CodeError("rework_target_reserved")
			}
			if !otherSeq.Valid {
				return false, nil
			}
		}
		// QueueRework refused pre-existing ready integrations before creating this
		// promise. Later ordinary readiness cannot overtake the older reservation.
		if ownDecision == nil && otherSeq.Valid && ownSeq.Valid && otherSeq.Int64 < ownSeq.Int64 {
			return false, nil
		}
	}
	return true, nil
}

func validateDAGResultTarget(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	r, err := dagDecisionForTask(ctx, tx, e.TaskID, e.WorkRevision)
	if err != nil || r == nil {
		return err
	}
	if _, err = guardDAGBinding(ctx, tx, e.TaskID, e.WorkRevision, e.AttemptID); err != nil {
		return err
	}
	saved, err := parseDAGRework(r.payload)
	if err != nil {
		return err
	}
	a, err := readReworkArtifact(e)
	if err != nil {
		return err
	}
	if e.TaskID == saved.Spec.TaskID {
		old, err := readReworkArtifactFromEvidence(ctx, tx, saved.Evidence[e.TaskID])
		if err != nil {
			return err
		}
		if a.Candidate.CommonDir != old.Candidate.CommonDir || a.Candidate.Binding == nil || a.Candidate.Binding.RevisionSHA256 != saved.Receipt.RevisionSHA256 {
			return CodeError("rework_target_changed")
		}
		return nil
	}
	var raw json.RawMessage
	for _, n := range saved.Nodes {
		if n.TaskID == e.TaskID {
			raw = n.Revised
		}
	}
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || p.CandidateAction == nil {
		return ErrConflict
	}
	// Integration's final artifact is checked by its Host journal, and the input
	// target was already checked atomically before creating its launch request.
	if p.CandidateAction.Operation == "integrate" {
		return nil
	}
	want, ok := saved.Targets[e.TaskID]
	if !ok {
		return ErrConflict
	}
	scope, err := targetFromArtifact(a, p.CandidateAction.Target, int(want.Binding.PlanRevision))
	if err != nil {
		return err
	}
	if !sameTargetReservation(want, scope) {
		return CodeError("rework_target_changed")
	}
	return nil
}
func readReworkArtifactFromEvidence(ctx context.Context, tx *sql.Tx, p dagReworkEvidence) (reworkArtifact, error) {
	_, a, err := readReworkEventScope(ctx, tx, &reworkTask{id: p.TaskID, revision: p.Revision}, p.Event, p.Accepted, false)
	return a, err
}

// An active legacy integration must still match its original launch hash. The
// current accepted source alone is insufficient if another revision intervened.
func integrationAcceptedInput(ctx context.Context, tx *sql.Tx, current decisionTaskScope, source string) (contract.AcceptedInput, error) {
	var c contract.LaunchCommand
	err := tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,cap.work_revision,s.slot_token,s.launch_intent_hash,p.plan_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN runs p ON p.id=t.run_id JOIN report_capabilities cap ON cap.segment_id=s.segment_id AND cap.attempt_id=a.id AND cap.task_id=t.id WHERE t.id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, current.id).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &c.PlanRevision)
	if err == sql.ErrNoRows {
		return acceptedCandidateInput(ctx, tx, current.run, source)
	}
	if err != nil {
		return contract.AcceptedInput{}, err
	}
	c.AdapterPayload = json.RawMessage(current.payload)
	if err = bindCandidateInputForDecision(ctx, tx, &c, true, nil); err != nil || c.Input == nil {
		return contract.AcceptedInput{}, CodeError("rework_target_scope_unresolved")
	}
	return *c.Input, nil
}

func validateDAGTargetPromises(s dagReworkDecision) error {
	keys := map[string]bool{}
	for _, node := range s.Nodes {
		p, err := adapter.DecodeInvocationPayload(node.Original)
		if err != nil {
			return err
		}
		if p.CandidateAction == nil || p.CandidateAction.Operation != "integrate" {
			continue
		}
		target, ok := s.Targets[node.TaskID]
		if !ok {
			return ErrConflict
		}
		key := target.key()
		if keys[key] {
			return CodeError("rework_target_reserved")
		}
		keys[key] = true
	}
	return nil
}

// Legacy rework also saved an exact authorizing review. Recover its scope only
// from that historical artifact and the complete original four-node proof.
func legacyPendingIntegrationTarget(ctx context.Context, tx *sql.Tx, current decisionTaskScope, want adapter.CandidateTarget) (dagReworkTarget, bool, error) {
	var slot string
	err := tx.QueryRowContext(ctx, `SELECT action_slot FROM decision_attempt_bindings WHERE task_id=? AND target_work_revision=? AND attempt_id IS NULL AND retired=0`, current.id, current.revision).Scan(&slot)
	if err == sql.ErrNoRows {
		return dagReworkTarget{}, false, nil
	}
	if err != nil {
		return dagReworkTarget{}, false, err
	}
	record, err := readAttemptDecision(ctx, tx, slot)
	if err != nil {
		return dagReworkTarget{}, false, err
	}
	kind, err := decisionRecordKind(record)
	if err != nil || kind != "rework" {
		return dagReworkTarget{}, false, CodeError("rework_target_scope_unresolved")
	}
	if _, err = reworkDecisionTargets(ctx, tx, record); err != nil {
		return dagReworkTarget{}, false, err
	}
	var saved reworkDecision
	if strictDecisionJSON(record.payload, &saved) != nil || saved.Receipt.TaskRevisions[current.id] != current.revision || string(saved.Adapters[current.id]) != current.payload {
		return dagReworkTarget{}, false, ErrConflict
	}
	_, artifact, err := readReworkEventScope(ctx, tx, &reworkTask{id: saved.Spec.ReviewTaskID, revision: saved.Spec.ReviewWorkRevision}, saved.Spec.Review, false, false)
	if err != nil {
		return dagReworkTarget{}, false, err
	}
	var plan int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, current.run).Scan(&plan); err != nil {
		return dagReworkTarget{}, false, err
	}
	scope, err := targetFromArtifact(artifact, want, plan)
	return scope, true, err
}

func admitLegacyReworkTarget(ctx context.Context, tx *sql.Tx, artifact reworkArtifact, want adapter.CandidateTarget, plan int) error {
	reservations, err := outstandingDAGTargets(ctx, tx)
	if err != nil || len(reservations) == 0 {
		return err
	}
	scope, err := targetFromArtifact(artifact, want, plan)
	if err != nil {
		return CodeError("rework_target_scope_unresolved")
	}
	for _, other := range reservations {
		if scope.key() == other.key() {
			return CodeError("rework_target_reserved")
		}
	}
	return nil
}

// The binding is the durable reservation. A deleted/malformed JSON marker
// cannot make that reservation disappear. Classify every outstanding decision
// before deciding that the compatibility fast path contains only legacy work.
func outstandingDAGTargets(ctx context.Context, tx *sql.Tx) (map[string]dagReworkTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT b.action_slot,b.task_id,b.target_work_revision,b.reserved_active_ms FROM decision_attempt_bindings b JOIN task_runtime t ON t.task_id=b.task_id WHERE json_extract(t.adapter_payload,'$.candidate_action.operation')='integrate' AND b.retired=0 AND (b.attempt_id IS NULL OR EXISTS(SELECT 1 FROM segment_runtime s WHERE s.attempt_id=b.attempt_id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')))`)
	if err != nil {
		return nil, err
	}
	var bindings []decisionAttemptBinding
	for rows.Next() {
		var b decisionAttemptBinding
		if err = rows.Scan(&b.slot, &b.task, &b.revision, &b.reservedMS); err != nil {
			rows.Close()
			return nil, err
		}
		bindings = append(bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[string]dagReworkTarget{}
	for _, b := range bindings {
		record, err := readAttemptDecision(ctx, tx, b.slot)
		if err != nil {
			return nil, CodeError("rework_target_scope_unresolved")
		}
		kind, err := decisionRecordKind(record)
		if err != nil {
			return nil, CodeError("rework_target_scope_unresolved")
		}
		switch kind {
		case "rework":
			var saved reworkDecision
			if strictDecisionJSON(record.payload, &saved) != nil || saved.Spec.Version != 0 || saved.Receipt.Version != 0 || saved.Receipt.ClosureSHA256 != "" || saved.Receipt.TaskRevisions[b.task] != b.revision {
				return nil, CodeError("rework_target_scope_unresolved")
			}
			if _, err = reworkDecisionTargets(ctx, tx, record); err != nil {
				return nil, CodeError("rework_target_scope_unresolved")
			}
		case dagReworkKind:
			saved, err := parseDAGRework(record.payload)
			if err != nil || saved.Receipt.TaskRevisions[b.task] != b.revision || b.reservedMS <= 0 {
				return nil, CodeError("rework_target_scope_unresolved")
			}
			target, ok := saved.Targets[b.task]
			if !ok {
				return nil, CodeError("rework_target_scope_unresolved")
			}
			out[b.task] = target
		default:
			return nil, CodeError("rework_target_scope_unresolved")
		}
	}
	return out, nil
}
