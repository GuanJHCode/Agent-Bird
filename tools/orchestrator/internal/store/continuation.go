package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type providerBinding struct {
	version, revision                                                         int
	run, task, attempt, provider, lock, profile, cwd, payloadHash             string
	controller, owner, sessionKind, sessionID, sessionEvent, workspaceSegment string
}

var managedCodexSessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Match the continuation quota triggers' UTF-8 BLOB lengths and fixed numeric
// charge. Callers pass the same captured values used by the following SQL.
func continuationStoredBytes(fixed int64, fields ...string) (int64, error) {
	if fixed < 0 {
		return 0, CodeError("control_spool_full")
	}
	for _, field := range fields {
		size := int64(len(field))
		if size > math.MaxInt64-fixed {
			return 0, CodeError("control_spool_full")
		}
		fixed += size
	}
	return fixed, nil
}

// Only complete, initially unbound profiles enter the typed continuation path.
// Candidate actions have a separate immutable-input/retry protocol.
func continuationPayload(raw []byte) (adapter.InvocationPayload, error) {
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil {
		return p, CodeError("invalid_adapter_payload")
	}
	if p.Profile == nil {
		return p, nil
	}
	if p.CandidateAction != nil {
		return p, CodeError("candidate_resume_unsupported")
	}
	if p.Provider == string(adapter.ProviderCodex) {
		// Private Host tasks keep the ordinary immutable payload, but cannot
		// enter the general/legacy Codex constructor. This validates only the
		// supported readonly initial descriptor; physical seals remain Host's
		// responsibility and public execution guards remain closed.
		if p.Kind != "provider" || p.ProviderLock == nil || p.SessionKind != "" || p.SessionID != "" || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || len(p.Args) != 0 {
			return p, CodeError("profile_resume_not_verified")
		}
		_, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p.ProviderLock.Binary, Lock: p.ProviderLock, Profile: p.Profile, CWD: p.Directory, Prompt: p.Prompt, Permission: adapter.Permission{Mode: p.PermissionMode, Allow: p.Allow, Deny: p.Deny}, Workspace: p.CandidateWorkspace, Action: p.CandidateAction, ExtraArgs: p.ExtraArgs})
		if err != nil {
			return p, CodeError("profile_resume_not_verified")
		}
		return p, nil
	}
	if p.Provider != string(adapter.ProviderClaude) && p.Provider != string(adapter.ProviderAGY) {
		return p, CodeError("profile_resume_not_verified")
	}
	if p.Kind != "" || p.ProviderLock == nil || p.SessionKind != "" || p.SessionID != "" || p.BinaryPath != "" || p.BinaryVersion != "" || p.BinarySHA256 != "" || len(p.Args) != 0 {
		return p, CodeError("profile_resume_not_verified")
	}
	_, err = adapter.BuildInvocation(adapter.Request{Provider: adapter.Provider(p.Provider), Binary: p.ProviderLock.Binary, Lock: p.ProviderLock, Profile: p.Profile, CWD: p.Directory, Prompt: p.Prompt, Permission: adapter.Permission{Mode: p.PermissionMode, Allow: p.Allow, Deny: p.Deny}, Workspace: p.CandidateWorkspace, ExtraArgs: p.ExtraArgs})
	if err != nil {
		return p, CodeError("profile_resume_not_verified")
	}
	return p, nil
}

func readProviderBinding(ctx context.Context, tx *sql.Tx, attempt string) (providerBinding, error) {
	b := providerBinding{}
	err := tx.QueryRowContext(ctx, `SELECT version,run_id,task_id,attempt_id,work_revision,provider,provider_lock_json,profile_json,original_cwd,adapter_payload_sha256,controller_thread,owner_context_id,session_kind,session_id,session_event_id,workspace_source_segment_id FROM attempt_provider_bindings WHERE attempt_id=?`, attempt).Scan(&b.version, &b.run, &b.task, &b.attempt, &b.revision, &b.provider, &b.lock, &b.profile, &b.cwd, &b.payloadHash, &b.controller, &b.owner, &b.sessionKind, &b.sessionID, &b.sessionEvent, &b.workspaceSegment)
	return b, err
}

