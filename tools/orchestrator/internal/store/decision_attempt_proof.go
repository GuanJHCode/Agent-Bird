package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type decisionTaskScope struct {
	id, run, status, payload, deps string
	revision                       int
}

func readDecisionTask(ctx context.Context, tx *sql.Tx, id string) (decisionTaskScope, error) {
	s := decisionTaskScope{id: id}
	err := tx.QueryRowContext(ctx, `SELECT t.run_id,t.status,t.dependencies_json,r.work_revision,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, id).Scan(&s.run, &s.status, &s.deps, &s.revision, &s.payload)
	return s, err
}

func pendingTaskUnoccupied(ctx context.Context, tx *sql.Tx, task string) error {
	var active int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, task).Scan(&active)
	if err != nil || active != 0 {
		return ErrConflict
	}
	return nil
}

func validateOrdinaryDecisionPending(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task decisionTaskScope, kind string) error {
	managed := candidateManaged([]byte(task.payload))
	if task.status != "ready" || r.task != task.id || r.revision != task.revision || managed != (kind == reviewRetryKind) || pendingTaskUnoccupied(ctx, tx, task.id) != nil {
		return ErrConflict
	}
	e, err := decisionSource(ctx, tx, r, task.id, task.revision, !managed)
	if err != nil || e.Kind == "stopped" && !managed {
		return ErrConflict
	}
	if r.origin == "policy" {
		budget, e2 := readExecutionBudget(ctx, tx, task.id)
		candidatePolicy := managed && validateSavedPrelaunchReviewRetry(ctx, tx, r, task) == nil
		if e2 != nil || budget.policy == nil || budget.policy.Retry == nil || r.fallback || (r.payload != "" && !candidatePolicy) || e.Kind != contract.EventLaunchRejected || !e.Failure.ValidPrelaunchProbe() {
			return ErrConflict
		}
		body, e2 := budget.policy.CanonicalJSON()
		if e2 != nil || runtimeHash(string(body)) != r.policySHA {
			return ErrConflict
		}
		allowed := false
		for _, code := range budget.policy.Retry.Codes {
			allowed = allowed || code == e.Failure.Code
		}
		if !allowed {
			return ErrConflict
		}
		var state string
		var questions int
		if tx.QueryRowContext(ctx, `SELECT status FROM segment_runtime WHERE segment_id=?`, r.segment).Scan(&state) != nil || state != "not_started" {
			return ErrConflict
		}
		if tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started'))+(SELECT COUNT(*) FROM report_events q JOIN report_capabilities c ON c.capability_id=q.capability_id WHERE c.task_id=? AND q.kind='question' AND q.delivery_status='pending_session')`, task.id, task.id).Scan(&questions) != nil || questions != 0 {
			return ErrConflict
		}
	}
	var attemptStatus, sourceAttempt, latestAttempt, latestSegment string
	err = tx.QueryRowContext(ctx, `SELECT a.id,a.status,(SELECT id FROM attempts WHERE task_id=a.task_id ORDER BY attempt_no DESC LIMIT 1),(SELECT id FROM segments WHERE attempt_id=a.id ORDER BY segment_no DESC LIMIT 1) FROM attempts a JOIN segments s ON s.attempt_id=a.id WHERE s.id=? AND a.task_id=?`, r.segment, task.id).Scan(&sourceAttempt, &attemptStatus, &latestAttempt, &latestSegment)
	if err != nil || sourceAttempt != e.AttemptID || sourceAttempt != latestAttempt || r.segment != latestSegment || (attemptStatus != "failed" && !(managed && attemptStatus == "interrupted")) {
		return ErrConflict
	}
	next, err := nextTaskAttemptOrdinal(ctx, tx, task.id)
	if err != nil {
		return err
	}
	if r.promise < next {
		return ErrConflict
	}
	if managed && r.origin != "policy" {
		if err := validateSavedReviewRetry(ctx, tx, r, task, e); err != nil {
			return err
		}
	} else if r.fallback {
		var index int
		var raw string
		var payloads []json.RawMessage
		if tx.QueryRowContext(ctx, `SELECT fallback_index,fallback_payloads FROM task_runtime WHERE task_id=?`, task.id).Scan(&index, &raw) != nil || json.Unmarshal([]byte(raw), &payloads) != nil || index < 1 || index > len(payloads) || string(payloads[index-1]) != task.payload {
			return ErrConflict
		}
	}
	return checkTaskProvider(ctx, tx, task.id, nil)
}

