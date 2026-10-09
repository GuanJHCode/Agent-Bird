package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

const validationRetryKind = "candidate_validation_retry"

type validationRetryDecision struct {
	Kind                   string `json:"kind"`
	AdapterSHA256          string `json:"adapter_sha256"`
	InputSHA256            string `json:"input_sha256"`
	DependencyInputsSHA256 string `json:"dependency_inputs_sha256"`
	HostIdentitySHA256     string `json:"host_identity_sha256"`
	SourceBody             []byte `json:"source_body"`
	SourceSegmentID        string `json:"source_segment_id"`
	PlanRevision           int    `json:"plan_revision"`
}

func parseValidationRetry(raw string) (validationRetryDecision, error) {
	var v validationRetryDecision
	if strictDecisionJSON(raw, &v) != nil || v.Kind != validationRetryKind || !validDigest(v.AdapterSHA256) || !validDigest(v.InputSHA256) || (v.DependencyInputsSHA256 != "" && !validDigest(v.DependencyInputsSHA256)) || !validDigest(v.HostIdentitySHA256) || v.SourceSegmentID == "" || v.PlanRevision < 1 || len(v.SourceBody) == 0 {
		return v, ErrConflict
	}
	return v, nil
}
func validationRetryOriginalGrant(ctx context.Context, tx *sql.Tx, e contract.Event) (contract.LaunchCommand, error) {
	var c contract.LaunchCommand
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,c.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN report_capabilities c ON c.segment_id=s.segment_id JOIN runs p ON p.id=t.run_id WHERE s.segment_id=? AND t.id=? AND s.host_id=? AND s.command_id=?`, e.SegmentID, e.TaskID, e.ProducerID, e.CommandID).Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &raw, &c.PlanRevision)
	c.AdapterPayload = json.RawMessage(raw)
	if err != nil || c.AttemptID != e.AttemptID || c.WorkRevision != e.WorkRevision {
		return c, ErrConflict
	}
	if _, err = adapter.ValidationRetryPayload(c.AdapterPayload); err != nil {
		return c, err
	}
	if err = bindHostArtifacts(ctx, tx, &c, false); err != nil || c.ValidationTool == nil {
		return c, contract.ErrValidationToolGrant
	}
	return c, nil
}
func validationSourceGrant(ctx context.Context, tx *sql.Tx, e contract.Event) (contract.LaunchCommand, error) {
	c, err := validationRetryOriginalGrant(ctx, tx, e)
	if err != nil {
		return c, err
	}
	if err = bindHistoricalCandidateInput(ctx, tx, &c); err != nil || c.Input == nil || c.HostArtifacts == nil {
		return c, ErrConflict
	}
	return c, nil
}
func validateValidationSource(c contract.LaunchCommand, body []byte) error {
	if c.Input == nil || c.Input.Event.Artifact == nil || runtimeHash(string(body)) != c.Input.Event.Artifact.SHA256 {
		return ErrConflict
	}
	p, err := adapter.ValidationRetryPayload(c.AdapterPayload)
	if err != nil {
		return err
	}
	source, err := adapter.DecodeInvocationPayload(c.Input.AdapterPayload)
	if err != nil {
		return ErrConflict
	}
	var out newBaseDelivery
	if json.Unmarshal(body, &out) != nil || out.Version != 1 || !reworkBindingMatches(&out.Binding, c.Input.Event, c.PlanRevision) || !reworkBindingMatches(out.Candidate.Binding, c.Input.Event, c.PlanRevision) || out.Candidate.AttemptID != c.Input.Event.AttemptID || out.Candidate.PlanRevision != int64(c.PlanRevision) || !reworkOID(out.Candidate.CandidateOID) || !reworkOID(out.Candidate.TreeOID) || out.Candidate.PrivateRefExpectedOID != out.Candidate.CandidateOID || out.Candidate.BaseOID != p.CandidateAction.Target.BaseOID || out.Candidate.RepoRoot != p.CandidateAction.Target.Worktree || !validDigest(out.CandidateReceiptSHA256) || out.Candidate.CommonDir == "" || out.Candidate.Worktree == "" || out.Candidate.WorktreeIdentity != out.Candidate.Worktree || out.Candidate.RootIdentity == (gitops.DirectoryIdentity{}) || out.Candidate.RepoRootIdentity == (gitops.DirectoryIdentity{}) || out.Candidate.CommonDirIdentity == (gitops.DirectoryIdentity{}) || len(out.Candidate.Changes) > 256 {
		return ErrConflict
	}
	if source.CandidateWorkspace != nil {
		if out.Stage != "" || source.ProviderLock == nil || !reflect.DeepEqual(out.ProviderLock, source.ProviderLock) || out.Composition != nil {
			return ErrConflict
		}
	} else if !isV2Compose(c.Input.AdapterPayload) || out.Stage != "compose" || out.Composition == nil || gitops.ValidateCompositionManifest(out.Composition, out.Candidate, out.CandidateReceiptSHA256) != nil {
		return ErrConflict
	}
	return nil
}

// Only this read-only gate reads object bytes. Admission repeats immutable tuple,
// current source/vector, artifact-ref authority and physical Host identity in tx.
func (d *DB) prepareValidationRetry(ctx context.Context, e contract.Event, item *hostArtifactCommit) (*validationRetryDecision, error) {
	if item == nil {
		return nil, CodeError("validation_start_unproved")
	}
	body, err := artifacts.ReadVerifiedHostSlot(ctx, item.root, item.descriptor, item.proof)
	if err != nil {
		return nil, hostArtifactError(err)
	}
	expected, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Stage   string `json:"stage"`
		Code    string `json:"code"`
	}{1, "validation_start_failure", "validation_start", e.Failure.Code})
	if string(body) != string(expected) {
		return nil, CodeError("validation_start_unproved")
	}
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = validationRetryOriginalGrant(ctx, tx, e); err != nil {
		return nil, CodeError("validation_start_unproved")
	}
	c, err := validationSourceGrant(ctx, tx, e)
	if err != nil {
		return nil, nil
	} // ACK terminal; no unsupported retry promise.
	source, err := readCommittedHostArtifact(ctx, tx, c.Input.Event, 192*1024)
	if err != nil {
		return nil, nil
	}
	if validateValidationSource(c, source) != nil {
		return nil, nil
	}
	identity, err := reviewRetryHostIdentity(ctx, tx, &c)
	if err != nil {
		return nil, nil
	}
	saved := &validationRetryDecision{Kind: validationRetryKind, AdapterSHA256: runtimeHash(string(c.AdapterPayload)), InputSHA256: retryInputHash(c.Input), DependencyInputsSHA256: dependencyVectorHash(c.DependencyInputs), HostIdentitySHA256: identity, SourceBody: source, SourceSegmentID: c.SegmentID, PlanRevision: c.PlanRevision}
	raw, err := json.Marshal(saved)
	if err != nil || len(raw) > 256*1024 {
		return nil, nil
	}
	return saved, nil
}
func validateValidationRetryGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, saved validationRetryDecision) error {
	if saved.AdapterSHA256 != runtimeHash(string(c.AdapterPayload)) || saved.PlanRevision != c.PlanRevision || saved.InputSHA256 != retryInputHash(c.Input) || saved.DependencyInputsSHA256 != dependencyVectorHash(c.DependencyInputs) || validateValidationSource(*c, saved.SourceBody) != nil {
		return ErrConflict
	}
	identity, err := reviewRetryHostIdentity(ctx, tx, c)
	if err != nil || identity != saved.HostIdentitySHA256 {
		return ErrConflict
	}
	// Freeze both the receipt descriptor and its durable ownership, not an arbitrary file.
	var database string
	if tx.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&database) != nil {
		return ErrConflict
	}
	item, err := authorizeHostArtifact(ctx, tx, database, c.Input.Event, false)
	if err != nil || item == nil {
		return ErrConflict
	}
	var receipt string
	if tx.QueryRowContext(ctx, `SELECT proof_json FROM managed_artifact_refs WHERE id=? AND reservation_id=? AND slot='0' AND intake_id IS NULL`, item.proof.IntakeID, item.descriptor.ReservationID).Scan(&receipt) != nil || receipt != item.receipt {
		return ErrConflict
	}
	return nil
}
func authorizeValidationRetry(ctx context.Context, tx *sql.Tx, e contract.Event, raw string, saved *validationRetryDecision) (string, error) {
	if saved == nil || saved.SourceSegmentID != e.SegmentID || saved.AdapterSHA256 != runtimeHash(raw) {
		return "", ErrConflict
	}
	c, err := validationSourceGrant(ctx, tx, e)
	if err != nil {
		return "", err
	}
	if err = validateValidationRetryGrant(ctx, tx, &c, *saved); err != nil {
		return "", err
	}
	body, err := json.Marshal(saved)
	return string(body), err
}
func validateSavedValidationRetry(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord, task decisionTaskScope) error {
	saved, err := parseValidationRetry(r.payload)
	if err != nil || r.origin != "policy" || r.fallback || saved.SourceSegmentID != r.segment || saved.AdapterSHA256 != runtimeHash(task.payload) {
		return ErrConflict
	}
	var held int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_attempt_bindings WHERE action_slot=? AND task_id=? AND target_work_revision=? AND retired=0`, r.slot, task.id, task.revision).Scan(&held) != nil || held != 1 {
		return ErrConflict
	}
	e, err := decisionSource(ctx, tx, r, task.id, task.revision, false)
	if err != nil || !e.Failure.ValidValidationStart() {
		return ErrConflict
	}
	c, err := validationSourceGrant(ctx, tx, e)
	if err != nil {
		return err
	}
	return validateValidationRetryGrant(ctx, tx, &c, saved)
}
func prepareValidationRetryGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, raw string) (*reviewRetryDecision, error) {
	saved, err := parseValidationRetry(raw)
	if err != nil || saved.AdapterSHA256 != runtimeHash(string(c.AdapterPayload)) {
		return nil, ErrConflict
	}
	if _, err = adapter.ValidationRetryPayload(c.AdapterPayload); err != nil {
		return nil, err
	}
	var slot string
	if tx.QueryRowContext(ctx, `SELECT action_slot FROM decision_attempt_bindings WHERE attempt_id=? AND task_id=? AND target_work_revision=? AND retired=0`, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&slot) != nil {
		return nil, ErrConflict
	}
	identity, err := reviewRetryHostIdentity(ctx, tx, c)
	if err != nil || identity != saved.HostIdentitySHA256 {
		return nil, ErrConflict
	}
	// Shared input binder only consumes frozen hashes/vector; this never applies
	// review directory rewriting or reinterprets a review decision as validation.
	return &reviewRetryDecision{InputSHA256: saved.InputSHA256, DependencyInputsSHA256: saved.DependencyInputsSHA256}, nil
}

func validateBoundValidationRetry(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT d.revision_payload FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.attempt_id=? AND b.task_id=? AND b.target_work_revision=? AND b.retired=0`, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(raw), &kind) != nil {
		return ErrConflict
	}
	if kind.Kind != validationRetryKind {
		return nil
	}
	saved, err := parseValidationRetry(raw)
	if err != nil {
		return err
	}
	return validateValidationRetryGrant(ctx, tx, c, saved)
}

// This is a refusal gate, never ordinal-based reconstruction of authority. A
// committed validation promise targeting this or an earlier attempt cannot be
// replayed as a fresh ordinary grant when its exact durable binding is absent.
func rejectMissingValidationRetryBinding(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if _, err := adapter.ValidationRetryPayload(c.AdapterPayload); err != nil {
		return nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM retry_decisions d JOIN attempts a ON a.task_id=d.task_id WHERE d.task_id=? AND d.work_revision=? AND a.id=? AND d.next_attempt_no<=a.attempt_no AND json_extract(d.revision_payload,'$.kind')=?)`, c.TaskID, c.WorkRevision, c.AttemptID, validationRetryKind).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	return nil
}
