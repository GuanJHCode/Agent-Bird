package store

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// RunSummary is a read-only projection of the existing task database. Actions
// describe state eligibility, not grants: mutations still require fresh event,
// revision, owner and (for resume) explicit recovery authorization.
type RunSummary struct {
	Version int            `json:"version"`
	RunID   string         `json:"run_id"`
	Counts  map[string]int `json:"counts"`
	Tasks   []SummaryTask  `json:"tasks"`
}

type SummaryTask struct {
	TemporaryWorkspaceCleanup *contract.TemporaryWorkspaceCleanup `json:"temporary_workspace_cleanup,omitempty"`
	DiagnosticsFailure        *contract.DiagnosticsFailure        `json:"diagnostics_failure,omitempty"`
	EffectiveBudget           *EffectiveBudgetSnapshot            `json:"effective_budget,omitempty"`
	ExecutionPolicy           *contract.ExecutionPolicy           `json:"execution_policy,omitempty"`
	NextRetryAtUnixMS         int64                               `json:"next_retry_at_unix_ms,omitempty"`
	TaskID                    string                              `json:"task_id"`
	Status                    string                              `json:"status"`
	Group                     string                              `json:"group"`
	WorkRevision              int                                 `json:"work_revision"`
	ActiveSegments            int                                 `json:"active_segments"`
	AllowedActions            []string                            `json:"allowed_actions"`
	ReviewableResult          *ReviewableResultBinding            `json:"reviewable_result,omitempty"`
	ResumeBlockedReason       string                              `json:"resume_blocked_reason,omitempty"`
	StopReason                string                              `json:"stop_reason,omitempty"`
	LatestProgress            *contract.ArtifactRef               `json:"latest_progress,omitempty"`
	ContinuationSource        *ContinuationSourceBinding          `json:"continuation_source,omitempty"`
}

type ContinuationSourceBinding struct {
	ReviewableResultBinding
	SegmentID string `json:"segment_id"`
}

// ReviewableResultBinding recovers the existing immutable accept/reject tuple
// after collection ACK. It carries no result body or owner capability.
type ReviewableResultBinding struct {
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	WorkRevision  int    `json:"work_revision"`
}

