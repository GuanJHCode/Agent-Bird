package gitops

import (
	"context"
	"errors"
	"fmt"
)

var ErrIntegrationUncertain = errors.New("integration_uncertain")

// Managed operations keep the source Host's strict admission, bounded output,
// immutable-object and owned-process rules across validation and integration.
func ManagedMaterialize(ctx context.Context, req MaterializeRequest, journal Journal) (MaterializeReceipt, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, req.RepoRoot); err != nil {
		return MaterializeReceipt{}, err
	}
	receipt, err := Materialize(ctx, req, journal)
	if err != nil {
		return receipt, err
	}
	if receipt.CommitOID != req.CandidateOID {
		return MaterializeReceipt{}, fail(CodeTargetDrift, "materialize", fmt.Errorf("candidate changed"))
	}
	if err = CheckMaterialized(ctx, receipt); err != nil {
		return MaterializeReceipt{}, err
	}
	return receipt, nil
}

func CheckMaterialized(ctx context.Context, r MaterializeReceipt) error {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, r.Worktree); err != nil {
		return err
	}
	id, err := directoryIdentity(r.Worktree)
	if err != nil {
		return err
	}
	if id != r.RootIdentity {
		return fail(CodeIdentityChanged, "validation", fmt.Errorf("worktree changed"))
	}
	oid, err := rev(ctx, r.Worktree, "HEAD")
	if err != nil {
		return err
	}
	tree, err := rev(ctx, r.Worktree, "HEAD^{tree}")
	if err != nil {
		return err
	}
	if oid != r.CommitOID || tree != r.TreeOID {
		return fail(CodeTargetDrift, "validation", fmt.Errorf("candidate changed"))
	}
	dirty, _, err := targetStatus(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if dirty {
		return fail(CodeTargetDirty, "validation", fmt.Errorf("validation modified candidate"))
	}
	files, err := snapshotWithContext(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if files[".git"] != r.Files[".git"] {
		return fail(CodeIdentityChanged, "validation", fmt.Errorf("git link changed"))
	}
	return nil
}

func TargetReviewBinding(ctx context.Context, repo, target, ref, base, candidate string, inputs []string, plan int64) (ReviewBinding, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, target); err != nil {
		return ReviewBinding{}, err
	}
	root, common, err := validateRepo(ctx, target)
	if err != nil {
		return ReviewBinding{}, err
	}
	repoCommon, err := commonDir(ctx, repo)
	if err != nil {
		return ReviewBinding{}, err
	}
	if root != CanonicalPath(target) || common != repoCommon {
		return ReviewBinding{}, fail(CodeInvalidInput, "target", fmt.Errorf("repository mismatch"))
	}
	actualRef, _, err := gitText(ctx, target, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return ReviewBinding{}, err
	}
	actualBase, err := rev(ctx, target, "HEAD")
	if err != nil {
		return ReviewBinding{}, err
	}
	if actualRef != ref || actualBase != base {
		return ReviewBinding{}, fail(CodeTargetDrift, "target", fmt.Errorf("target changed"))
	}
	id, err := directoryIdentity(target)
	if err != nil {
		return ReviewBinding{}, err
	}
	return ReviewBinding{TargetWorktreeIdentity: target, TargetDirectoryIdentity: id, TargetRef: ref, TargetBaseOID: base, FinalCandidateOID: candidate, OrderedInputOIDs: inputs, PlanRevision: plan}, nil
}

func ManagedIntegrate(ctx context.Context, req IntegrationRequest) (IntegrationOutcome, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, req.RepoRoot); err != nil {
		return IntegrationOutcome{}, err
	}
	if err := candidateRepositoryAllowed(ctx, req.TargetWorktree); err != nil {
		return IntegrationOutcome{}, err
	}
	out, err := Integrate(ctx, req)
	if out.State == IntegrationUncertain {
		return out, errors.Join(ErrIntegrationUncertain, err)
	}
	return out, err
}
