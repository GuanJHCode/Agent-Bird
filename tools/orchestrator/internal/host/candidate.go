package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
)

func (h *Host) prepareCandidate(ctx context.Context, grant contract.LaunchCommand, inv contract.InvocationView, profile *adapter.ExecutionProfile) (func(context.Context, string) ([]byte, error), func(), *gitops.PreparedWorkspace, error) {
	value, ok := inv.(interface {
		CandidateWorkspace() *adapter.CandidateWorkspace
	})
	if !ok || value.CandidateWorkspace() == nil {
		return nil, func() {}, nil, nil
	}
	spec := value.CandidateWorkspace()
	if spec.Version != 1 || profile == nil || profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite || (grant.SessionID != "" && grant.Continuation == nil) {
		return nil, func() {}, nil, errors.New("candidate_workspace_profile_invalid")
	}
	sourceGrant := grant
	if c := grant.Continuation; c != nil {
		if err := adapter.ValidateContinuationReference(c); err != nil {
			return nil, func() {}, nil, err
		}
		if c.WorkspaceSourceSegmentID == "" || c.AdapterPayloadSHA256 != hashBytes(grant.AdapterPayload) || c.OwnerContextID != h.continuationOwner {
			return nil, func() {}, nil, errors.New("continuation_workspace_binding_invalid")
		}
		sourceGrant.SegmentID = c.WorkspaceSourceSegmentID
	}
	binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, PlanRevision: grant.PlanRevision}
	binding.SegmentID = sourceGrant.SegmentID
	if r := spec.Rework; r != nil {
		if r.Version != 1 || r.RunID != grant.RunID || r.TaskID != grant.TaskID || r.PreviousWorkRevision+1 != grant.WorkRevision || r.PreviousCandidateOID != spec.BaseOID || r.CandidateEventID == "" || len(r.CandidateSHA256) != 64 || r.ReviewEventID == "" || len(r.ReviewSHA256) != 64 || r.Feedback == "" || r.Acceptance == "" {
			return nil, func() {}, nil, errors.New("candidate_rework_binding_invalid")
		}
		binding.RevisionSHA256 = r.Digest()
	}
	if n := spec.NewBaseRework; n != nil {
		if spec.Rework != nil {
			return nil, func() {}, nil, errors.New("candidate_new_base_binding_invalid")
		}
		if err := n.Validate(grant.RunID, grant.TaskID, spec.BaseOID); err != nil {
			return nil, func() {}, nil, err
		}
		if err := gitops.ValidateNewBaseTarget(ctx, spec.RepoRoot, n.TargetWorktree, n.TargetRef, n.IntegratedHEAD, n.SourceCommonDir, gitops.DirectoryIdentity(n.TargetDirectoryIdentity)); err != nil {
			return nil, func() {}, nil, err
		}
		binding.NewBaseSHA256 = n.Digest()
	}
	key := hashText(grant.RunID + "\x00" + grant.TaskID + "\x00" + grant.AttemptID + "\x00" + sourceGrant.SegmentID)
	journal, err := gitopsworker.OpenJournal(filepath.Join(h.spoolRoot, ".gitops-journal", key))
	if err != nil {
		return nil, func() {}, nil, err
	}
	ctx = gitops.WithCandidateJournal(ctx, journal)
	closeJournal := func() { _ = journal.Close() }
	prepared, err := journal.LoadWorkspacePrepared(ctx)
	if err != nil && err.Error() == "workspace_prepared_missing" && grant.Continuation == nil {
		prepared, err = gitops.PrepareWorkspace(ctx, gitops.MaterializeRequest{RepoRoot: spec.RepoRoot, Worktree: candidateDirectory(inv.WorkingDirectory(), sourceGrant, spec.AutoDirectory), AttemptID: grant.AttemptID, BaseOID: spec.BaseOID, CandidateOID: spec.BaseOID, OrderedInputOIDs: []string{spec.BaseOID}, PlanRevision: int64(grant.PlanRevision)}, binding, spec.Paths, "refs/orchestrator/g3/candidate-"+key, journal)
		if err == nil {
			prepared.OwnerContextID = h.continuationOwner
			prepared.AdapterPayloadSHA256 = hashBytes(grant.AdapterPayload)
			err = journal.RecordWorkspacePrepared(ctx, prepared)
		}
	}
	if err == nil && (prepared.Binding != binding || prepared.OwnerContextID != h.continuationOwner || prepared.AdapterPayloadSHA256 != hashBytes(grant.AdapterPayload) || prepared.Receipt.RepoRoot != spec.RepoRoot || prepared.Receipt.BaseOID != spec.BaseOID || prepared.Receipt.Worktree != candidateDirectory(inv.WorkingDirectory(), sourceGrant, spec.AutoDirectory) || !reflect.DeepEqual(prepared.Paths, spec.Paths) || prepared.PrivateRef != "refs/orchestrator/g3/candidate-"+key) {
		err = errors.New("continuation_workspace_binding_invalid")
	}
	if err == nil {
		err = gitops.ValidatePreparedWorkspace(ctx, prepared)
	}
	if err != nil {
		closeJournal()
		return nil, func() {}, nil, err
	}
	prepared.Binding.SegmentID = grant.SegmentID
	return func(ctx context.Context, providerResult string) ([]byte, error) {
		if n := spec.NewBaseRework; n != nil {
			if err := gitops.ValidateNewBaseTarget(ctx, spec.RepoRoot, n.TargetWorktree, n.TargetRef, n.IntegratedHEAD, n.SourceCommonDir, gitops.DirectoryIdentity(n.TargetDirectoryIdentity)); err != nil {
				return nil, err
			}
		}
		if len(providerResult) > 512*1024 {
			return nil, errors.New("candidate_provider_result_too_large")
		}
		if err := gitops.CheckCandidateArtifactBudget(prepared, providerResult); err != nil {
			return nil, err
		}
		ctx = gitops.WithCandidateJournal(ctx, journal)
		if receipt, found, err := journal.LoadCandidateFreeze(ctx); err != nil {
			return nil, err
		} else if found {
			if err = gitops.ValidateFrozenWorkspace(ctx, prepared, receipt); err != nil {
				return nil, err
			}
			return gitops.CandidateArtifact(providerResult, receipt)
		}
		receipt, err := gitops.FreezeWorkspace(ctx, prepared, journal)
		if err != nil {
			return nil, err
		}
		return gitops.CandidateArtifact(providerResult, receipt)
	}, closeJournal, &prepared, nil
}