func (d *DB) SummarizeRun(ctx context.Context, taskID, controller, token string) (RunSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ValidateTaskOwner(ctx, taskID, controller, token); err != nil {
		return RunSummary{}, err
	}
	tx, e := d.sql.BeginTx(ctx, nil)
	if e != nil {
		return RunSummary{}, e
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT t.run_id,t.id,t.status,r.work_revision,
 (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')),
 (SELECT COUNT(*) FROM attempts a JOIN task_runtime ar ON ar.task_id=a.task_id WHERE ar.budget_group_id=r.budget_group_id),t.max_attempts,
 EXISTS(SELECT 1 FROM runtime_events e WHERE e.task_id=t.id AND json_extract(e.body_json,'$.kind')='result' AND e.segment_id=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1)),
 EXISTS(SELECT 1 FROM runtime_questions q WHERE q.task_id=t.id AND q.status='open' AND q.work_revision=r.work_revision),
 EXISTS(SELECT 1 FROM runtime_events e JOIN segments s ON s.id=e.segment_id JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id AND a.status='failed' AND a.attempt_no=(SELECT MAX(attempt_no) FROM attempts WHERE task_id=t.id) AND (json_extract(e.body_json,'$.kind')='failed' OR (json_extract(e.body_json,'$.kind')='result' AND EXISTS(SELECT 1 FROM review_decisions rd WHERE rd.event_id=e.event_id AND rd.decision='reject')))),r.adapter_payload,
 EXISTS(SELECT 1 FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id WHERE e.task_id=t.id AND s.status='exited' AND s.outcome=json_extract(e.body_json,'$.kind') AND json_extract(e.body_json,'$.kind') IN ('stopped','failed') AND json_extract(e.body_json,'$.artifact') IS NULL AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1)),
 (SELECT COUNT(*) FROM task_runtime other WHERE other.budget_group_id=r.budget_group_id),
 COALESCE((SELECT json_object('event_id',e.event_id,'event_revision',e.event_revision,'event_hash',e.payload_hash,'action_slot',e.action_slot,'work_revision',r.work_revision)
  FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id AND s.attempt_id=e.attempt_id
  JOIN attempts a ON a.id=s.attempt_id AND a.task_id=t.id
  WHERE e.task_id=t.id AND t.status='result_ready' AND s.status='exited' AND s.outcome='result'
   AND a.attempt_no=(SELECT MAX(newest.attempt_no) FROM attempts newest WHERE newest.task_id=t.id)
   AND json_extract(e.body_json,'$.kind')='result' AND json_extract(e.body_json,'$.work_revision')=r.work_revision
   AND e.delivery_status IN ('pending','acked') AND json_type(e.body_json,'$.artifact')='object'
   AND json_extract(e.body_json,'$.artifact.path')!='' AND json_extract(e.body_json,'$.artifact.size')>0
   AND json_extract(e.body_json,'$.artifact.sha256')=e.payload_hash
   AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts latest ON latest.id=z.attempt_id WHERE latest.task_id=t.id ORDER BY latest.attempt_no DESC,z.segment_no DESC LIMIT 1)
  ORDER BY e.sequence DESC LIMIT 1),''),
 COALESCE((SELECT json_extract(e.body_json,'$.stop_reason') FROM runtime_events e WHERE e.task_id=t.id AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1) AND json_extract(e.body_json,'$.kind')='stopped' ORDER BY e.sequence DESC LIMIT 1),''),
 COALESCE((SELECT json_extract(e.body_json,'$.diagnostics_failure') FROM runtime_events e
 JOIN segment_runtime sr ON sr.segment_id=e.segment_id AND sr.host_id=e.producer_id AND sr.attempt_id=e.attempt_id
 WHERE e.task_id=t.id AND json_extract(e.body_json,'$.kind')='exited'
 AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1)
 ORDER BY e.sequence ASC LIMIT 1),''),
 COALESCE((SELECT json_extract(e.body_json,'$.artifact') FROM host_progress e WHERE e.task_id=t.id AND NOT EXISTS(SELECT 1 FROM managed_artifact_reservations m WHERE m.task_id=e.task_id AND m.scope_key=e.segment_id AND m.class='progress' AND m.state IN ('deleting','released')) AND json_extract(e.body_json,'$.work_revision')=r.work_revision AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1) AND json_extract(e.body_json,'$.kind')='progress' ORDER BY e.sequence DESC LIMIT 1),'')
