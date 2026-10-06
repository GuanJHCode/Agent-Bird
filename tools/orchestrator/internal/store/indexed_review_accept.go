package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

// Store decodes only receipt evidence. It never imports Host or reruns Git.
type indexedResultArtifact struct {
	Version                int                             `json:"version"`
	Stage                  string                          `json:"stage"`
	Binding                gitops.CandidateBinding         `json:"binding"`
	Candidate              gitops.CandidateReceipt         `json:"candidate"`
	CandidateReceiptSHA256 string                          `json:"candidate_receipt_sha256"`
	InputEventID           string                          `json:"input_event_id"`
	InputSHA256            string                          `json:"input_sha256"`
	OwnerDecisionID        string                          `json:"owner_decision_id"`
	ProviderLock           *adapter.ProviderLock           `json:"provider_lock"`
	ConfigGeneration       *contract.ConfigGenerationRef   `json:"config_generation"`
	Validation             json.RawMessage                 `json:"validation"`
	ValidationDigest       string                          `json:"validation_digest"`
	ReviewIndex            *contract.IndexedReviewManifest `json:"review_index"`
	ReviewCoverage         *contract.IndexedReviewCoverage `json:"review_coverage"`
	ReviewAssignmentSHA256 string                          `json:"review_assignment_sha256"`
	ReviewInputSHA256      string                          `json:"review_input_sha256"`
	ReviewSnapshotSHA256   string                          `json:"review_snapshot_sha256"`
	ReviewScope            string                          `json:"review_scope"`
	DependencyInputsSHA256 string                          `json:"dependency_inputs_sha256"`
	Target                 gitops.ReviewBinding            `json:"target"`
	Review                 *struct {
		Decision string `json:"decision"`
		Summary  string `json:"summary"`
	} `json:"review"`
	Integration *gitops.IntegrationOutcome `json:"integration"`
}

