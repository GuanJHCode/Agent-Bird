package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

// FilterStreams is supplied only by the original Host. Filesystem grants and
// cleanup remain with its authenticated Broker; this interface grants neither.
type FilterStreams interface {
	Binding() gitfilter.StreamBinding
	Load(context.Context, int, *os.File) error
	LoadStream(context.Context, int, func(io.Writer) error) (gitfilter.StreamLoadResult, error)
	Transform(context.Context, string, int, int) (gitfilter.Result, error)
	ReadSlot(context.Context, int, io.Writer) error
}

// FilterMaterialization has no exported fields and cannot be restored from a
// receipt. Its original source, stream owner and linked worktree stay live.
type FilterMaterializeJournal interface {
	Journal
	RecordFilterMaterialized(context.Context, MaterializeReceipt) error
}

type FilterMaterialization struct {
	source    *FilterGitContext
	streams   FilterStreams
	binding   *FilterWorktreeBinding
	receipt   MaterializeReceipt
	workspace *filterRawWorkspace
}

func MaterializeFiltered(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, journal Journal) (*FilterMaterialization, error) {
	durable, ok := journal.(FilterMaterializeJournal)
	processes, tracked := journal.(GitProcessJournal)
	if source == nil || source.source != nil || streams == nil || !ok || !tracked || req.AttemptID == "" || req.PlanRevision < 1 || req.RepoRoot != source.repoRoot || len(req.OrderedInputOIDs) < 1 || len(req.OrderedInputOIDs) > 256 {
		return nil, fail(CodeInvalidInput, "filter materialize input", nil)
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, fail(CodeInvalidInput, "filter materialize deadline", nil)
	}
	manifestSHA, err := source.manifest.Digest()
	expected := gitfilter.StreamBinding{ManifestSHA256: manifestSHA, ScopeSHA256: req.FilterScopeSHA256, ReservationID: req.FilterReservationID, AttemptID: req.AttemptID, PlanRevision: int(req.PlanRevision)}
	if err != nil || req.FilterScopeSHA256 == "" || req.FilterReservationID == "" || streams.Binding() != expected {
		return nil, fail(CodeInvalidInput, "filter stream binding", err)
	}
	req.OrderedInputOIDs = append([]string(nil), req.OrderedInputOIDs...)
	ctx = source.Context(WithCandidateJournal(ctx, processes))
	if err := source.Verify(ctx); err != nil {
		return nil, err
	}
	for _, oid := range append([]string{req.CandidateOID, req.BaseOID}, req.OrderedInputOIDs...) {
		if err := requireOID(oid); err != nil {
			return nil, err
		}
		actual, err := rev(ctx, source.repoRoot, oid+"^{commit}")
		if err != nil {
			return nil, err
		}
		if actual != oid {
			return nil, fail(CodeInvalidInput, "filter requires commit OID", nil)
		}
		ancestor, err := mergeAncestor(ctx, source.repoRoot, oid, req.CandidateOID)
		if err != nil {
			return nil, err
		}
		if !ancestor {
			return nil, fail(CodeReviewMismatch, "filter input ancestry", nil)
		}
	}
	tree, err := rev(ctx, source.repoRoot, req.CandidateOID+"^{tree}")
	if err != nil {
		return nil, err
	}
	entries, err := filterReadTree(ctx, source, req.CandidateOID)
	if err != nil {
		return nil, err
	}
	if err = filterCheckTree(ctx, source, entries); err != nil {
		return nil, err
	}
	workspace, err := filterOpenParent(req.Worktree)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			workspace.Close()
		}
	}()
	intent := IntegrationIntent{AttemptID: req.AttemptID, TargetWorktree: req.Worktree, TargetBaseOID: req.BaseOID, CandidateOID: req.CandidateOID, PlanRevision: req.PlanRevision, OrderedInputOIDs: append([]string(nil), req.OrderedInputOIDs...), CreatedAt: time.Now().UTC()}
	if err = journal.PersistIntegrationIntent(ctx, intent); err != nil {
		return nil, err
	}
	if err = workspace.verifyParent(); err != nil {
		return nil, err
	}
	if err = workspace.absent(); err != nil {
		return nil, err
	}
	if _, err = run(ctx, source.repoRoot, "worktree", "add", "--detach", "--no-checkout", req.Worktree, req.CandidateOID); err != nil {
		return nil, err
	}
	if err = workspace.capture(source); err != nil {
		return nil, errors.Join(process.ErrProcessTreeUnknown, fail(CodeUnknownObject, "filter creation ownership", err))
	}
	evidence := FilterViewEvidenceV1{Version: 1, ManifestSHA256: manifestSHA, FilterContextSHA256: source.SHA256(), Views: []FilterPathViewV1{}}
	filtered := map[string]bool{}
	for _, p := range source.manifest.Paths {
		filtered[p] = true
	}
	for _, entry := range entries {
		if err = workspace.verify(); err != nil {
			return nil, err
		}
		if !filtered[entry.path] {
			err = workspace.write(ctx, entry, func(out io.Writer) (string, error) { return filterReadBlob(ctx, source, entry, out) })
		} else {
			var canonical string
			var loaded gitfilter.StreamLoadResult
			loaded, err = streams.LoadStream(ctx, 0, func(out io.Writer) error {
				var e error
				canonical, e = filterReadBlob(ctx, source, entry, out)
				return e
			})
			if err != nil {
				return nil, err
			}
			if loaded.SHA256 != canonical || loaded.Bytes != entry.size {
				return nil, fail(CodeIdentityChanged, "filter raw slot", nil)
			}
			smudged, e := filterTransform(ctx, source, streams, "smudge", 0, 1)
			if e != nil {
				return nil, e
			}
			if smudged.InputSHA256 != canonical || smudged.InputBytes != entry.size {
				return nil, fail(CodeIdentityChanged, "filter smudge input", nil)
			}
			cleaned, e := filterTransform(ctx, source, streams, "clean", 1, 2)
			if e != nil {
				return nil, e
			}
			if cleaned.InputSHA256 != smudged.OutputSHA256 || cleaned.InputBytes != smudged.OutputBytes || cleaned.OutputSHA256 != canonical || cleaned.OutputBytes != entry.size {
				return nil, fail(CodeInvalidInput, "filter round trip", nil)
			}
			err = workspace.write(ctx, entry, func(out io.Writer) (string, error) { return smudged.OutputSHA256, streams.ReadSlot(ctx, 1, out) })
			if err == nil {
				evidence.Views = append(evidence.Views, FilterPathViewV1{Path: entry.path, CanonicalSHA256: canonical, RenderedSHA256: smudged.OutputSHA256})
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if err = evidence.ValidateBinding(source.manifest, source.SHA256()); err != nil {
		return nil, err
	}
	if err = workspace.verifyInitialIndex(); err != nil {
		return nil, err
	}
	binding, err := source.BindWorktree(ctx, req.Worktree)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			binding.Close()
		}
	}()
	child := binding.worktree
	if child.repoIdentity != workspace.rootIdentity || child.repository.digest() != workspace.anchor.digest() || child.bindingBacklink == nil || *child.bindingBacklink != workspace.backlink {
		return nil, fail(CodeIdentityChanged, "filter original worktree binding", nil)
	}
	if err = workspace.verifyInitialIndex(); err != nil {
		return nil, err
	}
	childCtx := binding.Context(ctx)
	if _, err = run(childCtx, req.Worktree, "read-tree", req.CandidateOID); err != nil {
		return nil, err
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--verify", "HEAD"}, req.CandidateOID},
		{[]string{"rev-parse", "--verify", "HEAD^{tree}"}, tree},
		{[]string{"write-tree"}, tree},
	} {
		result, e := run(childCtx, req.Worktree, check.args...)
		if e != nil {
			return nil, e
		}
		if result.Stdout != check.want {
			return nil, fail(CodeIdentityChanged, "filter materialized tree", nil)
		}
	}
	files, err := workspace.inventory(ctx)
	if err != nil {
		return nil, err
	}
	receipt := MaterializeReceipt{AttemptID: req.AttemptID, RepoRoot: source.repoRoot, Worktree: req.Worktree, CommonDir: source.commonDir, CommitOID: req.CandidateOID, TreeOID: tree, BaseOID: req.BaseOID, WorktreeIdentity: req.Worktree, RootIdentity: workspace.rootIdentity, Files: files, OrderedInputOIDs: append([]string(nil), req.OrderedInputOIDs...), PlanRevision: req.PlanRevision, Filter: &evidence}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fail(CodeInvalidInput, "filter receipt size", nil)
	}
	if err = binding.Verify(ctx); err != nil {
		return nil, err
	}
	m := &FilterMaterialization{source: source, streams: streams, binding: binding, receipt: receipt, workspace: workspace}
	if err = durable.RecordFilterMaterialized(ctx, m.Receipt()); err != nil {
		return nil, err
	}
	// The journal callback is not allowed to replace the tree it records.
	if err = workspace.verify(); err != nil {
		return nil, err
	}
	keep = true
	return m, nil
}

