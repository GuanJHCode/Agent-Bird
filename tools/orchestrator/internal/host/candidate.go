package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

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
	if spec.Version != 1 || profile == nil || profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite || grant.SessionID != "" {
		return nil, func() {}, nil, errors.New("candidate_workspace_profile_invalid")
	}
	binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, PlanRevision: grant.PlanRevision}
	if r := spec.Rework; r != nil {
		if r.Version != 1 || r.RunID != grant.RunID || r.TaskID != grant.TaskID || r.PreviousWorkRevision+1 != grant.WorkRevision || r.PreviousCandidateOID != spec.BaseOID || r.CandidateEventID == "" || len(r.CandidateSHA256) != 64 || r.ReviewEventID == "" || len(r.ReviewSHA256) != 64 || r.Feedback == "" || r.Acceptance == "" {
			return nil, func() {}, nil, errors.New("candidate_rework_binding_invalid")
		}
		binding.RevisionSHA256 = r.Digest()
	}
	key := hashText(grant.RunID + "\x00" + grant.TaskID + "\x00" + grant.AttemptID + "\x00" + grant.SegmentID)
	journal, err := gitopsworker.OpenJournal(filepath.Join(h.spoolRoot, ".gitops-journal", key))
	if err != nil {
		return nil, func() {}, nil, err
	}
	ctx = gitops.WithCandidateJournal(ctx, journal)
	closeJournal := func() { _ = journal.Close() }
	prepared, err := gitops.PrepareWorkspace(ctx, gitops.MaterializeRequest{RepoRoot: spec.RepoRoot, Worktree: candidateDirectory(inv.WorkingDirectory(), grant, spec.AutoDirectory), AttemptID: grant.AttemptID, BaseOID: spec.BaseOID, CandidateOID: spec.BaseOID, OrderedInputOIDs: []string{spec.BaseOID}, PlanRevision: int64(grant.PlanRevision)}, binding, spec.Paths, "refs/orchestrator/g3/candidate-"+key, journal)
	if err != nil {
		closeJournal()
		return nil, func() {}, nil, err
	}
	if err = journal.RecordWorkspacePrepared(ctx, prepared); err != nil {
		closeJournal()
		return nil, func() {}, nil, err
	}
	return func(ctx context.Context, providerResult string) ([]byte, error) {
		if len(providerResult) > 512*1024 {
			return nil, errors.New("candidate_provider_result_too_large")
		}
		if err := gitops.CheckCandidateArtifactBudget(prepared, providerResult); err != nil {
			return nil, err
		}
		ctx = gitops.WithCandidateJournal(ctx, journal)
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
	body, _ := json.Marshal(map[string]string{"status": "failed", "reason": "candidate_freeze_failed", "code": code})
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
