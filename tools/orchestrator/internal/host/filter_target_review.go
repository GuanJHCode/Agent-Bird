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

func (h *Host) prepareFilteredTargetReview(ctx context.Context, f *filterRuntime, inv contract.InvocationView, p adapter.InvocationPayload) (process.Command, actionFinalizer, func(), error) {
	cmd, noop := invocationCommand(inv), func() {}
	g := f.grant
	in, err := readAcceptedFilteredTarget(g, p, filepath.Dir(h.spoolRoot))
	if err != nil {
		return cmd, nil, noop, err
	}
	lock, err := reviewProviderLock(g, inv)
	if err != nil {
		return cmd, nil, noop, err
	}
	// The conservative bound is checked before any source Git/filter operation.
	out, err := newFilteredTargetReviewDelivery(g, in, lock)
	if err != nil {
		return cmd, nil, noop, err
	}
	if f.journal == nil || f.source != nil || f.broker == nil || f.invalid || f.cleanup != nil {
		return cmd, nil, noop, errors.New("git_filter_runtime_reused")
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	c := in.Review.Candidate
	f.source, err = gitops.OpenFilterTargetGit(ctx, c.RepoRoot, f.manifest)
	if err != nil {
		return cmd, nil, noop, err
	}
	t := p.CandidateAction.Target
	target, err := gitops.InspectFilteredTarget(ctx, f.source, f.broker, gitops.FilterTargetRequest{Worktree: t.Worktree, Ref: t.Ref, BaseOID: t.BaseOID, AttemptID: g.AttemptID, PlanRevision: g.PlanRevision, FilterScopeSHA256: g.GitFilter.ScopeSHA256, FilterReservationID: g.GitFilter.ReservationID}, c, f.journal)
	if err != nil {
		return cmd, nil, noop, err
	}
	var candidate *gitops.FilterMaterialization
	var pair *gitops.FilterTargetCandidatePair
	closeLive := func() {
		if pair != nil {
			_ = pair.Close()
		}
		if candidate != nil {
			_ = candidate.Close()
		}
		_ = target.Close()
	}
	fail := func(err error) (process.Command, actionFinalizer, func(), error) { return cmd, nil, closeLive, err }
	if !reflect.DeepEqual(target.Receipt(), out.Observation) {
		return fail(errors.New("filter_target_preflight_changed"))
	}
	candidate, err = gitops.MaterializeFilteredCandidate(ctx, f.source, f.broker, f.materializeRequest(c.RepoRoot, p.Directory, c.BaseOID, c.CandidateOID, c.OrderedInputOIDs), c, f.journal)
	if err != nil {
		return fail(err)
	}
	workspace := candidate.Receipt()
	if err = f.journal.RecordCandidateAction(ctx, "materialized", workspace); err != nil {
		return fail(err)
	}
	pair, err = gitops.BindFilteredTargetCandidate(ctx, target, candidate)
	if err != nil {
		return fail(err)
	}
	cmd.Dir = workspace.Worktree
	scratch := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "scratch")
	cmd, err = h.prepareCandidateReviewSandbox(ctx, cmd, p.Profile, scratch, g, lock)
	if err != nil {
		return fail(err)
	}
	if err = pair.Verify(ctx); err != nil {
		return fail(err)
	}
	attempted := false
	finalize := func(ctx context.Context, text string, exit int, success bool) ([]byte, error) {
		if attempted {
			return nil, errors.New("git_filter_finalizer_reused")
		}
		attempted = true
		if err := f.requireOriginalTerminal(); err != nil {
			return nil, err
		}
		if len(text) >= candidateDeliveryMaximumBytes {
			return nil, errors.New("candidate_output_limit")
		}
		ctx = gitops.WithCandidateJournal(ctx, f.journal)
		if err := pair.Verify(ctx); err != nil {
			return nil, err
		}
		decision, err := decodeCandidateReview(text)
		if !success || exit != 0 || err != nil {
			return nil, errors.New("candidate_review_invalid")
		}
		out.Review = &decision
		body, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if len(body) > candidateDeliveryMaximumBytes-1024 {
			return nil, errors.New("filter_target_review_output_budget")
		}
		err = f.journal.RecordCandidateAction(ctx, "filter_target_review_result", out)
		if verifyErr := pair.Verify(ctx); verifyErr != nil {
			return nil, errors.Join(err, verifyErr)
		}
		if err != nil {
			return nil, err
		}
		if decision.Decision != "approve" {
			return nil, &actionRejected{body}
		}
		return body, nil
	}
	return cmd, finalize, closeLive, nil
}
