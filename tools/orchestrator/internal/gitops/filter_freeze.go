package gitops

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
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
	if ctx == nil {
		return nil, fail(CodeInvalidInput, "filter context", nil)
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
	if _, exists, e := filterPrivateRefOID(ctx, source, ref); e != nil {
		return nil, e
	} else if exists {
		return nil, fail(CodeCollision, "filtered private ref already exists", nil)
	}
	live, err := MaterializeFiltered(ctx, source, streams, req, journal)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		for existing, id := range live.workspace.owned {
			a, b := strings.ToLower(p), strings.ToLower(existing)
			if (a == b && (p != existing || os.FileMode(id.identity.Mode).IsDir())) || strings.HasPrefix(b, a+"/") || (strings.HasPrefix(a, b+"/") && !os.FileMode(id.identity.Mode).IsDir()) {
				live.Close()
				return nil, fail(CodeInvalidInput, "filtered owned base alias", nil)
			}
		}
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
	if p == nil {
		return CandidateReceipt{}, fail(CodeInvalidInput, "filtered freeze owner", nil)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.attempted || p.materialized == nil || journal == nil {
		return CandidateReceipt{}, fail(CodeInvalidInput, "filtered freeze lifetime", nil)
	}
	p.attempted = true
	m, r := p.materialized, p.prepared.Receipt
	ctx, err := filterJournalContext(ctx, m.source, journal)
	if err != nil {
		return CandidateReceipt{}, err
	}
	lock, err := acquireCandidateLock(ctx, r.CommonDir)
	if err != nil {
		return CandidateReceipt{}, err
	}
	defer releaseIntegrationLock(lock)
	childCtx := m.binding.Context(ctx)
	if err = filterFreezeState(ctx, m, r.CommitOID, r.TreeOID); err != nil {
		return CandidateReceipt{}, err
	}
	active, err := candidateOperationInProgress(childCtx, r.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if active {
		return CandidateReceipt{}, fail(CodeTargetDirty, "filtered freeze operation", nil)
	}
	if _, exists, e := filterPrivateRefOID(ctx, m.source, p.prepared.PrivateRef); e != nil {
		return CandidateReceipt{}, e
	} else if exists {
		return CandidateReceipt{}, fail(CodeCollision, "filtered ref already exists", nil)
	}
	allowed, filtered := map[string]bool{}, map[string]bool{}
	for _, path := range p.prepared.Paths {
		allowed[path] = true
	}
	for _, path := range m.source.manifest.Paths {
		filtered[path] = true
	}
	observed, err := filterObserveChanges(ctx, m.workspace, allowed, filtered)
	if err != nil {
		return CandidateReceipt{}, err
	}
	baseEntries, err := filterReadTree(ctx, m.source, r.CommitOID)
	if err != nil {
		return CandidateReceipt{}, err
	}
	base := map[string]filterTreeEntry{}
	for _, e := range baseEntries {
		base[e.path] = e
	}
	// Before any object/intent write, check attributes on the complete final path
	// vector, including newly declared files which could inherit a wildcard.
	paths := []string{}
	for path, e := range observed.owned {
		if path != ".git" && !os.FileMode(e.identity.Mode).IsDir() {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	finalEntries := make([]filterTreeEntry, 0, len(paths))
	for _, path := range paths {
		e, exists := base[path]
		if !exists {
			e = filterTreeEntry{path: path, size: observed.owned[path].identity.Size}
		}
		finalEntries = append(finalEntries, e)
	}
	if err = filterCheckTree(ctx, m.source, finalEntries); err != nil {
		return CandidateReceipt{}, err
	}
	evidence := r.Filter.Clone()
	views := map[string]int{}
	for i, v := range evidence.Views {
		views[v.Path] = i
	}
	changes := []CandidateChange{}
	blobs := map[string]filterFrozenBlob{}
	for _, path := range p.prepared.Paths {
		current, present := observed.owned[path]
		original, existed := base[path]
		if !present {
			if existed {
				changes = append(changes, CandidateChange{Path: path, Deleted: true})
			}
			continue
		}
		mode := "100644"
		if os.FileMode(current.identity.Mode).Perm()&0111 != 0 {
			mode = "100755"
		}
		old := m.workspace.owned[path]
		var blob filterFrozenBlob
		if existed && current.sha256 == old.sha256 {
			sha := current.sha256
			if filtered[path] {
				sha = evidence.Views[views[path]].CanonicalSHA256
			}
			blob = filterFrozenBlob{oid: original.oid, sha: sha, size: original.size, filtered: filtered[path]}
		} else if filtered[path] {
			blob, err = filterRoundTripObserved(ctx, m, observed, path)
			if err != nil {
				return CandidateReceipt{}, err
			}
		} else {
			blob, err = filterHashObserved(ctx, observed, path, len(r.CommitOID), io.Discard)
			if err != nil {
				return CandidateReceipt{}, err
			}
		}
		if filtered[path] {
			evidence.Views[views[path]] = FilterPathViewV1{Path: path, CanonicalSHA256: blob.sha, RenderedSHA256: current.sha256}
		}
		if !existed || blob.oid != original.oid || mode != original.mode {
			changes = append(changes, CandidateChange{Path: path, Mode: mode, BlobOID: blob.oid})
			blobs[path] = blob
		}
	}
	if len(changes) == 0 {
		return CandidateReceipt{}, fail(CodeInvalidInput, "filtered no changes", nil)
	}
	if err = evidence.ValidateBinding(m.source.manifest, m.source.SHA256()); err != nil {
		return CandidateReceipt{}, err
	}
	files, err := observed.inventory(ctx)
	if err != nil {
		return CandidateReceipt{}, err
	}
	boundBinding := p.prepared.Binding
	receipt := CandidateReceipt{Binding: &boundBinding, Changes: changes, AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, Worktree: r.Worktree, CommonDir: r.CommonDir, BaseOID: r.CommitOID, CandidateOID: strings.Repeat("f", 64), TreeOID: strings.Repeat("f", 64), OrderedInputOIDs: append([]string(nil), r.OrderedInputOIDs...), RootIdentity: r.RootIdentity, WorktreeIdentity: r.WorktreeIdentity, RepoRootIdentity: p.prepared.RepoRootIdentity, CommonDirIdentity: p.prepared.CommonDirIdentity, Files: files, PlanRevision: r.PlanRevision, PrivateRef: p.prepared.PrivateRef, PrivateRefExpectedOID: strings.Repeat("f", 64), Filter: &evidence}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if len(raw) > (1<<20)-1024 {
		return CandidateReceipt{}, fail(CodeInvalidInput, "filtered candidate receipt size", nil)
	}
	if _, err = CandidateArtifact("", receipt); err != nil {
		return CandidateReceipt{}, err
	}
	intent := CandidateIntent{Binding: &boundBinding, Changes: changes, AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, Worktree: r.Worktree, BaseOID: r.CommitOID, PrivateRef: p.prepared.PrivateRef, OrderedInputOIDs: append([]string(nil), r.OrderedInputOIDs...), PlanRevision: r.PlanRevision, CreatedAt: time.Now().UTC(), Filter: &evidence}
	if err = journal.PersistCandidateIntent(ctx, cloneFilteredIntent(intent)); err != nil {
		return CandidateReceipt{}, err
	}
	if err = filterFreezeState(ctx, m, r.CommitOID, r.TreeOID); err != nil {
		return CandidateReceipt{}, err
	}
	if _, err = observed.inventory(ctx); err != nil {
		return CandidateReceipt{}, err
	}
	for _, change := range changes {
		if !change.Deleted {
			if err = filterWriteObservedBlob(ctx, m, observed, change.Path, blobs[change.Path]); err != nil {
				return CandidateReceipt{}, err
			}
		}
	}
	var index strings.Builder
	for _, change := range changes {
		if change.Deleted {
			index.WriteString("0 " + strings.Repeat("0", len(r.CommitOID)) + "\t" + change.Path + "\x00")
		} else {
			index.WriteString(change.Mode + " " + change.BlobOID + "\t" + change.Path + "\x00")
		}
	}
	if _, err = freezeGit(childCtx, r.Worktree, strings.NewReader(index.String()), "update-index", "-z", "--index-info"); err != nil {
		return CandidateReceipt{}, err
	}
	tree, err := freezeGit(childCtx, r.Worktree, nil, "write-tree")
	if err != nil {
		return CandidateReceipt{}, err
	}
	tree = strings.TrimSpace(tree)
	if err = requireOID(tree); err != nil {
		return CandidateReceipt{}, err
	}
	if err = verifyFrozenChanges(childCtx, r.Worktree, r.CommitOID, tree, changes); err != nil {
		return CandidateReceipt{}, err
	}
	entries, err := filterReadTree(ctx, m.source, tree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if err = filterCheckTree(ctx, m.source, entries); err != nil {
		return CandidateReceipt{}, err
	}
	if _, err = observed.inventory(ctx); err != nil {
		return CandidateReceipt{}, err
	}
	candidate, err := freezeGit(childCtx, r.Worktree, nil, "commit-tree", tree, "-p", r.CommitOID, "-m", "orchestrator: freeze filtered candidate "+DigestDetail(boundBinding.RunID+"\x00"+boundBinding.TaskID+"\x00"+boundBinding.SegmentID))
	if err != nil {
		return CandidateReceipt{}, err
	}
	candidate = strings.TrimSpace(candidate)
	if err = requireOID(candidate); err != nil {
		return CandidateReceipt{}, err
	}
	for _, check := range []struct{ suffix, want string }{{"^", r.CommitOID}, {"^{tree}", tree}} {
		got, e := rev(ctx, m.source.repoRoot, candidate+check.suffix)
		if e != nil {
			return CandidateReceipt{}, e
		}
		if got != check.want {
			return CandidateReceipt{}, fail(CodeTargetDrift, "filtered commit structure", nil)
		}
	}
	if _, err = run(childCtx, r.Worktree, "update-ref", "--no-deref", "HEAD", candidate, r.CommitOID); err != nil {
		return CandidateReceipt{}, err
	}
	refIntent := CandidateRefIntent{AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, PrivateRef: p.prepared.PrivateRef, CandidateOID: candidate, CreatedAt: time.Now().UTC()}
	if err = journal.PersistCandidateRefIntent(ctx, refIntent); err != nil {
		return CandidateReceipt{}, err
	}
	// Strict create-only CAS: even an existing identical value is a collision.
	if _, err = run(ctx, r.RepoRoot, "update-ref", "--no-deref", p.prepared.PrivateRef, candidate, strings.Repeat("0", len(candidate))); err != nil {
		return CandidateReceipt{}, err
	}
	receipt.CandidateOID, receipt.PrivateRefExpectedOID, receipt.TreeOID = candidate, candidate, tree
	verify := func() error {
		if e := filterFreezeState(ctx, m, candidate, tree); e != nil {
			return e
		}
		got, exists, e := filterPrivateRefOID(ctx, m.source, p.prepared.PrivateRef)
		if e != nil {
			return e
		}
		if !exists || got != candidate {
			return fail(CodeTargetDrift, "filtered published ref", nil)
		}
		actual, e := observed.inventory(ctx)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(actual, files) {
			return fail(CodeIdentityChanged, "filtered frozen inventory", nil)
		}
		return nil
	}
	if err = verify(); err != nil {
		return CandidateReceipt{}, err
	}
	if err = journal.RecordCandidateFreeze(ctx, cloneFilteredCandidate(receipt)); err != nil {
		return CandidateReceipt{}, err
	}
	if err = verify(); err != nil {
		return CandidateReceipt{}, err
	}
	return cloneFilteredCandidate(receipt), nil
}

// The caller authenticates the accepted delivery. This boundary binds its
// immutable objects/evidence to the original source, never adopts its process.
func checkFilteredCandidateRef(ctx context.Context, source *FilterGitContext, c CandidateReceipt) error {
	if err := validateFilteredChanges(c.Changes); err != nil {
		return err
	}
	if c.Filter == nil || c.Filter.ValidateBinding(source.manifest, source.SHA256()) != nil || c.Binding == nil || c.Binding.RunID == "" || c.Binding.TaskID == "" || c.Binding.SegmentID == "" || c.Binding.WorkRevision < 1 || c.AttemptID == "" || c.Binding.RevisionSHA256 != "" || c.Binding.NewBaseSHA256 != "" || len(c.OrderedInputOIDs) != 1 || c.OrderedInputOIDs[0] != c.BaseOID || c.CandidateOID == c.BaseOID || len(c.Changes) < 1 || len(c.Changes) > 256 || c.Binding.AttemptID != c.AttemptID || c.PlanRevision < 1 || int64(c.Binding.PlanRevision) != c.PlanRevision || c.RepoRoot != source.repoRoot || c.CommonDir != source.commonDir || c.RepoRootIdentity != source.repoIdentity || c.CommonDirIdentity != source.commonIdentity || c.WorktreeIdentity != c.Worktree || !filepath.IsAbs(c.Worktree) || CanonicalPath(c.Worktree) != c.Worktree || c.RootIdentity == (DirectoryIdentity{}) || c.PrivateRefExpectedOID != c.CandidateOID {
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
	ref, exists, err := filterPrivateRefOID(ctx, source, c.PrivateRef)
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
	parents, err := freezeGit(ctx, source.repoRoot, nil, "rev-list", "--parents", "-n", "1", c.CandidateOID)
	if err != nil {
		return err
	}
	if fields := strings.Fields(parents); len(fields) != 2 || fields[0] != c.CandidateOID || fields[1] != c.BaseOID {
		return fail(CodeReviewMismatch, "filtered direct candidate parent", nil)
	}
	if err = verifyFrozenChanges(ctx, source.repoRoot, c.BaseOID, c.TreeOID, c.Changes); err != nil {
		return err
	}
	return previous.Verify(ctx)
}

func validateFilteredChanges(changes []CandidateChange) error {
	if len(changes) < 1 || len(changes) > 256 {
		return fail(CodeReviewMismatch, "filtered candidate changes", nil)
	}
	seen := map[string]bool{}
	for i, c := range changes {
		if !filterSafePath(c.Path) || strings.EqualFold(path.Base(c.Path), ".gitattributes") || i > 0 && changes[i-1].Path >= c.Path || seen[strings.ToLower(c.Path)] {
			return fail(CodeReviewMismatch, "filtered candidate change path", nil)
		}
		seen[strings.ToLower(c.Path)] = true
		for _, prior := range changes[:i] {
			a, b := strings.ToLower(c.Path), strings.ToLower(prior.Path)
			if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return fail(CodeReviewMismatch, "filtered candidate path transition", nil)
			}
		}
		if c.Deleted {
			if c.Mode != "" || c.BlobOID != "" {
				return fail(CodeReviewMismatch, "filtered conflicting deletion", nil)
			}
		} else if c.Mode != "100644" && c.Mode != "100755" || requireOID(c.BlobOID) != nil || c.BlobOID != strings.ToLower(c.BlobOID) {
			return fail(CodeReviewMismatch, "filtered candidate blob", nil)
		}
	}
	return nil
}
func MaterializeFilteredCandidate(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, accepted CandidateReceipt, journal Journal) (*FilterMaterialization, error) {
	ctx, err := filterJournalContext(ctx, source, journal)
	if err != nil {
		return nil, err
	}
	accepted = cloneFilteredCandidate(accepted)
	if req.AttemptID == "" || req.AttemptID == accepted.AttemptID || req.RepoRoot != accepted.RepoRoot || req.CandidateOID != accepted.CandidateOID || req.BaseOID != accepted.BaseOID || req.PlanRevision != accepted.PlanRevision || !reflect.DeepEqual(req.OrderedInputOIDs, accepted.OrderedInputOIDs) || req.Worktree == accepted.Worktree {
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

func filterFreezeState(ctx context.Context, m *FilterMaterialization, commit, tree string) error {
	if m.streams.Binding() != m.streamBinding {
		return fail(CodeIdentityChanged, "filtered stream lifetime", nil)
	}
	if err := m.binding.Verify(ctx); err != nil {
		return err
	}
	if err := m.workspace.verify(); err != nil {
		return err
	}
	childCtx := m.binding.Context(ctx)
	for _, check := range []struct {
		args []string
		want string
	}{{[]string{"rev-parse", "--verify", "HEAD"}, commit}, {[]string{"write-tree"}, tree}, {[]string{"rev-parse", "--symbolic-full-name", "HEAD"}, "HEAD"}} {
		got, err := run(childCtx, m.workspace.path, check.args...)
		if err != nil {
			return err
		}
		if got.Stdout != check.want {
			return fail(CodeTargetDrift, "filtered original tree", nil)
		}
	}
	return nil
}

// Unlike rev-parse --verify, an absent exact ref is a successful read. Reject
// descendants and symbolic aliases rather than treating prefix output as it.
func filterPrivateRefOID(ctx context.Context, source *FilterGitContext, ref string) (string, bool, error) {
	raw, err := filterBoundedQuery(ctx, source, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", ref)
	if err != nil {
		return "", false, err
	}
	if len(raw) == 0 {
		return "", false, nil
	}
	parts := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\x00")
	if len(parts) != 3 || parts[0] != ref || parts[2] != "" || requireOID(parts[1]) != nil {
		return "", false, fail(CodeCollision, "filtered ref alias", nil)
	}
	return parts[1], true, nil
}
