package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const reviewRetryKind = "candidate_review_retry"

type reviewRetryDecision struct {
	Kind          string `json:"kind"`
	AdapterSHA256 string `json:"adapter_sha256"`
	InputSHA256   string `json:"input_sha256"`
}

func parseReviewRetry(raw string) (reviewRetryDecision, error) {
	var v reviewRetryDecision
	if json.Unmarshal([]byte(raw), &v) != nil || v.Kind != reviewRetryKind || !validDigest(v.AdapterSHA256) || !validDigest(v.InputSHA256) {
		return v, ErrConflict
	}
	return v, nil
}
func candidateManaged(raw []byte) bool {
	p, e := adapter.DecodeInvocationPayload(raw)
	return e == nil && (p.CandidateAction != nil || p.CandidateWorkspace != nil)
}
func reviewRetryProfile(raw []byte) (adapter.InvocationPayload, error) {
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || p.Kind != "" || p.Provider != string(adapter.ProviderClaude) || p.CandidateWorkspace != nil || p.CandidateAction == nil || p.CandidateAction.Version != 1 || p.CandidateAction.Operation != "review" || len(p.CandidateAction.Command) != 0 || p.Profile == nil || p.Profile.Role != adapter.Reviewer || p.Profile.Permission != adapter.ReadOnly || p.ProviderLock == nil || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || p.SessionID != "" || p.SessionKind != "" || len(p.Args) != 0 || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory || p.Directory == string(filepath.Separator) {
		return p, CodeError("candidate_review_retry_unsupported")
	}
	_, err = adapter.BuildInvocation(adapter.Request{Provider: adapter.Provider(p.Provider), Binary: p.ProviderLock.Binary, CWD: p.Directory, Prompt: p.Prompt, Permission: adapter.Permission{Mode: p.PermissionMode, Allow: p.Allow, Deny: p.Deny}, ExtraArgs: p.ExtraArgs, Profile: p.Profile, Lock: p.ProviderLock, Action: p.CandidateAction})
	if err != nil {
		return p, CodeError("candidate_review_retry_unsupported")
	}
	return p, nil
}
func retryInputHash(input *contract.AcceptedInput) string {
	body, _ := json.Marshal(input)
	return runtimeHash(string(body))
}

// Reuse the existing decision ledger; the task's original adapter stays fixed.
// The actual fresh directory is part of the normal bound launch intent.
func prepareReviewRetryGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) (*reviewRetryDecision, error) {
	var raw string
	var expectedAttempt, actualAttempt int
	err := tx.QueryRowContext(ctx, `SELECT d.revision_payload,d.next_attempt_no,a.attempt_no FROM retry_decisions d JOIN attempts a ON a.task_id=d.task_id AND d.next_attempt_no<=a.attempt_no WHERE a.id=? AND d.work_revision=? ORDER BY d.next_attempt_no DESC LIMIT 1`, c.AttemptID, c.WorkRevision).Scan(&raw, &expectedAttempt, &actualAttempt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(raw), &discriminator) != nil {
		return nil, ErrConflict
	}
	if discriminator.Kind != reviewRetryKind {
		return nil, nil
	}
	if expectedAttempt != actualAttempt {
		return nil, CodeError("candidate_review_retry_changed")
	}
	saved, err := parseReviewRetry(raw)
	if err != nil {
		return nil, err
	}
	if saved.AdapterSHA256 != runtimeHash(string(c.AdapterPayload)) {
		return nil, CodeError("candidate_review_retry_changed")
	}
	p, err := reviewRetryProfile(c.AdapterPayload)
	if err != nil {
		return nil, err
	}
	p.Directory = filepath.Join(filepath.Dir(p.Directory), ".orchestrator-review-"+runtimeHash(c.RunID + "\x00" + c.TaskID + "\x00" + c.AttemptID)[:32])
	c.AdapterPayload, err = json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

func authorizeReviewRetry(ctx context.Context, tx *sql.Tx, spec RetrySpec, raw string, e contract.Event) (string, error) {
	if _, err := reviewRetryProfile([]byte(raw)); err != nil {
		return "", err
	}
	if spec.UseNextFallback || e.Artifact != nil || (e.Kind != contract.EventStopped && e.Kind != contract.EventFailed) || e.WorkRevision != spec.WorkRevision {
		return "", CodeError("candidate_review_retry_unsupported")
	}
	var c contract.LaunchCommand
	var status, outcome, hostStatus, originalPayload string
	err := tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision,s.status,s.outcome,h.status FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN runs p ON p.id=t.run_id JOIN runtime_hosts h ON h.id=s.host_id WHERE s.segment_id=? AND t.id=? AND s.segment_id=(SELECT z.id FROM segments z JOIN attempts a2 ON a2.id=z.attempt_id WHERE a2.task_id=t.id ORDER BY a2.attempt_no DESC,z.segment_no DESC LIMIT 1)`, spec.SegmentID, spec.TaskID).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &originalPayload, &c.PlanRevision, &status, &outcome, &hostStatus)
	if err != nil || status != "exited" || outcome != e.Kind || hostStatus != "ready" || e.ProducerID != c.HostID || e.AttemptID != c.AttemptID || e.CommandID != c.CommandID {
		return "", ErrConflict
	}
	c.AdapterPayload = json.RawMessage(originalPayload)
	var groupTasks int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runtime WHERE budget_group_id=(SELECT budget_group_id FROM task_runtime WHERE task_id=?)`, spec.TaskID).Scan(&groupTasks); err != nil {
		return "", err
	}
	if groupTasks != 1 {
		return "", CodeError("candidate_review_retry_shared_budget_unsupported")
	}
	var questions int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started'))+(SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND e.kind='question' AND e.delivery_status='pending_session')`, spec.TaskID, spec.TaskID).Scan(&questions)
	if err != nil {
		return "", err
	}
	if questions != 0 {
		return "", ErrConflict
	}
	var charged int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN b.status='settled' THEN b.used_ms ELSE b.granted_ms END),0) FROM budget_runtime b JOIN task_runtime r ON r.budget_group_id=b.budget_group_id WHERE r.task_id=?`, spec.TaskID).Scan(&charged); err != nil {
		return "", err
	}
	if charged >= defaultGroupActiveMS {
		return "", CodeError("budget_exhausted")
	}
	// Reconstruct the exact previous grant, including any previous retry directory.
	if err = bindCandidateInput(ctx, tx, &c, true); err != nil {
		return "", err
	}
	if c.Input == nil {
		return "", ErrConflict
	}
	input := c.Input.Event
	_, artifact, err := readReworkEvent(ctx, tx, &reworkTask{id: input.TaskID, revision: input.WorkRevision}, ReworkEvent{EventID: input.EventID, EventRevision: input.EventRevision, EventHash: input.PayloadHash}, true)
	if err != nil {
		return "", err
	}
	if artifact.Stage != "validate" || artifact.Validation == nil || !artifact.Validation.Passed {
		return "", CodeError("candidate_validation_required")
	}
	body, err := json.Marshal(reviewRetryDecision{Kind: reviewRetryKind, AdapterSHA256: runtimeHash(raw), InputSHA256: retryInputHash(c.Input)})
	return string(body), err
}
