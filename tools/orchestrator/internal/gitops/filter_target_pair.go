package gitops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"golang.org/x/sys/unix"
)

// FilterTargetCandidatePair binds two already-live, original Host owners. It
// owns only the extra child-index descriptor needed to close the callback gap;
// it never owns either observer, materialization, source, or stream broker.
type FilterTargetCandidatePair struct {
	target             *FilterTargetObservation
	candidate          *FilterMaterialization
	childGitDir        *os.File
	childGitIdentity   DirectoryIdentity
	childGitPath       string
	childIndex         *os.File
	childIndexIdentity FileIdentity
	childIndexSHA256   string
	childHead          filterPinnedFile
}

// BindFilteredTargetCandidate first captures the candidate child HEAD and
// index through original descriptors, then proves both existing live owners.
// Capturing before either full Verify prevents a later journal callback from
// becoming a new baseline for the pair's final pure verification.
func BindFilteredTargetCandidate(ctx context.Context, target *FilterTargetObservation, candidate *FilterMaterialization) (*FilterTargetCandidatePair, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, fail(CodeInvalidInput, "filter target/candidate deadline", nil)
	}
	if !filterTargetCandidateCompatible(target, candidate) {
		return nil, fail(CodeReviewMismatch, "filter target/candidate live owner", nil)
	}
	p := &FilterTargetCandidatePair{target: target, candidate: candidate}
	if err := p.captureChildState(); err != nil {
		_ = p.Close()
		return nil, err
	}
	if err := p.Verify(ctx); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

func filterTargetCandidateCompatible(target *FilterTargetObservation, candidate *FilterMaterialization) bool {
	if target == nil || candidate == nil || target.source == nil || candidate.source == nil || target.source != candidate.source || target.source.source != nil || target.streams == nil || candidate.streams == nil || !sameFilterStreams(target.streams, candidate.streams) || target.streamBinding != candidate.streamBinding || target.streams.Binding() != target.streamBinding || candidate.streams.Binding() != candidate.streamBinding || target.journal == nil || candidate.journal == nil || target.receipt.Version != 1 || candidate.accepted == nil || !reflect.DeepEqual(target.candidate, *candidate.accepted) || candidate.workspace == nil || candidate.binding == nil || candidate.binding.source != target.source || candidate.binding.worktree == nil {
		return false
	}
	r := candidate.receipt
	return r.RepoRoot == target.candidate.RepoRoot && r.CommonDir == target.candidate.CommonDir && r.BaseOID == target.candidate.BaseOID && r.CommitOID == target.candidate.CandidateOID && r.TreeOID == target.candidate.TreeOID && r.PlanRevision == target.candidate.PlanRevision && reflect.DeepEqual(r.OrderedInputOIDs, target.candidate.OrderedInputOIDs) && reflect.DeepEqual(r.Filter, target.candidate.Filter) && r.Worktree == candidate.workspace.path && r.RootIdentity == candidate.workspace.rootIdentity && candidate.binding.worktree.repoRoot == r.Worktree && candidate.binding.worktree.repoIdentity == r.RootIdentity
}

func sameFilterStreams(a, b FilterStreams) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta != nil && ta == tb && ta.Comparable() && a == b
}

