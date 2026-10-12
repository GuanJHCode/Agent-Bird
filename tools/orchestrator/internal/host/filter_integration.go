package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

// Called only after newOwnerExecution has verified the coordinator signature.
// The executable hash is inherited from that signed scope, never self-issued.
func (h *Host) bindFilterHelper(binding contract.OriginalOwnerBinding) error {
	scope, err := contract.OriginalOwnerScopeSHA256(binding.Scope)
	if err != nil {
		return err
	}
	pin := gitfilter.CarrierHelperExecutableExpectation{Path: binding.Scope.Executable, SHA256: binding.Scope.ExecutableSHA256}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.filterHelperOwnerScope != "" && (h.filterHelperOwnerScope != scope || h.filterHelperPin != pin) {
		return errors.New("filter_helper_owner_changed")
	}
	h.filterHelperOwnerScope, h.filterHelperPin = scope, pin
	return nil
}
func (f *filterRuntime) settleCarrier(ctx context.Context) error {
	g := f.grant.GitFilterCarrier
	if g == nil {
		return nil
	}
	if f.carrierCleanup != nil {
		return f.carrierCleanup.Validate()
	}
	if !f.carrierAttempted {
		f.carrierCleanup = &contract.GitFilterCarrierCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, FilterReservationID: g.FilterReservationID, ManifestSHA256: g.ManifestSHA256, RuntimeNamespace: g.RuntimeNamespace, Outcome: "not_created", JournalSHA256: hashBytes(nil)}
		return f.carrierCleanup.Validate()
	}
	if f.carrier == nil {
		return errors.New("filter_carrier_original_owner_missing")
	}
	receipt, err := f.carrier.Cleanup(ctx)
	if err != nil || receipt == nil {
		return errors.Join(errors.New("filter_carrier_cleanup_unproved"), err)
	}
	f.carrierCleanup = receipt
	return receipt.Validate()
}

func (h *Host) integrateFilteredTarget(ctx context.Context, f *filterRuntime, p adapter.InvocationPayload) ([]byte, error) {
	h.mu.Lock()
	pin, scope := h.filterHelperPin, h.filterHelperOwnerScope
	h.mu.Unlock()
	if scope == "" || pin.Path == "" {
		return nil, errors.New("filter_helper_signed_owner_required")
	}
	in, err := readAcceptedFilteredIntegration(f.grant, p, filepath.Dir(h.spoolRoot))
	if err != nil {
		return nil, err
	}
	out, err := newFilteredIntegrationDelivery(f.grant, in)
	if err != nil {
		return nil, err
	}
	if f.journal == nil || f.source != nil || f.broker == nil || f.invalid || f.cleanup != nil {
		return nil, errors.New("git_filter_runtime_reused")
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	c, g, target := in.Preflight.Review.Candidate, f.grant, p.CandidateAction.Target
	f.source, err = gitops.OpenFilterTargetGit(ctx, c.RepoRoot, f.manifest)
	if err != nil {
		return nil, err
	}
	live, err := gitops.InspectFilteredTarget(ctx, f.source, f.broker, gitops.FilterTargetRequest{Worktree: target.Worktree, Ref: target.Ref, BaseOID: target.BaseOID, AttemptID: g.AttemptID, PlanRevision: g.PlanRevision, FilterScopeSHA256: g.GitFilter.ScopeSHA256, FilterReservationID: g.GitFilter.ReservationID}, c, f.journal)
	if err != nil {
		return nil, err
	}
	defer live.Close()
	if !reflect.DeepEqual(live.Receipt(), freshFilteredTargetObservation(g, in.Review.Observation)) {
		return nil, errors.New("filter_target_review_changed")
	}
	candidate, err := gitops.MaterializeFilteredCandidate(ctx, f.source, f.broker, f.materializeRequest(c.RepoRoot, p.Directory, c.BaseOID, c.CandidateOID, c.OrderedInputOIDs), c, f.journal)
	if err != nil {
		return nil, err
	}
	defer candidate.Close()
	pair, err := gitops.BindFilteredTargetCandidate(ctx, live, candidate)
	if err != nil {
		return nil, err
	}
	defer pair.Close()
	transition, err := gitops.OpenFilterTargetTransition(ctx, pair)
	if err != nil {
		return nil, err
	}
	defer transition.Close()
	permits, err := transition.TransformPermits(ctx)
	if err != nil {
		return nil, err
	}
	f.carrierAttempted = true
	f.carrier, err = gitfilter.OpenCarrierExecution(ctx, gitfilter.CarrierExecutionRequest{Launch: g, Manifest: f.manifest, Broker: f.broker, Spool: f.spool, Permits: permits, Helper: pin})
	if err != nil {
		return nil, err
	}
	intent := gitops.IntegrationIntent{AttemptID: g.AttemptID, TargetWorktree: target.Worktree, TargetRef: target.Ref, TargetBaseOID: target.BaseOID, CandidateOID: c.CandidateOID, TargetIdentity: target.Worktree, InitialHEAD: target.BaseOID, InitialIndexTree: in.Preflight.Observation.BaseTreeOID, ValidationDigest: out.ValidationDigest, PlanRevision: int64(g.PlanRevision), ReviewRevision: out.Target.ReviewRevision, OrderedInputOIDs: append([]string(nil), c.OrderedInputOIDs...), CreatedAt: time.Now().UTC()}
	if err = f.journal.PersistIntegrationIntent(ctx, intent); err != nil {
		return nil, err
	}
	if err = transition.VerifyInitial(ctx); err != nil {
		return nil, err
	}
	if err = f.journal.RecordCandidateAction(ctx, "filter_integration_authorized", out); err != nil {
		return nil, err
	}
	if err = transition.VerifyInitial(ctx); err != nil {
		return nil, err
	}
	result, mergeErr := transition.MergeWithCarrier(ctx, f.carrier)
	outcome := gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: c.CandidateOID, CandidateOID: c.CandidateOID, TargetWorktree: target.Worktree, GitExitCode: result.ExitCode, ProcessPID: result.PID, RecordedAt: time.Now().UTC()}
	if mergeErr != nil {
		outcome.State, outcome.TargetHEAD, outcome.Code = gitops.IntegrationUncertain, "", "filter_integration_uncertain"
		journalErr := f.journal.RecordIntegration(context.WithoutCancel(ctx), outcome)
		return nil, errors.Join(gitops.ErrIntegrationUncertain, mergeErr, journalErr)
	}
	if err = f.journal.RecordIntegration(ctx, outcome); err != nil {
		return nil, errors.Join(gitops.ErrIntegrationUncertain, err)
	}
	if err = transition.RevalidateFinal(ctx); err != nil {
		return nil, errors.Join(gitops.ErrIntegrationUncertain, err)
	}
	out.Integration = &outcome
	if err = f.journal.RecordCandidateAction(ctx, "filter_integration_result", out); err != nil {
		return nil, errors.Join(gitops.ErrIntegrationUncertain, err)
	}
	if err = transition.RevalidateFinal(ctx); err != nil {
		return nil, errors.Join(gitops.ErrIntegrationUncertain, err)
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) > candidateDeliveryMaximumBytes-1024 {
		return nil, errors.Join(gitops.ErrIntegrationUncertain, errors.New("filter_integration_output_invalid"), err)
	}
	return body, nil
}
