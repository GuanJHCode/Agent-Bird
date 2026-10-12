package gitops

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// ValidatePreparedWorkspace observes identities and declared-file edits only.
// It does not reset, stage, commit, clean or create a worktree.
func ValidatePreparedWorkspace(ctx context.Context, p PreparedWorkspace) error {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	r := p.Receipt
	if err := candidateRepositoryAllowed(ctx, r.Worktree); err != nil {
		return err
	}
	if r.Worktree != r.WorktreeIdentity || r.AttemptID != p.Binding.AttemptID || r.BaseOID != r.CommitOID || len(p.Paths) == 0 {
		return fail(CodeInvalidInput, "resume", fmt.Errorf("workspace binding missing"))
	}
	for _, v := range []struct {
		path     string
		identity DirectoryIdentity
	}{{r.Worktree, r.RootIdentity}, {r.CommonDir, p.CommonDirIdentity}, {r.RepoRoot, p.RepoRootIdentity}} {
		actual, err := directoryIdentity(v.path)
		if err != nil || actual != v.identity {
			return fail(CodeIdentityChanged, "resume", fmt.Errorf("directory identity changed"))
		}
	}
	common, err := commonDir(ctx, r.Worktree)
	if err != nil {
		return err
	}
	originCommon, err := commonDir(ctx, r.RepoRoot)
	if err != nil {
		return err
	}
	if common != r.CommonDir || originCommon != r.CommonDir {
		return fail(CodeIdentityChanged, "resume", fmt.Errorf("repository changed"))
	}
	head, err := rev(ctx, r.Worktree, "HEAD")
	if err != nil {
		return err
	}
	tree, err := rev(ctx, r.Worktree, "HEAD^{tree}")
	if err != nil {
		return err
	}
	inProgress, err := candidateOperationInProgress(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if head != r.CommitOID || tree != r.TreeOID || inProgress {
		return fail(CodeTargetDrift, "resume", fmt.Errorf("workspace base changed"))
	}
	files, err := snapshotWithContext(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if files[".git"] != r.Files[".git"] {
		return fail(CodeIdentityChanged, "resume", fmt.Errorf("git link changed"))
	}
	if _, err = freezeGit(ctx, r.Worktree, nil, "diff", "--cached", "--quiet", "--no-ext-diff", r.CommitOID, "--"); err != nil {
		return fail(CodeTargetDirty, "resume", err)
	}
	_, err = workspaceChanges(ctx, r.Worktree, p.Paths)
	return err
}

func ValidateFrozenWorkspace(ctx context.Context, p PreparedWorkspace, c CandidateReceipt) error {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if c.Binding == nil || *c.Binding != p.Binding || c.AttemptID != p.Receipt.AttemptID || c.Worktree != p.Receipt.Worktree || c.BaseOID != p.Receipt.CommitOID || c.PrivateRef != p.PrivateRef {
		return fail(CodeIdentityChanged, "freeze replay", fmt.Errorf("candidate binding changed"))
	}
	if err := candidateRepositoryAllowed(ctx, c.Worktree); err != nil {
		return err
	}
	root, err := directoryIdentity(c.Worktree)
	if err != nil || root != c.RootIdentity {
		return fail(CodeIdentityChanged, "freeze replay", fmt.Errorf("workspace changed"))
	}
	files, err := snapshotWithContext(ctx, c.Worktree)
	if err != nil || !reflect.DeepEqual(files, c.Files) {
		return fail(CodeIdentityChanged, "freeze replay", fmt.Errorf("files changed"))
	}
	head, err := rev(ctx, c.Worktree, "HEAD")
	if err != nil {
		return err
	}
	ref, err := rev(ctx, c.RepoRoot, c.PrivateRef)
	if err != nil {
		return err
	}
	if head != c.CandidateOID || strings.TrimSpace(ref) != c.PrivateRefExpectedOID {
		return fail(CodeTargetDrift, "freeze replay", fmt.Errorf("candidate ref changed"))
	}
	return nil
}