func filterTransform(ctx context.Context, source *FilterGitContext, streams FilterStreams, op string, input, output int) (gitfilter.Result, error) {
	if err := source.Verify(ctx); err != nil {
		return gitfilter.Result{}, err
	}
	result, err := streams.Transform(ctx, op, input, output)
	if result.Unknown || result.Started && (!result.WaitProved || !result.TreeProved) {
		err = errors.Join(err, process.ErrProcessTreeUnknown)
	}
	if err == nil && (!result.Accepted || !result.Started || !result.WaitProved || !result.TreeProved || result.OutputBytes < 0 || result.OutputBytes > gitfilter.MaximumBytes) {
		err = fail(CodeGitFailure, "filter transform", fmt.Errorf("unaccepted %s", op))
	}
	return result, source.verifyAfter(err)
}

func (m *FilterMaterialization) Receipt() MaterializeReceipt {
	if m == nil {
		return MaterializeReceipt{}
	}
	r := m.receipt
	r.OrderedInputOIDs = append([]string(nil), r.OrderedInputOIDs...)
	r.Files = make(map[string]FileIdentity, len(m.receipt.Files))
	for p, id := range m.receipt.Files {
		r.Files[p] = id
	}
	if r.Filter != nil {
		cloned := r.Filter.Clone()
		r.Filter = &cloned
	}
	return r
}
func (m *FilterMaterialization) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.binding != nil {
		err = m.binding.Close()
	}
	if m.workspace != nil {
		err = errors.Join(err, m.workspace.Close())
	}
	return err
}