func currentProviderBinding(ctx context.Context, tx *sql.Tx, event contract.Event) (providerBinding, adapter.InvocationPayload, error) {
	b := providerBinding{version: 1, run: event.RunID, task: event.TaskID, attempt: event.AttemptID, revision: event.WorkRevision}
	var raw, run string
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT t.run_id,r.work_revision,r.adapter_payload,p.controller_thread,p.origin_context_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN runs p ON p.id=t.run_id WHERE t.id=?`, event.TaskID).Scan(&run, &revision, &raw, &b.controller, &b.owner)
	if err != nil || run != event.RunID || revision != event.WorkRevision {
		return b, adapter.InvocationPayload{}, ErrConflict
	}
	p, err := continuationPayload([]byte(raw))
	if err != nil {
		return b, p, err
	}
	lock, _ := json.Marshal(p.ProviderLock)
	profile, _ := json.Marshal(p.Profile)
	b.provider, b.lock, b.profile, b.cwd, b.payloadHash = p.Provider, string(lock), string(profile), p.Directory, runtimeHash(raw)
	return b, p, nil
}

func sameProviderBinding(a, b providerBinding) bool {
	return a.version == b.version && a.run == b.run && a.task == b.task && a.attempt == b.attempt && a.revision == b.revision && a.provider == b.provider && a.lock == b.lock && a.profile == b.profile && a.cwd == b.cwd && a.payloadHash == b.payloadHash && a.controller == b.controller && a.owner == b.owner
}

func typedProviderEvent(ctx context.Context, tx *sql.Tx, event contract.Event) bool {
	var raw string
	if tx.QueryRowContext(ctx, `SELECT adapter_payload FROM task_runtime WHERE task_id=?`, event.TaskID).Scan(&raw) != nil {
		return false
	}
	var p struct {
		Provider        string          `json:"provider"`
		Profile         json.RawMessage `json:"profile"`
		CandidateAction json.RawMessage `json:"candidate_action"`
	}
	// Candidate actions execute their ordinary native session, but are not
	// admitted to continuation. Their existing resume/answer guards stay in
	// continuationPayload; don't turn a Session event into a resume request.
	return json.Unmarshal([]byte(raw), &p) == nil && (len(p.CandidateAction) == 0 || string(p.CandidateAction) == "null") && len(p.Profile) > 0 && string(p.Profile) != "null" && (p.Provider == string(adapter.ProviderClaude) || p.Provider == string(adapter.ProviderAGY) || p.Provider == string(adapter.ProviderCodex))
}

func (d *DB) freezeProviderBinding(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	if !typedProviderEvent(ctx, tx, event) {
		return nil
	}
	b, p, err := currentProviderBinding(ctx, tx, event)
	if err != nil {
		return err
	}
	wantKind := "session-id"
	if p.Provider == string(adapter.ProviderAGY) {
		wantKind = "conversation-id"
	}
	if event.SessionKind != wantKind || event.SessionID == "" || len(event.SessionID) > 256 || strings.ContainsAny(event.SessionID, "/\\ \r\n\x00") {
		return CodeError("invalid_session")
	}
	if p.Provider == string(adapter.ProviderCodex) && !managedCodexSessionID.MatchString(event.SessionID) {
		return CodeError("invalid_session")
	}
	old, err := readProviderBinding(ctx, tx, event.AttemptID)
	if err == nil {
		if !sameProviderBinding(old, b) || old.sessionKind != event.SessionKind || old.sessionID != event.SessionID {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if p.CandidateWorkspace != nil {
		b.workspaceSegment = event.SegmentID
	}
	b.sessionKind, b.sessionID, b.sessionEvent = event.SessionKind, event.SessionID, event.EventID
	if len(b.lock)+len(b.profile)+len(b.cwd)+len(b.controller)+len(b.owner) > 8192 {
		return CodeError("continuation_binding_too_large")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	incoming, err := continuationStoredBytes(16, b.lock, b.profile, b.cwd, b.controller, b.owner, b.sessionID, b.sessionKind, b.sessionEvent, b.workspaceSegment, b.payloadHash, b.attempt, b.task, b.run, b.provider, stamp)
	if err != nil {
		return err
	}
	// runtime_events has already charged this transaction. Admit the binding
	// against that updated usage so individually fitting writes cannot exceed
	// the limit when combined.
	if err = d.storageAdmission(ctx, tx, event.RunID, incoming, false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO attempt_provider_bindings(attempt_id,version,run_id,task_id,work_revision,provider,provider_lock_json,profile_json,original_cwd,adapter_payload_sha256,controller_thread,owner_context_id,session_kind,session_id,session_event_id,workspace_source_segment_id,created_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.attempt, b.run, b.task, b.revision, b.provider, b.lock, b.profile, b.cwd, b.payloadHash, b.controller, b.owner, b.sessionKind, b.sessionID, b.sessionEvent, b.workspaceSegment, stamp)
	return mapConflict(err)
}

func validateTypedQuestionBinding(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	if !typedProviderEvent(ctx, tx, event) {
		return nil
	}
	current, _, err := currentProviderBinding(ctx, tx, event)
	if err != nil {
		return err
	}
	bound, err := readProviderBinding(ctx, tx, event.AttemptID)
	if err != nil {
		return CodeError("resume_session_unbound")
	}
	if !sameProviderBinding(bound, current) || event.SessionKind != bound.sessionKind || event.SessionID != bound.sessionID || event.QuestionKind != "technical" {
		return ErrConflict
	}
	var sessionKind, sessionID string
	if tx.QueryRowContext(ctx, `SELECT session_kind,session_id FROM segment_sessions WHERE segment_id=? AND attempt_id=?`, event.SegmentID, event.AttemptID).Scan(&sessionKind, &sessionID) != nil || sessionKind != bound.sessionKind || sessionID != bound.sessionID {
		return CodeError("resume_session_unbound")
	}
	return nil
}

func checkContinuationBudget(ctx context.Context, tx *sql.Tx, task, attempt string) error {
	var max, charged int64
	var group string
	if err := tx.QueryRowContext(ctx, `SELECT max_active_ms,budget_group_id FROM task_runtime WHERE task_id=?`, task).Scan(&max, &group); err != nil {
		return err
	}
	used, err := attemptChargedActiveMS(ctx, tx, attempt)
	if err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, group).Scan(&charged); err != nil {
		return err
	}
	if used >= max || charged >= defaultGroupActiveMS {
		return CodeError("budget_exhausted")
	}
	return nil
}

func readContinuationSource(ctx context.Context, tx *sql.Tx, eventID, task, segment string, report bool) (contract.Event, string, error) {
	var e contract.Event
	var raw string
	sourceKind := "host"
	err := tx.QueryRowContext(ctx, `SELECT body_json,event_revision,action_slot,payload_hash FROM runtime_events WHERE event_id=? AND task_id=? AND segment_id=?`, eventID, task, segment).Scan(&raw, &e.EventRevision, &e.ActionSlot, &e.PayloadHash)
	if errors.Is(err, sql.ErrNoRows) && report {
		sourceKind = "report"
		err = tx.QueryRowContext(ctx, `SELECT e.body_json,e.event_revision,e.action_slot,e.payload_hash FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=? AND c.task_id=? AND c.segment_id=?`, eventID, task, segment).Scan(&raw, &e.EventRevision, &e.ActionSlot, &e.PayloadHash)
	}
	if err != nil {
		return e, "", ErrConflict
	}
	var body contract.Event
	if json.Unmarshal([]byte(raw), &body) != nil {
		return e, "", ErrConflict
	}
	body.EventRevision, body.ActionSlot, body.PayloadHash = e.EventRevision, e.ActionSlot, e.PayloadHash
	return body, sourceKind, nil
}

func trustedContinuationRef(ctx context.Context, tx *sql.Tx, event contract.Event, mode, sourceKind string) (*contract.ContinuationRef, providerBinding, error) {
	current, payload, err := currentProviderBinding(ctx, tx, event)
	if err != nil {
		return nil, current, err
	}
	if payload.Provider == string(adapter.ProviderCodex) && (mode != contract.ContinuationQuestion || sourceKind != "host") {
		return nil, current, CodeError("codex_continuation_source_unsupported")
	}
	if payload.Provider == "claude-code" && !adapter.ClaudeHistorySupported(payload.ProviderLock.Binary) {
		return nil, current, CodeError("claude_session_history_not_verified")
	}
	b, err := readProviderBinding(ctx, tx, event.AttemptID)
	if err != nil {
		return nil, b, CodeError("resume_session_unbound")
	}
	if !sameProviderBinding(b, current) {
		return nil, b, ErrConflict
	}
	var status, attempt string
	if tx.QueryRowContext(ctx, `SELECT status,attempt_id FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&status, &attempt) != nil || status != "exited" || attempt != b.attempt {
		return nil, b, ErrConflict
	}
	ref := &contract.ContinuationRef{Version: 1, Mode: mode, SourceKind: sourceKind, SourceSegmentID: event.SegmentID, SourceEventID: event.EventID, SourceEventRevision: event.EventRevision, SourceEventSHA256: event.PayloadHash, SessionEventID: b.sessionEvent, AdapterPayloadSHA256: b.payloadHash, OwnerContextID: b.owner, WorkspaceSourceSegmentID: b.workspaceSegment}
	return ref, b, nil
}