func readIndexedResultArtifact(e contract.Event) (indexedResultArtifact, error) {
	var d indexedResultArtifact
	bad := CodeError("candidate_indexed_result_invalid")
	r := e.Artifact
	if r == nil || !filepath.IsAbs(r.Path) || r.Size < 1 || r.Size > 1024*1024 || r.SHA256 != e.PayloadHash || !validDigest(r.SHA256) {
		return d, bad
	}
	f, err := os.OpenFile(r.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return d, bad
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != r.Size {
		return d, bad
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || int64(len(raw)) != r.Size || runtimeHash(string(raw)) != r.SHA256 || json.Unmarshal(raw, &d) != nil {
		return d, bad
	}
	return d, nil
}

func validIndexedResultEvidence(d indexedResultArtifact, plan int) bool {
	m, c := d.ReviewIndex, d.Candidate
	var v struct {
		CandidateOID string                  `json:"candidate_oid"`
		TreeOID      string                  `json:"tree_oid"`
		Binding      gitops.CandidateBinding `json:"binding"`
		Passed       bool                    `json:"passed"`
	}
	return d.Version == 1 && m != nil && contract.ValidateIndexedReviewManifest(*m) == nil && c.Binding != nil && c.Binding.PlanRevision == plan && m.CandidateReceiptSHA256 == d.CandidateReceiptSHA256 && m.BaseOID == c.BaseOID && m.CandidateOID == c.CandidateOID && m.TreeOID == c.TreeOID && json.Unmarshal(d.Validation, &v) == nil && v.Passed && v.CandidateOID == c.CandidateOID && v.TreeOID == c.TreeOID && v.Binding.RunID == c.Binding.RunID && v.Binding.PlanRevision == plan && v.Binding.WorkRevision > 0 && v.Binding.TaskID != "" && v.Binding.AttemptID != "" && v.Binding.SegmentID != "" && d.ValidationDigest == contract.IndexedReviewJSONSHA256(d.Validation) && d.Target.FinalCandidateOID == c.CandidateOID && d.Target.PlanRevision == int64(plan) && d.Target.ValidationDigest == d.ValidationDigest && reflect.DeepEqual(d.Target.OrderedInputOIDs, c.OrderedInputOIDs)
}

// Called inside the existing acceptance transaction before any decision/task
// writes. The earlier idempotent-decision branch deliberately bypasses it.
func validateIndexedResultAccept(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT adapter_payload FROM task_runtime WHERE task_id=?`, e.TaskID).Scan(&raw); err != nil {
		return err
	}
	var selector struct {
		Action *struct {
			Index json.RawMessage `json:"review_index"`
		} `json:"candidate_action"`
	}
	if raw == "" {
		return nil
	}
	if json.Unmarshal([]byte(raw), &selector) != nil {
		return ErrConflict
	}
	if selector.Action == nil || len(selector.Action.Index) == 0 || string(selector.Action.Index) == "null" {
		return nil
	}
	_, err := validateIndexedResult(ctx, tx, e, []byte(raw))
	return err
}

func validateIndexedResult(ctx context.Context, tx *sql.Tx, e contract.Event, raw []byte) (indexedResultArtifact, error) {
	bad := CodeError("candidate_indexed_result_invalid")
	d, err := readIndexedResultArtifact(e)
	if err != nil {
		return d, err
	}
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || p.CandidateAction == nil || p.CandidateAction.ReviewIndex == nil {
		return d, bad
	}
	a, ri := p.CandidateAction, p.CandidateAction.ReviewIndex
	if a.Version != 1 || ri.Version != 1 || ri.Mode != "indexed_candidate_review_v1" || ri.Shards < 1 || ri.Shards > 63 || ri.ReviewOrdinal < 0 || ri.ReviewOrdinal >= ri.Shards || d.Stage != a.Operation {
		return d, bad
	}
	var plan int
	var intent, status, outcome string
	if tx.QueryRowContext(ctx, `SELECT r.plan_revision,s.launch_intent_hash,s.status,s.outcome FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN runs r ON r.id=t.run_id WHERE r.id=? AND t.id=? AND s.segment_id=? AND s.host_id=? AND s.command_id=?`, e.RunID, e.TaskID, e.SegmentID, e.ProducerID, e.CommandID).Scan(&plan, &intent, &status, &outcome) != nil || status != "exited" || outcome != "result" || !validDigest(intent) {
		return d, bad
	}
	b := d.Binding
	if b.RunID != e.RunID || b.TaskID != e.TaskID || b.AttemptID != e.AttemptID || b.SegmentID != e.SegmentID || b.WorkRevision != e.WorkRevision || b.PlanRevision != plan || d.Candidate.Binding == nil || d.Candidate.Binding.RunID != e.RunID || !validIndexedResultEvidence(d, plan) || !reflect.DeepEqual(d.ConfigGeneration, p.ConfigGeneration) {
		return d, bad
	}
	input, err := acceptedCandidateInput(ctx, tx, e.RunID, a.SourceTask)
	if err != nil {
		return d, err
	}
	source, err := readIndexedResultArtifact(input.Event)
	if err != nil {
		return d, err
	}
	sp, err := adapter.DecodeInvocationPayload(input.AdapterPayload)
	if err != nil || !reflect.DeepEqual(p.ConfigGeneration, sp.ConfigGeneration) || d.InputEventID != input.Event.EventID || d.InputSHA256 != input.Event.PayloadHash || d.OwnerDecisionID != input.DecisionCommandID || d.CandidateReceiptSHA256 != source.CandidateReceiptSHA256 || !reflect.DeepEqual(d.Candidate, source.Candidate) {
		return d, bad
	}
	if a.Operation == "validate" {
		var v struct {
			Binding gitops.CandidateBinding `json:"binding"`
		}
		if source.Stage != "" || sp.CandidateWorkspace == nil || source.Candidate.Binding == nil || source.Candidate.Binding.TaskID != input.Event.TaskID || source.Candidate.Binding.AttemptID != input.Event.AttemptID || source.Candidate.Binding.SegmentID != input.Event.SegmentID || source.Candidate.Binding.WorkRevision != input.Event.WorkRevision || json.Unmarshal(d.Validation, &v) != nil || !reflect.DeepEqual(v.Binding, d.Binding) {
			return d, bad
		}
		return d, nil
	}
	if !validIndexedResultEvidence(source, plan) || !reflect.DeepEqual(d.ConfigGeneration, source.ConfigGeneration) || contract.IndexedReviewJSONSHA256(d.ReviewIndex) != contract.IndexedReviewJSONSHA256(source.ReviewIndex) || d.ValidationDigest != source.ValidationDigest || sp.CandidateAction == nil || sp.CandidateAction.ReviewIndex == nil || ri.Version != sp.CandidateAction.ReviewIndex.Version || ri.Mode != sp.CandidateAction.ReviewIndex.Mode || ri.Shards != sp.CandidateAction.ReviewIndex.Shards {
		return d, bad
	}
	target, sourceTarget := d.Target, source.Target
	target.ReviewRevision, sourceTarget.ReviewRevision = 0, 0
	if !reflect.DeepEqual(target, sourceTarget) {
		return d, bad
	}
	proof := d.ReviewCoverage
	if proof == nil || contract.ValidateIndexedReviewCoverage(*d.ReviewIndex, *proof) != nil || proof.Shards != ri.Shards || proof.ReviewOrdinal != ri.ReviewOrdinal || !reflect.DeepEqual(proof.ConfigGeneration, p.ConfigGeneration) || d.ReviewAssignmentSHA256 != proof.AssignmentSHA256 || d.ReviewInputSHA256 != proof.ConsumerInputSHA256 || d.ReviewSnapshotSHA256 != proof.ConsumerSnapshotSHA256 || d.ReviewScope != contract.IndexedReviewScope || d.Review == nil || d.Review.Decision != "approve" || d.Review.Summary == "" {
		return d, bad
	}
	switch a.Operation {
	case "review":
		if source.Stage != "validate" || sp.CandidateAction.Operation != "validate" || proof.GrantIntentSHA256 != intent || proof.AcceptedInputSHA256 != contract.IndexedReviewJSONSHA256(input) || d.Target.ReviewRevision != int64(e.WorkRevision) || p.Provider != string(adapter.ProviderGrok) || p.ProviderLock == nil || p.ProviderLock.Provider != adapter.ProviderGrok || p.ProviderLock.Version != 1 || p.ProviderLock.Protocol != adapter.ProtocolID(adapter.ProviderGrok) || !validDigest(p.ProviderLock.Binary.SHA256) || p.ProviderLock.Binary.Path == "" || p.ProviderLock.Binary.Version == "" || !reflect.DeepEqual(d.ProviderLock, p.ProviderLock) || p.Profile == nil || p.Profile.Version != 1 || p.Profile.Role != adapter.Reviewer || p.Profile.Permission != adapter.ReadOnly {
			return d, bad
		}
	case "integrate":
		if source.Stage != "review" || sp.CandidateAction.Operation != "review" || !reflect.DeepEqual(proof, source.ReviewCoverage) || !reflect.DeepEqual(d.ProviderLock, source.ProviderLock) || d.Target.ReviewRevision != source.Target.ReviewRevision || d.Integration == nil || d.Integration.State != gitops.IntegrationIntegrated || d.Integration.CandidateOID != d.Candidate.CandidateOID {
			return d, bad
		}
		vector, err := loadDependencyInputs(ctx, tx, e.SegmentID)
		if err != nil || vector == nil || vector.Version != 1 || len(vector.Inputs) != ri.Shards || d.DependencyInputsSHA256 != dependencyVectorHash(vector) {
			return d, bad
		}
		seen := map[int]bool{}
		anchor := false
		for _, item := range vector.Inputs {
			current, err := acceptedCandidateInput(ctx, tx, e.RunID, item.Event.TaskID)
			if err != nil || contract.IndexedReviewJSONSHA256(current) != contract.IndexedReviewJSONSHA256(item) {
				return d, bad
			}
			shardPayload, err := adapter.DecodeInvocationPayload(item.AdapterPayload)
			if err != nil || shardPayload.CandidateAction == nil || shardPayload.CandidateAction.Operation != "review" {
				return d, bad
			}
			shard, err := validateIndexedResult(ctx, tx, item.Event, item.AdapterPayload)
			if err != nil || shard.ReviewCoverage == nil || shard.ReviewCoverage.Shards != ri.Shards || seen[shard.ReviewCoverage.ReviewOrdinal] || shard.ReviewCoverage.ManifestSHA256 != proof.ManifestSHA256 || shard.ReviewCoverage.AcceptedInputSHA256 != proof.AcceptedInputSHA256 || !reflect.DeepEqual(shard.Candidate, d.Candidate) || shard.ValidationDigest != d.ValidationDigest || !reflect.DeepEqual(shard.ConfigGeneration, d.ConfigGeneration) {
				return d, bad
			}
			seen[shard.ReviewCoverage.ReviewOrdinal] = true
			if item.Event.TaskID == a.SourceTask {
				anchor = contract.IndexedReviewJSONSHA256(item) == contract.IndexedReviewJSONSHA256(input)
			}
		}
		if len(seen) != ri.Shards || !anchor {
			return d, bad
		}
	default:
		return d, bad
	}
	return d, nil
}
