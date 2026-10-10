package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

// This separate envelope cannot turn a content review into target approval.
// Review keeps its original bindings and zero Target, including ReviewRevision.
type FilteredTargetPreflightDeliveryV1 struct {
	Version         int                                `json:"version"`
	Stage           string                             `json:"stage"`
	Binding         gitops.CandidateBinding            `json:"binding"`
	InputEventID    string                             `json:"input_event_id"`
	InputSHA256     string                             `json:"input_sha256"`
	OwnerDecisionID string                             `json:"owner_decision_id"`
	Review          CandidateDelivery                  `json:"review"`
	Target          adapter.CandidateTarget            `json:"target"`
	Observation     gitops.FilteredTargetObservationV1 `json:"observation"`
}

func filteredTargetReviewSource(g contract.LaunchCommand, p adapter.InvocationPayload, in CandidateDelivery) error {
	bad := errors.New("git_filter_target_review_invalid")
	if !adapter.IsFilterTargetPreflight(p) || g.Input == nil || g.Input.Event.Artifact == nil || g.Input.Event.Artifact.HostArtifact == nil || g.Input.Event.TaskID != p.CandidateAction.SourceTask {
		return bad
	}
	source, err := adapter.DecodeInvocationPayload(g.Input.AdapterPayload)
	if err != nil || source.Kind != "" || source.CandidateAction == nil || source.CandidateAction.Operation != "review" || adapter.ValidateGitFilterPipelinePayload(source) != nil || !reflect.DeepEqual(source.GitFilter, p.GitFilter) || !reflect.DeepEqual(in.ProviderLock, source.ProviderLock) {
		return bad
	}
	c, v := in.Candidate, in.Validation
	e := g.Input.Event
	expected := gitops.CandidateBinding{RunID: g.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, WorkRevision: e.WorkRevision, PlanRevision: g.PlanRevision}
	allowed := CandidateDelivery{Version: 1, Stage: "review", Binding: expected, InputEventID: in.InputEventID, InputSHA256: in.InputSHA256, OwnerDecisionID: in.OwnerDecisionID, Candidate: c, CandidateReceiptSHA256: in.CandidateReceiptSHA256, Validation: v, ValidationDigest: in.ValidationDigest, Review: in.Review, ProviderLock: in.ProviderLock}
	if !reflect.DeepEqual(in, allowed) || e.WorkRevision != 1 || c.Binding == nil || c.Binding.RunID != g.RunID || c.Binding.PlanRevision != g.PlanRevision || c.Binding.WorkRevision != 1 || c.Filter == nil || c.Files != nil || in.InputEventID == "" || in.OwnerDecisionID == "" || !adapter.ValidCommandSHA256(in.InputSHA256) || !adapter.ValidCommandSHA256(in.CandidateReceiptSHA256) || in.Review == nil || in.Review.Decision != "approve" || strings.TrimSpace(in.Review.Summary) == "" || len(in.Review.Summary) > 64<<10 || v == nil || !v.Passed || v.ExitCode != 0 || v.Binding.RunID != g.RunID || v.Binding.PlanRevision != g.PlanRevision || v.Binding.WorkRevision != 1 || v.Binding.TaskID != source.CandidateAction.SourceTask || v.Binding.AttemptID == "" || v.Binding.SegmentID == "" || v.Binding.RevisionSHA256 != "" || v.Binding.NewBaseSHA256 != "" || v.CandidateOID != c.CandidateOID || v.TreeOID != c.TreeOID || !adapter.ValidCommandSHA256(v.ExecutableSHA256) || !adapter.ValidCommandSHA256(v.OutputSHA256) || len(v.Command) < 1 || len(v.Command) > 64 || !filepath.IsAbs(v.Command[0]) {
		return bad
	}
	vraw, err := json.Marshal(v)
	if err != nil || hashBytes(vraw) != in.ValidationDigest || p.CandidateAction.Target.Worktree != c.RepoRoot || p.CandidateAction.Target.BaseOID != c.BaseOID {
		return bad
	}
	return nil
}

func newFilteredTargetDelivery(g contract.LaunchCommand, p adapter.InvocationPayload, in CandidateDelivery, observation gitops.FilteredTargetObservationV1) (FilteredTargetPreflightDeliveryV1, error) {
	out := FilteredTargetPreflightDeliveryV1{Version: 1, Stage: "filter_target_preflight", Binding: filterCandidateBinding(g), InputEventID: g.Input.Event.EventID, InputSHA256: g.Input.Event.Artifact.SHA256, OwnerDecisionID: g.Input.DecisionCommandID, Review: in, Target: p.CandidateAction.Target, Observation: observation}
	raw, err := json.Marshal(out)
	if err != nil {
		return FilteredTargetPreflightDeliveryV1{}, err
	}
	var frozen FilteredTargetPreflightDeliveryV1
	if len(raw) > candidateDeliveryMaximumBytes-1024 || contract.DecodeBridgeStrict(raw, &frozen) != nil {
		return frozen, errors.New("filter_target_delivery_invalid")
	}
	return frozen, nil
}

func (h *Host) observeFilteredTarget(ctx context.Context, f *filterRuntime, p adapter.InvocationPayload) ([]byte, error) {
	in, err := readAcceptedCandidate(f.grant, filepath.Dir(h.spoolRoot))
	if err != nil {
		return nil, err
	}
	if err = filteredTargetReviewSource(f.grant, p, in); err != nil {
		return nil, err
	}
	if f.journal == nil || f.source != nil || f.broker == nil || f.invalid || f.cleanup != nil {
		return nil, errors.New("git_filter_runtime_reused")
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	f.source, err = gitops.OpenFilterTargetGit(ctx, in.Candidate.RepoRoot, f.manifest)
	if err != nil {
		return nil, err
	}
	target, g := p.CandidateAction.Target, f.grant
	live, err := gitops.InspectFilteredTarget(ctx, f.source, f.broker, gitops.FilterTargetRequest{Worktree: target.Worktree, Ref: target.Ref, BaseOID: target.BaseOID, AttemptID: g.AttemptID, PlanRevision: g.PlanRevision, FilterScopeSHA256: g.GitFilter.ScopeSHA256, FilterReservationID: g.GitFilter.ReservationID}, in.Candidate, f.journal)
	if err != nil {
		return nil, err
	}
	defer live.Close()
	out, err := newFilteredTargetDelivery(g, p, in, live.Receipt())
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if err = f.journal.RecordCandidateAction(ctx, "filter_target_result", out); err != nil {
		return nil, err
	}
	if err = live.Verify(ctx); err != nil {
		return nil, err
	}
	if err = live.Close(); err != nil {
		return nil, err
	}
	return body, nil
}

func filterPayloadForMetadata(meta launchMetadata) adapter.InvocationPayload {
	if meta.artifactGrant == nil {
		return adapter.InvocationPayload{}
	}
	p, _ := adapter.DecodeInvocationPayload(meta.artifactGrant.AdapterPayload)
	return p
}