func (d *DB) insertContinuationDecision(ctx context.Context, tx *sql.Tx, ref *contract.ContinuationRef, b providerBinding, e contract.Event, command, question, answerHash string) error {
	raw, err := json.Marshal(ref)
	if err != nil || len(raw) > 4096 {
		return CodeError("continuation_binding_too_large")
	}
	id := "continuation-" + runtimeHash(e.EventID + "\x00" + e.ActionSlot)[:32]
	stamp, status := time.Now().UTC().Format(time.RFC3339Nano), "queued"
	incoming, err := continuationStoredBytes(24, ref.SourceKind, string(raw), id, b.run, b.task, b.attempt, ref.Mode, e.SegmentID, e.EventID, e.PayloadHash, e.ActionSlot, command, question, answerHash, "", status, stamp)
	if err != nil {
		return err
	}
	if err = d.storageAdmission(ctx, tx, b.run, incoming, false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO continuation_decisions(decision_id,version,run_id,task_id,attempt_id,work_revision,mode,source_kind,source_segment_id,source_event_id,source_event_revision,source_event_sha256,action_slot,command_id,question_id,answer_sha256,ref_json,status,created_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, b.run, b.task, b.attempt, b.revision, ref.Mode, ref.SourceKind, e.SegmentID, e.EventID, e.EventRevision, e.PayloadHash, e.ActionSlot, command, question, answerHash, string(raw), status, stamp)
	return mapConflict(err)
}

func (d *DB) queueQuestionContinuation(ctx context.Context, tx *sql.Tx, spec AnswerSpec) error {
	var raw, segment, attempt, kind, session string
	if err := tx.QueryRowContext(ctx, `SELECT r.adapter_payload,q.segment_id,q.attempt_id,q.session_kind,q.session_id FROM task_runtime r JOIN runtime_questions q ON q.task_id=r.task_id WHERE q.question_id=? AND q.task_id=?`, spec.QuestionID, spec.TaskID).Scan(&raw, &segment, &attempt, &kind, &session); err != nil {
		return err
	}
	p, err := continuationPayload([]byte(raw))
	if p.Profile == nil && err == nil {
		return nil
	}
	if err != nil {
		return err
	}
	var eventID string
	err = tx.QueryRowContext(ctx, `SELECT event_id FROM runtime_events WHERE task_id=? AND segment_id=? AND json_extract(body_json,'$.question_id')=? UNION ALL SELECT e.event_id FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND c.segment_id=? AND json_extract(e.body_json,'$.question_id')=? LIMIT 1`, spec.TaskID, segment, spec.QuestionID, spec.TaskID, segment, spec.QuestionID).Scan(&eventID)
	if err != nil {
		return ErrConflict
	}
	e, sourceKind, err := readContinuationSource(ctx, tx, eventID, spec.TaskID, segment, true)
	if err != nil {
		return err
	}
	if e.Kind != contract.EventQuestion || e.QuestionID != spec.QuestionID || e.QuestionRevision != spec.QuestionRevision || e.WorkRevision != spec.WorkRevision || e.AttemptID != attempt {
		return ErrConflict
	}
	ref, b, err := trustedContinuationRef(ctx, tx, e, contract.ContinuationQuestion, sourceKind)
	if err != nil {
		return err
	}
	if kind != b.sessionKind || session != b.sessionID {
		return ErrConflict
	}
	if err = checkContinuationBudget(ctx, tx, spec.TaskID, attempt); err != nil {
		return err
	}
	return d.insertContinuationDecision(ctx, tx, ref, b, e, "answer-"+runtimeHash(spec.QuestionID + "\x00" + spec.Answer)[:32], spec.QuestionID, runtimeHash(spec.Answer))
}

func (d *DB) bindContinuationGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, replay bool) error {
	p, err := continuationPayload(c.AdapterPayload)
	if p.Profile == nil && err == nil {
		return nil
	}
	// Initial starts and unsupported profiles retain their existing paths.
	var raw, id, question, answerHash, mode, sourceKind string
	if replay {
		err = tx.QueryRowContext(ctx, `SELECT decision_id,ref_json,question_id,answer_sha256,mode,source_kind FROM continuation_decisions WHERE target_segment_id=? AND attempt_id=? AND task_id=? AND work_revision=? AND status='claimed'`, c.SegmentID, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&id, &raw, &question, &answerHash, &mode, &sourceKind)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT decision_id,ref_json,question_id,answer_sha256,mode,source_kind FROM continuation_decisions WHERE attempt_id=? AND task_id=? AND work_revision=? AND status='queued'`, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&id, &raw, &question, &answerHash, &mode, &sourceKind)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if c.SessionID != "" || c.QuestionID != "" {
			return CodeError("resume_session_unbound")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = continuationPayload(c.AdapterPayload); err != nil {
		return err
	}
	var ref contract.ContinuationRef
	if json.Unmarshal([]byte(raw), &ref) != nil || adapter.ValidateContinuationReference(&ref) != nil || ref.SourceKind != sourceKind || ref.Mode != mode || ref.AdapterPayloadSHA256 != runtimeHash(string(c.AdapterPayload)) {
		return ErrConflict
	}
	e, actualKind, sourceErr := readContinuationSource(ctx, tx, ref.SourceEventID, c.TaskID, ref.SourceSegmentID, sourceKind == "report")
	if sourceErr != nil || actualKind != sourceKind || e.AttemptID != c.AttemptID || e.WorkRevision != c.WorkRevision || e.EventRevision != ref.SourceEventRevision || e.PayloadHash != ref.SourceEventSHA256 || (mode == contract.ContinuationQuestion && (e.Kind != contract.EventQuestion || e.QuestionID != question || e.QuestionRevision != c.QuestionRevision)) || (mode == contract.ContinuationInterrupted && e.Kind != contract.EventStopped) {
		return ErrConflict
	}
	if _, _, err = trustedContinuationRef(ctx, tx, e, mode, sourceKind); err != nil {
		return err
	}
	b, err := readProviderBinding(ctx, tx, c.AttemptID)
	if err != nil || b.run != c.RunID || b.task != c.TaskID || b.revision != c.WorkRevision || b.payloadHash != ref.AdapterPayloadSHA256 || b.owner != ref.OwnerContextID || b.sessionEvent != ref.SessionEventID || b.workspaceSegment != ref.WorkspaceSourceSegmentID {
		return ErrConflict
	}
	var owner, controller string
	if tx.QueryRowContext(ctx, `SELECT origin_context_id,controller_thread FROM runs WHERE id=?`, c.RunID).Scan(&owner, &controller) != nil || owner != b.owner || controller != b.controller {
		return ErrConflict
	}
	if mode == contract.ContinuationQuestion {
		if question != c.QuestionID || runtimeHash(c.Answer) != answerHash || c.SessionKind != b.sessionKind || c.SessionID != b.sessionID {
			return ErrConflict
		}
	} else if mode != contract.ContinuationInterrupted || question != "" || c.QuestionID != "" || c.Answer != "" {
		return ErrConflict
	}
	c.SessionKind, c.SessionID, c.Continuation = b.sessionKind, b.sessionID, &ref
	if !replay {
		status := "claimed"
		// The guarded old row is queued with an empty target. No other charged
		// field changes, so reserve only the actual positive UPDATE delta.
		growth, err := continuationStoredBytes(int64(len(status)-len("queued")), c.SegmentID)
		if err != nil {
			return err
		}
		if err = d.storageAdmission(ctx, tx, c.RunID, growth, false); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE continuation_decisions SET status=?,target_segment_id=? WHERE decision_id=? AND status='queued' AND target_segment_id=''`, status, c.SegmentID, id)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
	}
	return nil
}