func (p *FilterTargetCandidatePair) captureChildState() error {
	child := p.candidate.binding.worktree
	if err := p.candidate.workspace.verify(); err != nil {
		return err
	}
	if child == nil || child.repository.gitDir == "" || CanonicalPath(child.repository.gitDir) != child.repository.gitDir {
		return fail(CodeIdentityChanged, "filter target/candidate child repository", nil)
	}
	headPath := filepath.Join(child.repository.gitDir, "HEAD")
	head, err := pinFilterFileKind(headPath, "", filterGitLinkMaximumBytes, false, nil)
	if err != nil {
		return err
	}
	if err = pairVerifyHead(head, p.candidate.receipt.CommitOID); err != nil {
		return err
	}
	gitDir, err := os.OpenFile(child.repository.gitDir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	gitIdentity, err := filterTargetDirectory(gitDir)
	if err != nil || gitIdentity != child.repository.gitIdentity {
		_ = gitDir.Close()
		return fail(CodeIdentityChanged, "filter target/candidate child git directory", err)
	}
	index, err := filterOpenAt(gitDir, "index", unix.O_RDONLY, 0)
	if err != nil {
		_ = gitDir.Close()
		return err
	}
	raw, identity, err := pairReadIndex(index, gitDir)
	if err != nil {
		_ = index.Close()
		_ = gitDir.Close()
		return err
	}
	p.childHead, p.childGitDir, p.childGitIdentity, p.childGitPath = head, gitDir, gitIdentity, child.repository.gitDir
	p.childIndex, p.childIndexIdentity, p.childIndexSHA256 = index, identity, filterTargetHash(raw)
	return nil
}

func pairVerifyHead(pin filterPinnedFile, commit string) error {
	if !verifyFilterPin(pin) {
		return fail(CodeIdentityChanged, "filter target/candidate child head", nil)
	}
	f, err := os.OpenFile(pin.path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fail(CodeIdentityChanged, "filter target/candidate child head", err)
	}
	defer f.Close()
	id, err := filterTargetRegular(f)
	if err != nil || id != pin.identity {
		return fail(CodeIdentityChanged, "filter target/candidate child head", err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, filterGitLinkMaximumBytes+1))
	after, afterErr := filterTargetRegular(f)
	if err != nil || afterErr != nil || id != after || int64(len(raw)) != id.Size || string(raw) != commit+"\n" || !verifyFilterPin(pin) {
		return fail(CodeIdentityChanged, "filter target/candidate child head", errors.Join(err, afterErr))
	}
	return nil
}

func pairReadIndex(index, gitDir *os.File) ([]byte, FileIdentity, error) {
	if index == nil || gitDir == nil {
		return nil, FileIdentity{}, fail(CodeIdentityChanged, "filter target/candidate child index closed", nil)
	}
	id, err := filterTargetRegular(index)
	if err != nil || (id.Mode != 0600 && id.Mode != 0644) {
		return nil, id, fail(CodeInvalidInput, "filter target/candidate child index mode", err)
	}
	if _, err = index.Seek(0, io.SeekStart); err != nil {
		return nil, id, err
	}
	raw, err := io.ReadAll(io.LimitReader(index, 16<<20+1))
	after, afterErr := filterTargetRegular(index)
	named, openErr := filterOpenAt(gitDir, "index", unix.O_RDONLY, 0)
	if openErr != nil {
		return nil, id, openErr
	}
	namedID, namedErr := filterTargetRegular(named)
	closeErr := named.Close()
	if err != nil || afterErr != nil || namedErr != nil || closeErr != nil || id != after || id != namedID || int64(len(raw)) != id.Size || filterTargetIndexHeader(raw) != nil {
		return nil, id, fail(CodeIdentityChanged, "filter target/candidate child index", errors.Join(err, afterErr, namedErr, closeErr))
	}
	return raw, id, nil
}

// Verify proves both current live owners through their complete Git paths,
// then closes their final callback window with one joint pure tail check.
func (p *FilterTargetCandidatePair) Verify(ctx context.Context) error {
	if p == nil || p.target == nil || p.candidate == nil || !filterTargetCandidateCompatible(p.target, p.candidate) {
		return fail(CodeIdentityChanged, "filter target/candidate pair", nil)
	}
	if err := p.target.Verify(ctx); err != nil {
		return err
	}
	if err := p.candidate.Verify(ctx); err != nil {
		return err
	}
	return p.verifyPure(ctx)
}

func (p *FilterTargetCandidatePair) verifyPure(ctx context.Context) error {
	if err := p.target.verifyFinalPure(ctx); err != nil {
		return err
	}
	if err := p.candidate.workspace.verify(); err != nil {
		return err
	}
	files, err := p.candidate.workspace.inventory(ctx)
	if err != nil || !reflect.DeepEqual(files, p.candidate.receipt.Files) {
		return fail(CodeIdentityChanged, "filter target/candidate inventory", err)
	}
	if err = pairVerifyChildContext(p.candidate.binding.worktree, p.target.source); err != nil {
		return err
	}
	if err = p.verifyChildGitDir(); err != nil {
		return err
	}
	if err = pairVerifyHead(p.childHead, p.candidate.receipt.CommitOID); err != nil {
		return err
	}
	raw, id, err := pairReadIndex(p.childIndex, p.childGitDir)
	if err != nil || id != p.childIndexIdentity || filterTargetHash(raw) != p.childIndexSHA256 {
		return fail(CodeIdentityChanged, "filter target/candidate child index changed", err)
	}
	return nil
}

func (p *FilterTargetCandidatePair) verifyChildGitDir() error {
	child := p.candidate.binding.worktree
	if p.childGitDir == nil || child == nil || p.childGitPath != child.repository.gitDir || CanonicalPath(p.childGitPath) != p.childGitPath {
		return fail(CodeIdentityChanged, "filter target/candidate child git directory", nil)
	}
	held, err := filterTargetDirectory(p.childGitDir)
	named, namedErr := directoryIdentity(p.childGitPath)
	if err != nil || namedErr != nil || held != p.childGitIdentity || named != held || held != child.repository.gitIdentity {
		return fail(CodeIdentityChanged, "filter target/candidate child git directory", errors.Join(err, namedErr))
	}
	return nil
}

func pairVerifyChildContext(child, source *FilterGitContext) error {
	if child == nil || source == nil {
		return fail(CodeIdentityChanged, "filter target/candidate child context", nil)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.closed || child.source != source || child.bindingBacklink == nil || !verifyFilterPin(*child.bindingBacklink) || !child.verifySources() {
		return fail(CodeIdentityChanged, "filter target/candidate child context", nil)
	}
	if err := child.verifyRepositoryFiles(); err != nil {
		return err
	}
	if actual, err := directoryIdentity(child.repoRoot); err != nil || actual != child.repoIdentity {
		return fail(CodeIdentityChanged, "filter target/candidate child root", err)
	}
	if actual, err := directoryIdentity(child.commonDir); err != nil || actual != child.commonIdentity {
		return fail(CodeIdentityChanged, "filter target/candidate child common", err)
	}
	return nil
}

// Close releases only the pair-owned child-index descriptor. Borrowed live
// target/materialization/source/broker owners remain available to their Host.
func (p *FilterTargetCandidatePair) Close() error {
	if p == nil {
		return nil
	}
	var errs []error
	for _, file := range []**os.File{&p.childIndex, &p.childGitDir} {
		if *file != nil {
			errs = append(errs, (*file).Close())
			*file = nil
		}
	}
	return errors.Join(errs...)
}
