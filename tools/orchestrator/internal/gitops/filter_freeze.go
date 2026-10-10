package gitops

import (
	"context"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// FilterPreparedWorkspace is original-Host-only state. Neither its cloned
// Workspace nor a journal record can restore this live owner.
type FilterPreparedWorkspace struct {
	mu                sync.Mutex
	materialized      *FilterMaterialization
	prepared          PreparedWorkspace
	attempted, closed bool
}

func filterJournalContext(ctx context.Context, source *FilterGitContext, journal any) (context.Context, error) {
	j, ok := journal.(GitProcessJournal)
	if !ok || source == nil || source.source != nil {
		return nil, fail(CodeInvalidInput, "filter original journal/source", nil)
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, fail(CodeInvalidInput, "filter original deadline", nil)
	}
	return source.Context(WithCandidateJournal(ctx, j)), nil
}

func PrepareFilteredWorkspace(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, binding CandidateBinding, paths []string, ref string, journal Journal) (*FilterPreparedWorkspace, error) {
	ctx, err := filterJournalContext(ctx, source, journal)
	if err != nil {
		return nil, err
	}
	if binding.RunID == "" || binding.TaskID == "" || binding.AttemptID != req.AttemptID || binding.SegmentID == "" || binding.WorkRevision < 1 || binding.PlanRevision < 1 || int64(binding.PlanRevision) != req.PlanRevision || binding.RevisionSHA256 != "" || binding.NewBaseSHA256 != "" || req.BaseOID != req.CandidateOID || len(req.OrderedInputOIDs) != 1 || req.OrderedInputOIDs[0] != req.BaseOID || len(paths) < 1 || len(paths) > 256 {
		return nil, fail(CodeInvalidInput, "filtered prepare binding", nil)
	}
	if err = validatePrivateRef(ref); err != nil {
		return nil, err
	}
	paths = append([]string(nil), paths...)
	for i, p := range paths {
		if !filterSafePath(p) || strings.EqualFold(path.Base(p), ".gitattributes") || i > 0 && paths[i-1] >= p {
			return nil, fail(CodeInvalidInput, "filtered owned path", nil)
		}
		for _, prior := range paths[:i] {
			a, b := strings.ToLower(p), strings.ToLower(prior)
			if strings.EqualFold(p, prior) || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return nil, fail(CodeInvalidInput, "filtered owned path alias", nil)
			}
		}
	}
	active, err := candidateOperationInProgress(ctx, source.repoRoot)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, fail(CodeTargetDirty, "filtered prepare operation", nil)
	}
	// This is only a preflight; freeze still uses an expected-zero atomic create.
	if _, exists, e := privateRefOID(ctx, source.repoRoot, ref); e != nil {
		return nil, e
	} else if exists {
		return nil, fail(CodeCollision, "filtered private ref already exists", nil)
	}
	live, err := MaterializeFiltered(ctx, source, streams, req, journal)
	if err != nil {
		return nil, err
	}
	prepared := PreparedWorkspace{Receipt: live.Receipt(), Binding: binding, Paths: paths, PrivateRef: ref, CommonDirIdentity: source.commonIdentity, RepoRootIdentity: source.repoIdentity}
	return &FilterPreparedWorkspace{materialized: live, prepared: prepared}, nil
}
func (p *FilterPreparedWorkspace) Workspace() PreparedWorkspace {
	if p == nil {
		return PreparedWorkspace{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.prepared
	r.Receipt = p.materialized.Receipt()
	r.Paths = append([]string(nil), p.prepared.Paths...)
	return r
}
func (p *FilterPreparedWorkspace) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.materialized != nil {
		return p.materialized.Close()
	}
	return nil
}
func FreezeFilteredWorkspace(ctx context.Context, p *FilterPreparedWorkspace, journal CandidateJournal) (CandidateReceipt, error) {
	return CandidateReceipt{}, fail(CodeInvalidInput, "filtered freeze not implemented", nil)
}

// The caller authenticates the accepted delivery. This boundary binds its
// immutable objects/evidence to the original source, never adopts its process.
func checkFilteredCandidateRef(ctx context.Context, source *FilterGitContext, c CandidateReceipt) error {
	if c.Filter == nil || c.Filter.ValidateBinding(source.manifest, source.SHA256()) != nil || c.Binding == nil || c.Binding.RunID == "" || c.Binding.TaskID == "" || c.Binding.SegmentID == "" || c.Binding.WorkRevision < 1 || c.Binding.AttemptID != c.AttemptID || c.PlanRevision < 1 || int64(c.Binding.PlanRevision) != c.PlanRevision || c.RepoRoot != source.repoRoot || c.CommonDir != source.commonDir || c.RepoRootIdentity != source.repoIdentity || c.CommonDirIdentity != source.commonIdentity || c.WorktreeIdentity != c.Worktree || !filepath.IsAbs(c.Worktree) || CanonicalPath(c.Worktree) != c.Worktree || c.RootIdentity == (DirectoryIdentity{}) || c.PrivateRefExpectedOID != c.CandidateOID {
		return fail(CodeReviewMismatch, "filtered accepted candidate binding", nil)
	}
	if err := validatePrivateRef(c.PrivateRef); err != nil {
		return err
	}
	for _, oid := range []string{c.BaseOID, c.CandidateOID, c.TreeOID} {
		if err := requireOID(oid); err != nil {
			return err
		}
	}
	if err := source.Verify(ctx); err != nil {
		return err
	}
	previous, err := source.BindWorktree(ctx, c.Worktree)
	if err != nil {
		return err
	}
	defer previous.Close()
	if previous.worktree.repoIdentity != c.RootIdentity {
		return fail(CodeIdentityChanged, "filtered source worktree", nil)
	}
	ref, exists, err := privateRefOID(ctx, source.repoRoot, c.PrivateRef)
	if err != nil {
		return err
	}
	if !exists || ref != c.CandidateOID {
		return fail(CodeTargetDrift, "filtered candidate ref", nil)
	}
	tree, err := rev(ctx, source.repoRoot, c.CandidateOID+"^{tree}")
	if err != nil {
		return err
	}
	if tree != c.TreeOID {
		return fail(CodeTargetDrift, "filtered candidate tree", nil)
	}
	ancestor, err := mergeAncestor(ctx, source.repoRoot, c.BaseOID, c.CandidateOID)
	if err != nil {
		return err
	}
	if !ancestor {
		return fail(CodeTargetDrift, "filtered candidate ancestry", nil)
	}
	return previous.Verify(ctx)
}
func MaterializeFilteredCandidate(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, accepted CandidateReceipt, journal Journal) (*FilterMaterialization, error) {
	ctx, err := filterJournalContext(ctx, source, journal)
	if err != nil {
		return nil, err
	}
	accepted = cloneFilteredCandidate(accepted)
	if req.RepoRoot != accepted.RepoRoot || req.CandidateOID != accepted.CandidateOID || req.BaseOID != accepted.BaseOID || req.PlanRevision != accepted.PlanRevision || !reflect.DeepEqual(req.OrderedInputOIDs, accepted.OrderedInputOIDs) || req.Worktree == accepted.Worktree {
		return nil, fail(CodeReviewMismatch, "filtered validator input", nil)
	}
	if err = checkFilteredCandidateRef(ctx, source, accepted); err != nil {
		return nil, err
	}
	live, err := MaterializeFiltered(ctx, source, streams, req, journal)
	if err != nil {
		return nil, err
	}
	if live.receipt.TreeOID != accepted.TreeOID || !reflect.DeepEqual(live.receipt.Filter, accepted.Filter) {
		live.Close()
		return nil, fail(CodeReviewMismatch, "filtered validator views", nil)
	}
	if err = checkFilteredCandidateRef(ctx, source, accepted); err != nil {
		live.Close()
		return nil, err
	}
	if err = live.Verify(ctx); err != nil {
		live.Close()
		return nil, err
	}
	return live, nil
}

func cloneFilteredIntent(in CandidateIntent) CandidateIntent {
	if in.Binding != nil {
		v := *in.Binding
		in.Binding = &v
	}
	in.Changes = append([]CandidateChange(nil), in.Changes...)
	in.OrderedInputOIDs = append([]string(nil), in.OrderedInputOIDs...)
	if in.Filter != nil {
		v := in.Filter.Clone()
		in.Filter = &v
	}
	return in
}
func cloneFilteredCandidate(in CandidateReceipt) CandidateReceipt {
	if in.Binding != nil {
		v := *in.Binding
		in.Binding = &v
	}
	in.Changes = append([]CandidateChange(nil), in.Changes...)
	in.OrderedInputOIDs = append([]string(nil), in.OrderedInputOIDs...)
	if in.Files != nil {
		files := make(map[string]FileIdentity, len(in.Files))
		for p, id := range in.Files {
			files[p] = id
		}
		in.Files = files
	}
	if in.Filter != nil {
		v := in.Filter.Clone()
		in.Filter = &v
	}
	return in
}