FROM tasks t JOIN task_runtime r ON r.task_id=t.id
WHERE t.run_id=(SELECT run_id FROM tasks WHERE id=?) ORDER BY t.id`, taskID)
	if err != nil {
		return RunSummary{}, err
	}
	defer rows.Close()
	summary := RunSummary{Version: 1, Counts: map[string]int{"pending": 0, "running": 0, "blocked": 0, "completed": 0}, Tasks: []SummaryTask{}}
	policyRetryEligible := map[string]bool{}
	for rows.Next() {
		var task SummaryTask
		var attempts, maxAttempts, groupTasks int
		var result, question, failure, technicalReviewFailure bool
		var adapterPayload, reviewableResult, latestProgress, diagnosticsFailure string
		if err := rows.Scan(&summary.RunID, &task.TaskID, &task.Status, &task.WorkRevision, &task.ActiveSegments, &attempts, &maxAttempts, &result, &question, &failure, &adapterPayload, &technicalReviewFailure, &groupTasks, &reviewableResult, &task.StopReason, &diagnosticsFailure, &latestProgress); err != nil {
			return RunSummary{}, err
		}
		if reviewableResult != "" && task.ActiveSegments == 0 {
			if err := json.Unmarshal([]byte(reviewableResult), &task.ReviewableResult); err != nil {
				return RunSummary{}, err
			}
		}
		if diagnosticsFailure != "" {
			if err := json.Unmarshal([]byte(diagnosticsFailure), &task.DiagnosticsFailure); err != nil {
				return RunSummary{}, err
			}
		}
		if latestProgress != "" {
			if err := json.Unmarshal([]byte(latestProgress), &task.LatestProgress); err != nil {
				return RunSummary{}, err
			}
		}
		managed := candidateManaged([]byte(adapterPayload))
		_, reviewProfileErr := reviewRetryProfile([]byte(adapterPayload))
		policyRetryEligible[task.TaskID] = (!managed && failure) || (technicalReviewFailure && reviewProfileErr == nil && groupTasks == 1)
		reviewRetryEligible := technicalReviewFailure && reviewProfileErr == nil && groupTasks == 1 && attempts < maxAttempts && attempts < 3
		task.Group = "blocked"
		task.AllowedActions = []string{"status", "collect"}
		switch task.Status {
		case "ready", "resume_queued":
			task.Group = "pending"
			task.AllowedActions = append(task.AllowedActions, "wait-events")
		case "running":
			task.Group = "running"
			task.AllowedActions = append(task.AllowedActions, "wait-events")
		case "result_ready":
			task.Group = "pending"
			if result && task.ActiveSegments == 0 {
				task.AllowedActions = append(task.AllowedActions, "accept")
			}
		case "waiting_question":
			if question && task.ActiveSegments == 0 {
				task.AllowedActions = append(task.AllowedActions, "answer")
			}
		case "interrupted":
			if task.ActiveSegments == 0 {
				if reviewRetryEligible {
					task.AllowedActions = append(task.AllowedActions, "retry")
				} else if p, _ := continuationPayload([]byte(adapterPayload)); p.Profile != nil && (p.Provider == "claude-code" || p.Provider == "antigravity-cli") && p.CandidateAction == nil {
					// Resolve the trusted source after the bounded result rows close.
				} else if restriction := resumeProfileRestriction([]byte(adapterPayload)); restriction == nil {
					task.AllowedActions = append(task.AllowedActions, "resume")
				} else {
					task.ResumeBlockedReason = restriction.Error()
				}
			}
		case "failed":
			if task.ActiveSegments == 0 && attempts < maxAttempts && attempts < 3 && ((!managed && failure) || reviewRetryEligible) {
				task.AllowedActions = append(task.AllowedActions, "retry")
			}
		case "completed", "cancelled":
			task.Group = "completed"
		}
		if task.ActiveSegments > 0 {
			task.AllowedActions = append(task.AllowedActions, "stop")
		}
		summary.Counts[task.Group]++
		summary.Tasks = append(summary.Tasks, task)
	}
	if err := rows.Err(); err != nil {
		return RunSummary{}, err
	}
	if err := rows.Close(); err != nil {
		return RunSummary{}, err
	}
	for i := range summary.Tasks {
		task := &summary.Tasks[i]
		task.TemporaryWorkspaceCleanup, e = readTemporaryWorkspaceCleanup(ctx, tx, task.TaskID)
		if e != nil {
			return RunSummary{}, e
		}
		b, e := readExecutionBudget(ctx, tx, task.TaskID)
		if e != nil {
			return RunSummary{}, e
		}
		if b.policy == nil {
			continue
		}
		task.ExecutionPolicy = b.policy
		task.EffectiveBudget = b.snapshot()
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(d.eligible_at_unix_ms),0) FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.task_id=? AND b.attempt_id IS NULL AND b.retired=0`, task.TaskID).Scan(&task.NextRetryAtUnixMS); e != nil {
			return RunSummary{}, e
		}
		actions := task.AllowedActions[:0]
		for _, action := range task.AllowedActions {
			if action != "retry" {
				actions = append(actions, action)
			}
		}
		task.AllowedActions = actions
		if task.Status == "failed" {
			open, err := hasOpenPolicyRetryAction(ctx, tx, task.TaskID)
			if err != nil {
				return RunSummary{}, err
			}
			if !open {
				continue
			}
		}
		if (task.Status == "failed" || task.Status == "interrupted") && task.ActiveSegments == 0 && policyRetryEligible[task.TaskID] && executionCapacity(ctx, tx, task.TaskID, nil) == nil {
			if _, e = executionGrant(ctx, tx, task.TaskID, ""); e == nil {
				task.AllowedActions = append(task.AllowedActions, "retry")
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return RunSummary{}, e
	}
	for i := range summary.Tasks {
		task := &summary.Tasks[i]
		if task.Status != "interrupted" || task.ActiveSegments != 0 || task.ResumeBlockedReason != "" {
			continue
		}
		source, typed, err := d.continuationSummary(ctx, task.TaskID)
		if !typed {
			continue
		}
		if err != nil {
			task.ResumeBlockedReason = err.Error()
			continue
		}
		task.ContinuationSource = source
		task.AllowedActions = append(task.AllowedActions, "resume")
	}
	return summary, nil
}