// Queue-time group admission is not repeated after a decision commits. The
// saved readonly profile and original source/input identity remain mandatory;
// siblings only consume the separate shared capacity checked by ClaimReady.
func validateSavedReviewRetry(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task decisionTaskScope, e contract.Event) error {
	return validateSavedReviewRetryScope(ctx, tx, r, task, e, false)
}

// Legacy sources may themselves be retry grants whose binding was absent in
// schema12. This only verifies their saved grant bytes; the caller must still
// prove their unique decision ownership before creating a current binding.
func validateSavedReviewRetryScope(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task decisionTaskScope, e contract.Event, legacy bool) error {
	saved, err := parseReviewRetry(r.payload)
	if err != nil || r.fallback || e.Artifact != nil || e.Kind != contract.EventStopped && e.Kind != contract.EventFailed || saved.AdapterSHA256 != runtimeHash(task.payload) {
		return ErrConflict
	}
	if _, err = reviewRetryProfile([]byte(task.payload)); err != nil {
		return err
	}
	var c contract.LaunchCommand
	err = tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,c.work_revision,s.slot_token,s.launch_intent_hash,p.plan_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN report_capabilities c ON c.segment_id=s.segment_id AND c.attempt_id=a.id AND c.task_id=t.id JOIN runs p ON p.id=t.run_id WHERE s.segment_id=? AND t.id=?`, r.segment, task.id).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &c.PlanRevision)
	c.AdapterPayload = json.RawMessage(task.payload)
	if err != nil || c.WorkRevision != task.revision {
		return ErrConflict
	}
	original := c
	if bindErr := bindCandidateInput(ctx, tx, &c, true); bindErr != nil {
		var bound int
		if !legacy || tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id=?`, c.AttemptID).Scan(&bound) != nil || bound != 0 {
			return ErrConflict
		}
		c = original
		retry, retryErr := applyReviewRetryGrant(&c, saved)
		if retryErr != nil || bindCandidateInputForDecision(ctx, tx, &c, true, retry) != nil {
			return ErrConflict
		}
	}
	if c.Input == nil || saved.InputSHA256 != retryInputHash(c.Input) || saved.DependencyInputsSHA256 != dependencyVectorHash(c.DependencyInputs) {
		return ErrConflict
	}
	input := c.Input.Event
	_, artifact, err := readReworkEvent(ctx, tx, &reworkTask{id: input.TaskID, revision: input.WorkRevision}, ReworkEvent{EventID: input.EventID, EventRevision: input.EventRevision, EventHash: input.PayloadHash}, true)
	if err != nil || artifact.Stage != "validate" || artifact.Validation == nil || !artifact.Validation.Passed {
		return ErrConflict
	}
	return nil
}