func candidateFailure(err error) []byte {
	var artifact interface{ SafeArtifact() []byte }
	if errors.As(err, &artifact) {
		return artifact.SafeArtifact()
	}
	code := "candidate_freeze_failed"
	// Preserve only exact, source-defined action codes. Arbitrary error text may
	// contain Provider output or local paths and must not enter normal artifacts.
	switch err.Error() {
	case "candidate_review_invalid", "candidate_action_artifact_too_large",
		"candidate_integration_not_started", "candidate_input_order_changed":
		code = err.Error()
	}
	var ge *gitops.Error
	if errors.As(err, &ge) {
		code = string(ge.Code)
	}
	diagnostic := map[string]any{"status": "failed", "reason": "candidate_freeze_failed", "code": code}
	if code == "candidate_review_invalid" {
		diagnostic["writer_version"] = 2
	}
	body, _ := json.Marshal(diagnostic)
	return body
}

// An automatic path is a template, never an existing task's worktree. Durable
// attempt/segment identity gives retries a fresh tree while preserving evidence.
func candidateDirectory(base string, grant contract.LaunchCommand, automatic bool) string {
	if !automatic {
		return base
	}
	return base + ".attempt-" + hashText(grant.RunID + "\x00" + grant.TaskID + "\x00" + grant.AttemptID + "\x00" + grant.SegmentID)[:24]
}
