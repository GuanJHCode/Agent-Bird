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
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The authenticated accepted validator is the authority for this content-only
// stage. Its SourceTask names the producer, while this review names the validator.
func filteredReviewSource(g contract.LaunchCommand, p adapter.InvocationPayload, in CandidateDelivery) error {
	bad := errors.New("git_filter_review_source_invalid")
	if g.Input == nil || g.Input.Event.Artifact == nil || g.Input.Event.Artifact.HostArtifact == nil || p.CandidateAction == nil || p.CandidateAction.SourceTask != g.Input.Event.TaskID {
		return bad
	}
	source, err := adapter.DecodeInvocationPayload(g.Input.AdapterPayload)
	c, v := in.Candidate, in.Validation
	if err != nil || source.Kind != "candidate" || adapter.ValidateGitFilterPipelinePayload(source) != nil || source.CandidateAction == nil || source.CandidateAction.Operation != "validate" || !reflect.DeepEqual(source.GitFilter, p.GitFilter) || c.Binding == nil || c.Binding.WorkRevision != 1 || c.Filter == nil || c.Files != nil || source.CandidateAction.SourceTask != c.Binding.TaskID {
		return bad
	}
	e := g.Input.Event
	expected := gitops.CandidateBinding{RunID: g.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, WorkRevision: e.WorkRevision, PlanRevision: g.PlanRevision}
	allowed := CandidateDelivery{Version: 1, Stage: "validate", Binding: expected, InputEventID: in.InputEventID, InputSHA256: in.InputSHA256, OwnerDecisionID: in.OwnerDecisionID, Candidate: c, CandidateReceiptSHA256: in.CandidateReceiptSHA256, Validation: v, ValidationDigest: in.ValidationDigest}
	if !reflect.DeepEqual(in, allowed) || e.WorkRevision != 1 || c.Binding.RunID != g.RunID || c.Binding.PlanRevision != g.PlanRevision || in.InputEventID == "" || in.OwnerDecisionID == "" || !adapter.ValidCommandSHA256(in.InputSHA256) || !adapter.ValidCommandSHA256(in.CandidateReceiptSHA256) || v == nil || v.Binding != in.Binding || !v.Passed || v.ExitCode != 0 || v.CandidateOID != c.CandidateOID || v.TreeOID != c.TreeOID || !reflect.DeepEqual(v.Command, source.CandidateAction.Command) || v.ExecutableSHA256 != source.CandidateAction.CommandSHA256 || !adapter.ValidCommandSHA256(v.OutputSHA256) {
		return bad
	}
	raw, err := json.Marshal(v)
	if err != nil || in.ValidationDigest != hashBytes(raw) {
		return bad
	}
	return nil
}

// Start with a zero delivery, and detach retained evidence from every caller
// and journal DTO. In particular the validation binding must never become this
// review's binding, and zero Target must not acquire a ReviewRevision.
func newFilteredReviewDelivery(g contract.LaunchCommand, in CandidateDelivery, lock *adapter.ProviderLock) (CandidateDelivery, error) {
	out := CandidateDelivery{Version: 1, Stage: "review", Binding: filterCandidateBinding(g), InputEventID: g.Input.Event.EventID, InputSHA256: g.Input.Event.Artifact.SHA256, OwnerDecisionID: g.Input.DecisionCommandID, Candidate: in.Candidate, CandidateReceiptSHA256: in.CandidateReceiptSHA256, Validation: in.Validation, ValidationDigest: in.ValidationDigest, ProviderLock: lock}
	raw, err := json.Marshal(out)
	if err != nil {
		return CandidateDelivery{}, err
	}
	var frozen CandidateDelivery
	err = json.Unmarshal(raw, &frozen)
	return frozen, err
}

func (h *Host) prepareFilteredReview(ctx context.Context, f *filterRuntime, inv contract.InvocationView, p adapter.InvocationPayload) (process.Command, actionFinalizer, func(), error) {
	cmd, noop := invocationCommand(inv), func() {}
	g := f.grant
	in, err := readAcceptedCandidate(g, filepath.Dir(h.spoolRoot))
	if err != nil {
		return cmd, nil, noop, err
	}
	if err = filteredReviewSource(g, p, in); err != nil {
		return cmd, nil, noop, err
	}
	lock, err := reviewProviderLock(g, inv)
	if err != nil {
		return cmd, nil, noop, err
	}
	out, err := newFilteredReviewDelivery(g, in, lock)
	if err != nil {
		return cmd, nil, noop, err
	}
	c := in.Candidate
	if err = f.openSource(ctx, c.RepoRoot); err != nil {
		return cmd, nil, noop, err
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	live, err := gitops.MaterializeFilteredCandidate(ctx, f.source, f.broker, f.materializeRequest(c.RepoRoot, p.Directory, c.BaseOID, c.CandidateOID, c.OrderedInputOIDs), c, f.journal)
	if err != nil {
		return cmd, nil, noop, err
	}
	closeCandidate := func() { _ = live.Close() }
	fail := func(err error) (process.Command, actionFinalizer, func(), error) {
		return cmd, nil, closeCandidate, err
	}
	workspace := live.Receipt()
	if err = f.journal.RecordCandidateAction(ctx, "materialized", workspace); err != nil {
		return fail(err)
	}
	if err = live.Verify(ctx); err != nil {
		return fail(err)
	}
	cmd.Dir = workspace.Worktree
	scratch := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "scratch")
	cmd, err = h.prepareCandidateReviewSandbox(ctx, cmd, p.Profile, scratch, g, lock)
	if err != nil {
		return fail(err)
	}
	finalizeAttempted := false
	finalize := func(ctx context.Context, text string, exit int, success bool) ([]byte, error) {
		if finalizeAttempted {
			return nil, errors.New("git_filter_finalizer_reused")
		}
		finalizeAttempted = true
		if err := f.requireOriginalTerminal(); err != nil {
			return nil, err
		}
		if len(text) >= 1<<20 {
			return nil, errors.New("candidate_output_limit")
		}
		ctx = gitops.WithCandidateJournal(ctx, f.journal)
		if err := live.Verify(ctx); err != nil {
			return nil, err
		}
		decision, err := decodeCandidateReview(text)
		if !success || exit != 0 || err != nil {
			return nil, errors.New("candidate_review_invalid")
		}
		out.Review = &decision
		body, err := finishCandidateDelivery(ctx, out, decision.Decision == "approve", f.journal)
		if verifyErr := live.Verify(ctx); verifyErr != nil {
			return nil, errors.Join(err, verifyErr)
		}
		return body, err
	}
	return cmd, finalize, closeCandidate, nil
}
