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
	HostIdentitySHA256     string `json:"host_identity_sha256,omitempty"`
	DependencyInputsSHA256 string `json:"dependency_inputs_sha256,omitempty"`
	Kind                   string `json:"kind"`
	AdapterSHA256          string `json:"adapter_sha256"`
	InputSHA256            string `json:"input_sha256"`
}

func parseReviewRetry(raw string) (reviewRetryDecision, error) {
	var v reviewRetryDecision
	if strictDecisionJSON(raw, &v) != nil || v.Kind != reviewRetryKind || !validDigest(v.AdapterSHA256) || !validDigest(v.InputSHA256) || v.DependencyInputsSHA256 != "" && !validDigest(v.DependencyInputsSHA256) || v.HostIdentitySHA256 != "" && !validDigest(v.HostIdentitySHA256) {
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
	if err != nil || p.Kind != "" || (p.Provider != string(adapter.ProviderClaude) && p.Provider != string(adapter.ProviderGrok)) || p.CandidateWorkspace != nil || p.CandidateAction == nil || p.CandidateAction.Version != 1 || p.CandidateAction.Operation != "review" || len(p.CandidateAction.Command) != 0 || p.Profile == nil || p.Profile.Role != adapter.Reviewer || p.Profile.Permission != adapter.ReadOnly || p.ProviderLock == nil || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || p.SessionID != "" || p.SessionKind != "" || len(p.Args) != 0 || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory || p.Directory == string(filepath.Separator) {
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
	err := tx.QueryRowContext(ctx, `SELECT d.revision_payload FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.attempt_id=? AND b.task_id=? AND b.target_work_revision=?`, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&raw)
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
	saved, err := parseReviewRetry(raw)
	if err != nil {
		return nil, err
	}
	return applyReviewRetryGrant(c, saved)
}

func applyReviewRetryGrant(c *contract.LaunchCommand, saved reviewRetryDecision) (*reviewRetryDecision, error) {
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
	return reviewRetryDecisionForGrant(ctx, tx, &c, raw)
}

// The policy path reaches this only after the Host has proven an exact
// never-started EAGAIN/EINTR. It reconstructs the original bound review input
// before the new retry promise becomes visible.
func authorizePrelaunchReviewRetry(ctx context.Context, tx *sql.Tx, e contract.Event, raw string) (string, error) {
	if _, err := reviewRetryProfile([]byte(raw)); err != nil {
		return "", err
	}
	var c contract.LaunchCommand
	var hostStatus, originalPayload string
	err := tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision,h.status FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN runs p ON p.id=t.run_id JOIN runtime_hosts h ON h.id=s.host_id WHERE s.segment_id=? AND t.id=? AND s.host_id=? AND s.command_id=?`, e.SegmentID, e.TaskID, e.ProducerID, e.CommandID).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &originalPayload, &c.PlanRevision, &hostStatus)
	if err != nil || hostStatus != "ready" || c.WorkRevision != e.WorkRevision || c.AttemptID != e.AttemptID {
		return "", ErrConflict
	}
	c.AdapterPayload = json.RawMessage(originalPayload)
	decision, err := reviewRetryDecisionForGrant(ctx, tx, &c, raw)
	if err != nil {
		return "", err
	}
	saved, err := parseReviewRetry(decision)
	if err != nil {
		return "", err
	}
	saved.HostIdentitySHA256, err = prelaunchReviewHostIdentity(ctx, tx, &c)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(saved)
	return string(body), err
}

// HostID survives a physical rebind. Automatic retry authority does not: it
// stays on the exact ready process, generation and business owner captured in
// the same transaction as the failure. Epoch is excluded so the same physical
// Host can reconnect to a restarted Coordinator without acquiring new scope.
func prelaunchReviewHostIdentity(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) (string, error) {
	var identity struct {
		HostID, LaunchID, ContextID, Generation, Executable string
		PID, OriginPID                                      int
		Birth, OriginBirth                                  string
	}
	var status, generation, executable, contextID string
	err := tx.QueryRowContext(ctx, `SELECT h.id,h.launch_id,h.origin_context_id,h.host_generation,h.executable,h.pid,h.birth,r.origin_pid,r.origin_birth,h.status,l.host_generation,l.executable,l.origin_context_id FROM runtime_hosts h JOIN host_launches l ON l.id=h.launch_id JOIN task_runtime tr ON tr.host_launch_id=l.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id WHERE h.id=? AND t.id=? AND r.id=?`, c.HostID, c.TaskID, c.RunID).Scan(&identity.HostID, &identity.LaunchID, &identity.ContextID, &identity.Generation, &identity.Executable, &identity.PID, &identity.Birth, &identity.OriginPID, &identity.OriginBirth, &status, &generation, &executable, &contextID)
	if err != nil || status != "ready" || identity.Generation != generation || identity.Executable != executable || identity.ContextID != contextID || identity.PID <= 0 || identity.Birth == "" || identity.OriginPID <= 0 || identity.OriginBirth == "" {
		return "", ErrConflict
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return runtimeHash("candidate-policy-host-v1\x00" + string(body)), nil
}

func reviewRetryDecisionForGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, raw string) (string, error) {
	if _, err := executionGrant(ctx, tx, c.TaskID, ""); err != nil {
		return "", err
	}
	// Reconstruct the exact previous grant, including any previous retry directory.
	if err := bindCandidateInput(ctx, tx, c, true); err != nil {
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
	body, err := json.Marshal(reviewRetryDecision{Kind: reviewRetryKind, AdapterSHA256: runtimeHash(raw), InputSHA256: retryInputHash(c.Input), DependencyInputsSHA256: dependencyVectorHash(c.DependencyInputs)})
	return string(body), err
}

func validateSavedPrelaunchReviewRetry(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task decisionTaskScope) error {
	saved, err := parseReviewRetry(r.payload)
	if err != nil || saved.AdapterSHA256 != runtimeHash(task.payload) {
		return ErrConflict
	}
	if _, err = reviewRetryProfile([]byte(task.payload)); err != nil || !validDigest(saved.HostIdentitySHA256) {
		return ErrConflict
	}
	var c contract.LaunchCommand
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN runs p ON p.id=t.run_id WHERE s.segment_id=? AND t.id=?`, r.segment, task.id).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &raw, &c.PlanRevision)
	if err != nil || c.WorkRevision != task.revision {
		return ErrConflict
	}
	c.AdapterPayload = json.RawMessage(raw)
	hostIdentity, err := prelaunchReviewHostIdentity(ctx, tx, &c)
	if err != nil || hostIdentity != saved.HostIdentitySHA256 {
		return ErrConflict
	}
	if err = bindCandidateInput(ctx, tx, &c, true); err != nil || c.Input == nil || saved.InputSHA256 != retryInputHash(c.Input) || saved.DependencyInputsSHA256 != dependencyVectorHash(c.DependencyInputs) {
		return ErrConflict
	}
	input := c.Input.Event
	_, artifact, err := readReworkEvent(ctx, tx, &reworkTask{id: input.TaskID, revision: input.WorkRevision}, ReworkEvent{EventID: input.EventID, EventRevision: input.EventRevision, EventHash: input.PayloadHash}, true)
	if err != nil || artifact.Stage != "validate" || artifact.Validation == nil || !artifact.Validation.Passed {
		return ErrConflict
	}
	return nil
}