// Strictly re-establish the immutable old edit/reviewer decision and its four
// new-revision targets. Current execution states need not be the original
// pre-rework states; those identities are carried by the saved event tuple.
func reworkDecisionTargets(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord) ([]decisionTaskScope, error) {
	var saved reworkDecision
	if strictDecisionJSON(r.payload, &saved) != nil {
		return nil, ErrConflict
	}
	spec, receipt, context := saved.Spec, saved.Receipt, saved.Context
	if spec.WorkRevision < 1 || spec.WorkRevision >= 2147483647 || spec.ReviewWorkRevision < 1 || spec.ReviewWorkRevision >= 2147483647 || r.task != spec.TaskID || r.revision != spec.WorkRevision || r.event != spec.Review.EventID || r.eventRevision != spec.Review.EventRevision || r.hash != spec.Review.EventHash || r.slot != spec.ActionSlot || r.fallback || spec.CommandID != "" || receipt.Status != "rework_queued" || receipt.TaskID != spec.TaskID || receipt.WorkRevision != spec.WorkRevision+1 || receipt.CandidateOID != spec.CandidateOID || receipt.ReviewEventID != spec.Review.EventID || receipt.RevisionSHA256 != context.Digest() || context.Version != 1 || context.TaskID != spec.TaskID || context.PreviousWorkRevision != spec.WorkRevision || context.PreviousCandidateOID != spec.CandidateOID || context.CandidateEventID != spec.Candidate.EventID || context.CandidateSHA256 != spec.Candidate.EventHash || context.ReviewEventID != spec.Review.EventID || context.ReviewSHA256 != spec.Review.EventHash || context.Feedback != spec.Feedback || context.Acceptance != spec.Acceptance || len(saved.Adapters) != 4 || len(receipt.TaskRevisions) != 4 || strings.TrimSpace(spec.Feedback) == "" || strings.TrimSpace(spec.Acceptance) == "" {
		return nil, ErrConflict
	}
	old, candidate, err := readReworkEventScope(ctx, tx, &reworkTask{id: spec.TaskID, revision: spec.WorkRevision}, spec.Candidate, true, false)
	if err != nil {
		return nil, err
	}
	feedback, review, err := readReworkEventScope(ctx, tx, &reworkTask{id: spec.ReviewTaskID, revision: spec.ReviewWorkRevision}, spec.Review, false, false)
	if err != nil || feedback.SegmentID != r.segment || feedback.ActionSlot != r.slot || review.Review == nil || context.ReviewSummary != review.Review.Summary || old.RunID != context.RunID || feedback.RunID != context.RunID || candidate.Candidate.CandidateOID != spec.CandidateOID || !reflect.DeepEqual(candidate.Candidate, review.Candidate) {
		return nil, ErrConflict
	}
	if candidate.Stage != "" || review.Review.Decision != "approve" && review.Review.Decision != "reject" || feedback.Kind == "failed" && review.Review.Decision != "reject" {
		return nil, ErrConflict
	}
	if _, err = decisionSource(ctx, tx, r, spec.ReviewTaskID, spec.ReviewWorkRevision, false); err != nil {
		return nil, err
	}
	var validateID, integrateID string
	parsed := map[string]adapter.InvocationPayload{}
	for id, raw := range saved.Adapters {
		if len(raw) > 16*1024 || strictDecisionJSON(string(raw), new(adapter.InvocationPayload)) != nil {
			return nil, ErrConflict
		}
		p, err := adapter.DecodeInvocationPayload(raw)
		if err != nil {
			return nil, err
		}
		parsed[id] = p
		if a := p.CandidateAction; a != nil {
			if a.Version != 1 {
				return nil, ErrConflict
			}
			switch a.Operation {
			case "validate":
				if validateID != "" || a.SourceTask != spec.TaskID || p.Kind != "candidate" {
					return nil, ErrConflict
				}
				validateID = id
			case "integrate":
				if integrateID != "" || a.SourceTask != spec.ReviewTaskID || p.Kind != "candidate" {
					return nil, ErrConflict
				}
				integrateID = id
			case "review":
				if id != spec.ReviewTaskID {
					return nil, ErrConflict
				}
			default:
				return nil, ErrConflict
			}
		}
	}
	editPayload, reviewer := parsed[spec.TaskID], parsed[spec.ReviewTaskID]
	if validateID == "" || integrateID == "" || editPayload.CandidateWorkspace == nil || editPayload.CandidateWorkspace.Rework == nil || !reflect.DeepEqual(*editPayload.CandidateWorkspace.Rework, context) || editPayload.CandidateWorkspace.BaseOID != spec.CandidateOID || editPayload.Provider != "claude-code" || editPayload.Profile == nil || editPayload.Profile.Role != adapter.Implementer || editPayload.Profile.Permission != adapter.WorkspaceWrite || reviewer.CandidateAction == nil || reviewer.CandidateAction.SourceTask != validateID || reviewer.Provider != "claude-code" || reviewer.Profile == nil || reviewer.Profile.Role != adapter.Reviewer || reviewer.Profile.Permission != adapter.ReadOnly || receipt.TaskRevisions[spec.TaskID] != spec.WorkRevision+1 || receipt.TaskRevisions[spec.ReviewTaskID] != spec.ReviewWorkRevision+1 {
		return nil, ErrConflict
	}
	ids := []string{spec.TaskID, validateID, spec.ReviewTaskID, integrateID}
	seen := map[string]bool{}
	var scopes []decisionTaskScope
	for i, id := range ids {
		s, err := readDecisionTask(ctx, tx, id)
		var deps []string
		if seen[id] || err != nil || s.run != context.RunID || receipt.TaskRevisions[id] < 2 || s.revision != receipt.TaskRevisions[id] || s.payload != string(saved.Adapters[id]) || json.Unmarshal([]byte(s.deps), &deps) != nil {
			return nil, ErrConflict
		}
		seen[id] = true
		if i == 0 && len(deps) != 0 || i > 0 && (len(deps) != 1 || deps[0] != ids[i-1]) {
			return nil, ErrConflict
		}
		if i > 0 && !reflect.DeepEqual(parsed[id].CandidateAction.Target, parsed[validateID].CandidateAction.Target) {
			return nil, ErrConflict
		}
		scopes = append(scopes, s)
	}
	var validation ReworkEvent
	if tx.QueryRowContext(ctx, `SELECT event_id,event_revision,payload_hash FROM runtime_events WHERE event_id=? AND task_id=?`, review.InputEventID, validateID).Scan(&validation.EventID, &validation.EventRevision, &validation.EventHash) != nil {
		return nil, ErrConflict
	}
	validatedEvent, validated, err := readReworkEventScope(ctx, tx, &reworkTask{id: validateID, revision: receipt.TaskRevisions[validateID] - 1}, validation, true, false)
	var plan int
	if tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, context.RunID).Scan(&plan) != nil || err != nil || !reworkBindingMatches(candidate.Candidate.Binding, old, plan) || !reworkBindingMatches(&review.Binding, feedback, plan) || !reworkBindingMatches(&validated.Binding, validatedEvent, plan) || validated.Validation == nil || !validated.Validation.Passed || validated.Stage != "validate" || review.Stage != "review" || validated.InputEventID != old.EventID || validated.InputSHA256 != old.PayloadHash || review.InputSHA256 != validatedEvent.PayloadHash || validated.ValidationDigest != review.ValidationDigest || !reflect.DeepEqual(candidate.Candidate, validated.Candidate) {
		return nil, ErrConflict
	}
	return scopes, nil
}

func validatePendingBinding(ctx context.Context, tx *sql.Tx, b decisionAttemptBinding) error {
	r, err := readAttemptDecision(ctx, tx, b.slot)
	if err != nil {
		return err
	}
	kind, err := decisionRecordKind(r)
	if err != nil {
		return err
	}
	if kind == dagReworkKind {
		if b.reservedMS <= 0 {
			return ErrConflict
		}
		if _, err := guardDAGBinding(ctx, tx, b.task, b.revision, ""); err != nil {
			return err
		}
		target, err := readDecisionTask(ctx, tx, b.task)
		if err != nil || target.status != "ready" {
			return ErrConflict
		}
		return pendingTaskUnoccupied(ctx, tx, b.task)
	}
	if kind != "rework" {
		s, err := readDecisionTask(ctx, tx, b.task)
		if err != nil || s.revision != b.revision {
			return ErrConflict
		}
		return validateOrdinaryDecisionPending(ctx, tx, r, s, kind)
	}
	targets, err := reworkDecisionTargets(ctx, tx, r)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.id == b.task && target.revision == b.revision && target.status == "ready" {
			return pendingTaskUnoccupied(ctx, tx, b.task)
		}
	}
	return ErrConflict
}
