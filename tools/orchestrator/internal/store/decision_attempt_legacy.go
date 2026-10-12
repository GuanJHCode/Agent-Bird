package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Only this task and its bounded admitted candidate predecessor chain can
// contribute a relevant decision. No unrelated run or arbitrary file scan.
func relevantAttemptDecisions(ctx context.Context, tx *sql.Tx, task string) ([]attemptDecisionRecord, error) {
	start, err := readDecisionTask(ctx, tx, task)
	if err != nil {
		return nil, err
	}
	// Only the admitted filter actions extend the bounded predecessor chain.
	// Keep the existing bound for every legacy and non-filter action.
	maximum := 4
	if p, decodeErr := adapter.DecodeInvocationPayload([]byte(start.payload)); decodeErr == nil {
		if adapter.IsFilterTargetReview(p) {
			maximum = 5
		}
		if adapter.IsFilterIntegrate(p) {
			maximum = 6
		}
	}
	root, seen := start, map[string]bool{}
	for {
		if seen[root.id] || len(seen) >= maximum || root.run != start.run {
			return nil, errLegacyDecisionAmbiguous
		}
		seen[root.id] = true
		// Keep the existing opaque noncandidate (e.g. gitops) admission
		// schema; only an admitted candidate action supplies an ancestor.
		var payload struct {
			CandidateAction *struct {
				SourceTask string `json:"source_task"`
			} `json:"candidate_action"`
		}
		if json.Unmarshal([]byte(root.payload), &payload) != nil {
			return nil, errLegacyDecisionAmbiguous
		}
		if payload.CandidateAction == nil {
			break
		}
		if isV2Compose(json.RawMessage(root.payload)) {
			break
		}
		root, err = readDecisionTask(ctx, tx, payload.CandidateAction.SourceTask)
		if err != nil {
			return nil, errLegacyDecisionAmbiguous
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.action_slot,d.task_id,d.work_revision,d.source_event_id,d.source_event_revision,d.source_event_hash,d.source_segment_id,d.next_attempt_no,d.use_next_fallback,d.revision_payload FROM retry_decisions d JOIN tasks t ON t.id=d.task_id WHERE t.run_id=? AND d.task_id IN (?,?) ORDER BY d.action_slot LIMIT 65`, start.run, task, root.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []attemptDecisionRecord
	for rows.Next() {
		var r attemptDecisionRecord
		if err = rows.Scan(&r.slot, &r.task, &r.revision, &r.event, &r.eventRevision, &r.hash, &r.segment, &r.promise, &r.fallback, &r.payload); err != nil {
			return nil, err
		}
		records = append(records, r)
		if len(records) > 64 {
			return nil, errLegacyDecisionAmbiguous
		}
	}
	return records, rows.Err()
}

func recordTargetsRevision(r attemptDecisionRecord, task string, revision int) (string, bool, error) {
	kind, err := decisionRecordKind(r)
	if err != nil {
		return "", false, errLegacyDecisionAmbiguous
	}
	if kind == dagReworkKind {
		saved, err := parseDAGRework(r.payload)
		return kind, saved.Receipt.TaskRevisions[task] == revision, err
	}
	if kind != "rework" {
		return kind, r.task == task && r.revision == revision, nil
	}
	var saved reworkDecision
	if strictDecisionJSON(r.payload, &saved) != nil {
		return "", false, errLegacyDecisionAmbiguous
	}
	return kind, saved.Receipt.TaskRevisions[task] == revision, nil
}

func (d *DB) reconcileLegacyPendingTask(ctx context.Context, tx *sql.Tx, task string, revision int) error {
	if found, err := guardDAGBinding(ctx, tx, task, revision, ""); found || err != nil {
		return err
	}
	records, err := relevantAttemptDecisions(ctx, tx, task)
	if err != nil {
		return err
	}
	var matching []attemptDecisionRecord
	for _, r := range records {
		kind, relevant, err := recordTargetsRevision(r, task, revision)
		if err != nil {
			return err
		}
		if !relevant {
			continue
		}
		var bound int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_attempt_bindings WHERE action_slot=?`, r.slot).Scan(&bound); err != nil {
			return err
		}
		if bound != 0 {
			// No pending row was found for this target: never reopen a consumed
			// binding or repair a contradictory partial new-format commitment.
			return errLegacyDecisionAmbiguous
		}
		if kind != "rework" {
			var source, latest string
			if err = tx.QueryRowContext(ctx, `SELECT s.attempt_id,(SELECT id FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE s.id=? AND a.task_id=?`, task, r.segment, task).Scan(&source, &latest); err != nil {
				return errLegacyDecisionAmbiguous
			}
			if source != latest {
				continue // A historical source cannot supply current pending authority.
			}
		}
		matching = append(matching, r)
	}
	if len(matching) > 1 {
		return errLegacyDecisionAmbiguous
	}
	if len(matching) == 0 {
		if len(records) != 0 {
			return errLegacyDecisionAmbiguous
		}
		var attempts int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id=?`, task).Scan(&attempts); err != nil {
			return err
		}
		if attempts != 0 {
			return errLegacyDecisionAmbiguous
		}
		return nil // First admitted task attempt, with no relevant decision.
	}
	return d.reconcileLegacyDecision(ctx, tx, matching[0])
}

type legacyCandidateCommitment struct {
	record        attemptDecisionRecord
	kind          string
	sourceAttempt string
	sourceSegment string
}

// Authenticate every bounded relevant commitment, including historical
// consumed sources. A launch-content hash alone is not an action-slot proof.
func legacyCandidateCommitments(ctx context.Context, tx *sql.Tx, target decisionTaskScope) ([]legacyCandidateCommitment, error) {
	records, err := relevantAttemptDecisions(ctx, tx, target.id)
	if err != nil {
		return nil, err
	}
	var proofs []legacyCandidateCommitment
	for _, r := range records {
		kind, relevant, err := recordTargetsRevision(r, target.id, target.revision)
		if err != nil {
			return nil, err
		}
		if !relevant {
			continue
		}
		proof := legacyCandidateCommitment{record: r, kind: kind}
		switch kind {
		case reviewRetryKind:
			e, err := decisionSource(ctx, tx, r, target.id, target.revision, false)
			if err != nil || validateSavedReviewRetryScope(ctx, tx, r, target, e, true) != nil {
				return nil, errLegacyDecisionAmbiguous
			}
			proof.sourceAttempt, proof.sourceSegment = e.AttemptID, e.SegmentID
		case "rework":
			if _, err := reworkDecisionTargets(ctx, tx, r); err != nil {
				return nil, errLegacyDecisionAmbiguous
			}
		default:
			return nil, errLegacyDecisionAmbiguous
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func legacyCandidateMatches(ctx context.Context, tx *sql.Tx, proof legacyCandidateCommitment, c contract.LaunchCommand) bool {
	if c.AttemptID == proof.sourceAttempt || c.SegmentID == proof.sourceSegment {
		return false // A retry cannot consume its authenticated failure source.
	}
	var retry *reviewRetryDecision
	if proof.kind == reviewRetryKind {
		saved, err := parseReviewRetry(proof.record.payload)
		if err != nil {
			return false // Already authenticated by legacyCandidateCommitments.
		}
		retry, err = applyReviewRetryGrant(&c, saved)
		if err != nil {
			return false
		}
	}
	return bindCandidateInputForDecision(ctx, tx, &c, true, retry, candidateArtifactHistoricalProof) == nil
}

// Reproduce the original first segment, never an ordinal/timestamp neighbor,
// and exclude every second possible commitment for that exact target.
func proveLegacyCandidateAttempt(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, kind string, target decisionTaskScope) (string, error) {
	if !candidateManaged([]byte(target.payload)) {
		return "", errLegacyDecisionAmbiguous
	}
	proofs, err := legacyCandidateCommitments(ctx, tx, target)
	if err != nil {
		return "", err
	}
	var own *legacyCandidateCommitment
	for i := range proofs {
		if proofs[i].record.slot == r.slot && proofs[i].kind == kind {
			own = &proofs[i]
		}
	}
	if own == nil {
		return "", errLegacyDecisionAmbiguous
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,a.id,s.segment_id,c.work_revision,s.slot_token,s.launch_intent_hash,p.plan_revision FROM segment_runtime s JOIN segments z ON z.id=s.segment_id AND z.attempt_id=s.attempt_id AND z.segment_no=1 JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN report_capabilities c ON c.segment_id=s.segment_id AND c.attempt_id=a.id AND c.task_id=t.id AND c.run_id=t.run_id AND c.producer_id=s.host_id JOIN runs p ON p.id=t.run_id WHERE t.id=? AND c.work_revision=? LIMIT 4`, target.id, target.revision)
	if err != nil {
		return "", err
	}
	var commands []contract.LaunchCommand
	for rows.Next() {
		var c contract.LaunchCommand
		if err = rows.Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &c.PlanRevision); err != nil {
			rows.Close()
			return "", err
		}
		c.AdapterPayload = json.RawMessage(target.payload)
		commands = append(commands, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(commands) > 3 {
		return "", errLegacyDecisionAmbiguous
	}
	var matched string
	for _, c := range commands {
		if !legacyCandidateMatches(ctx, tx, *own, c) {
			continue
		}
		for _, other := range proofs {
			if other.record.slot == r.slot {
				continue
			}
			var revision int
			var attempt sql.NullString
			boundErr := tx.QueryRowContext(ctx, `SELECT target_work_revision,attempt_id FROM decision_attempt_bindings WHERE action_slot=? AND task_id=?`, other.record.slot, target.id).Scan(&revision, &attempt)
			if boundErr == nil {
				if revision != target.revision || attempt.String == c.AttemptID {
					return "", errLegacyDecisionAmbiguous
				}
				continue // An immutable pending/different target cannot be reassigned.
			}
			if !errors.Is(boundErr, sql.ErrNoRows) {
				return "", boundErr
			}
			if legacyCandidateMatches(ctx, tx, other, c) {
				return "", errLegacyDecisionAmbiguous
			}
		}
		if matched != "" && matched != c.AttemptID {
			return "", errLegacyDecisionAmbiguous
		}
		matched = c.AttemptID
	}
	return matched, nil
}

// Missing immutable scope is ambiguity, not proof of an unstarted target.
// In particular, a pre-upgrade StartAttempt could leave a bare managed segment.
func legacyAttemptScopesComplete(ctx context.Context, tx *sql.Tx, target decisionTaskScope) error {
	var incomplete int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a WHERE a.task_id=? AND (
 (SELECT COUNT(*) FROM segments z JOIN segment_runtime s ON s.segment_id=z.id AND s.attempt_id=a.id JOIN report_capabilities c ON c.segment_id=z.id AND c.attempt_id=a.id AND c.task_id=a.task_id AND c.run_id=? WHERE z.attempt_id=a.id AND z.segment_no=1)!=1
 OR (SELECT COUNT(DISTINCT c.work_revision) FROM segments z JOIN report_capabilities c ON c.segment_id=z.id AND c.attempt_id=a.id AND c.task_id=a.task_id WHERE z.attempt_id=a.id)!=1)`, target.id, target.run).Scan(&incomplete)
	if err != nil {
		return err
	}
	if incomplete != 0 {
		return errLegacyDecisionAmbiguous
	}
	return nil
}

func (d *DB) reconcileLegacyDecision(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord) error {
	return d.reconcileLegacyDecisionScope(ctx, tx, r, map[string]bool{})
}

func (d *DB) reconcileLegacyDecisionScope(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, resolving map[string]bool) error {
	if resolving[r.slot] || len(resolving) >= 64 {
		return errLegacyDecisionAmbiguous
	}
	resolving[r.slot] = true
	defer delete(resolving, r.slot)
	kind, err := decisionRecordKind(r)
	if err != nil {
		return errLegacyDecisionAmbiguous
	}
	var scopes []decisionTaskScope
	if kind == dagReworkKind || kind == validationRetryKind {
		return errLegacyDecisionAmbiguous
	}
	if kind == "rework" {
		scopes, err = reworkDecisionTargets(ctx, tx, r)
	} else {
		var target decisionTaskScope
		target, err = readDecisionTask(ctx, tx, r.task)
		scopes = []decisionTaskScope{target}
	}
	if err != nil {
		return errLegacyDecisionAmbiguous
	}
	if kind == reviewRetryKind {
		// A legacy retry source may itself be an earlier retry. Restore only
		// its uniquely proved commitment in this same transaction before the
		// normal saved-input checks; no neighboring ordinal/time inference.
		e, sourceErr := decisionSource(ctx, tx, r, r.task, r.revision, false)
		if sourceErr != nil || validateSavedReviewRetryScope(ctx, tx, r, scopes[0], e, true) != nil {
			return errLegacyDecisionAmbiguous
		}
		var source contract.LaunchCommand
		if tx.QueryRowContext(ctx, `SELECT command_id,host_id,attempt_id,segment_id,slot_token,launch_intent_hash FROM segment_runtime WHERE segment_id=?`, e.SegmentID).Scan(&source.CommandID, &source.HostID, &source.AttemptID, &source.SegmentID, &source.SlotToken, &source.LaunchIntentHash) != nil {
			return errLegacyDecisionAmbiguous
		}
		source.RunID, source.TaskID, source.WorkRevision, source.AdapterPayload = e.RunID, e.TaskID, e.WorkRevision, json.RawMessage(scopes[0].payload)
		if tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, source.RunID).Scan(&source.PlanRevision) != nil {
			return errLegacyDecisionAmbiguous
		}
		if bindHistoricalCandidateInput(ctx, tx, &source) != nil {
			if d.reconcileLegacyGrantScope(ctx, tx, &source, resolving) != nil || validateSavedReviewRetry(ctx, tx, r, scopes[0], e) != nil {
				return errLegacyDecisionAmbiguous
			}
		}
	}
	var bindings []decisionAttemptBinding
	var cost int64
	for _, target := range scopes {
		if err = legacyAttemptScopesComplete(ctx, tx, target); err != nil {
			return err
		}
		var grants int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT a.id) FROM attempts a JOIN segments z ON z.attempt_id=a.id JOIN report_capabilities c ON c.segment_id=z.id AND c.attempt_id=a.id AND c.task_id=a.task_id JOIN segment_runtime s ON s.segment_id=z.id AND s.attempt_id=a.id WHERE a.task_id=? AND c.work_revision=?`, target.id, target.revision).Scan(&grants); err != nil {
			return err
		}
		binding := decisionAttemptBinding{slot: r.slot, task: target.id, revision: target.revision}
		if grants > 0 && kind != "ordinary" {
			binding.attempt, err = proveLegacyCandidateAttempt(ctx, tx, r, kind, target)
			if err != nil {
				return err // Ambiguity is never evidence of an unconsumed promise.
			}
			if binding.attempt == "" {
				// Review retry's original source attempt shares the same revision;
				// it is not evidence that the retry promise was consumed.
				if kind != reviewRetryKind || validateOrdinaryDecisionPending(ctx, tx, r, target, kind) != nil {
					return errLegacyDecisionAmbiguous
				}
			}
		} else if kind != "rework" {
			if validateOrdinaryDecisionPending(ctx, tx, r, target, kind) != nil {
				return errLegacyDecisionAmbiguous
			}
		} else {
			if target.status != "ready" && target.status != "queued" && target.status != "blocked_dependency" || pendingTaskUnoccupied(ctx, tx, target.id) != nil {
				return errLegacyDecisionAmbiguous
			}
			if target.id == r.task {
				next, err := nextTaskAttemptOrdinal(ctx, tx, target.id)
				if err != nil || r.promise < next {
					return errLegacyDecisionAmbiguous
				}
			}
		}
		var existing decisionAttemptBinding
		var attempt sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT task_id,target_work_revision,attempt_id FROM decision_attempt_bindings WHERE action_slot=? AND task_id=?`, r.slot, target.id).Scan(&existing.task, &existing.revision, &attempt)
		if err == nil {
			if existing.revision != binding.revision || attempt.String != binding.attempt {
				return errLegacyDecisionAmbiguous
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		bindings = append(bindings, binding)
		cost += decisionBindingBytes(binding.slot, binding.task, binding.attempt)
	}
	if err = d.storageAdmission(ctx, tx, scopes[0].run, cost, false); err != nil {
		return err
	}
	return insertDecisionBindings(ctx, tx, bindings)
}

func (d *DB) reconcileLegacyGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	return d.reconcileLegacyGrantScope(ctx, tx, c, map[string]bool{})
}

func (d *DB) reconcileLegacyGrantScope(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, resolving map[string]bool) error {
	if found, err := guardDAGBinding(ctx, tx, c.TaskID, c.WorkRevision, c.AttemptID); found || err != nil {
		return err
	}
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT target_work_revision FROM decision_attempt_bindings WHERE attempt_id=? AND task_id=?`, c.AttemptID, c.TaskID).Scan(&revision)
	if err == nil {
		if revision != c.WorkRevision {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	records, err := relevantAttemptDecisions(ctx, tx, c.TaskID)
	if err != nil {
		return err
	}
	var matching []attemptDecisionRecord
	target, err := readDecisionTask(ctx, tx, c.TaskID)
	if err != nil {
		return err
	}
	for _, r := range records {
		kind, relevant, err := recordTargetsRevision(r, c.TaskID, c.WorkRevision)
		if err != nil {
			return err
		}
		if !relevant {
			continue
		}
		if kind == "ordinary" {
			return errLegacyDecisionAmbiguous
		}
		attempt, err := proveLegacyCandidateAttempt(ctx, tx, r, kind, target)
		if err != nil {
			return err
		}
		if attempt == c.AttemptID {
			matching = append(matching, r)
		}
	}
	if len(matching) > 1 {
		return errLegacyDecisionAmbiguous
	}
	if len(matching) == 0 {
		// Only a true original first grant can have no decision association.
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id=?`, c.TaskID).Scan(&count) != nil || count != 1 || len(records) != 0 {
			return errLegacyDecisionAmbiguous
		}
		return nil
	}
	return d.reconcileLegacyDecisionScope(ctx, tx, matching[0], resolving)
}
