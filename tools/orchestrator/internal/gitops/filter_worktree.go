package gitops

import (
	"context"
	"path/filepath"
)

// FilterWorktreeBinding pins a structurally valid linked worktree for a live
// Host. It does not prove that the Host created or owns that worktree. Only SourceSHA256 is handed to downstream receipts; child observations
// cannot replace the independently verified original source context.
type FilterWorktreeBinding struct {
	source, worktree *FilterGitContext
}

func (g *FilterGitContext) BindWorktree(ctx context.Context, path string) (*FilterWorktreeBinding, error) {
	invalid := func() (*FilterWorktreeBinding, error) {
		return nil, fail(CodeInvalidInput, "filter worktree binding", nil)
	}
	if g == nil || g.source != nil || path == g.repoRoot || !filepath.IsAbs(path) || CanonicalPath(path) != path {
		return invalid()
	}
	if err := g.Verify(ctx); err != nil {
		return nil, err
	}
	child, err := openFilterGit(ctx, path, g.manifest, false, g)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			child.Close()
		}
	}()
	if child.commonDir != g.commonDir || child.commonIdentity != g.commonIdentity || child.repository.gitLink == nil || child.repository.gitDir == g.repository.gitDir || filepath.Dir(child.repository.gitDir) != filepath.Join(g.commonDir, "worktrees") {
		return invalid()
	}
	backlink, raw, err := readFilterGitLink(filepath.Join(child.repository.gitDir, "gitdir"))
	if err != nil {
		return nil, err
	}
	target, err := filterGitLinkTarget(child.repository.gitDir, raw)
	if err != nil || target != filepath.Join(path, ".git") {
		return invalid()
	}
	if err = g.Verify(ctx); err != nil {
		return nil, err
	}
	child.source, child.bindingBacklink = g, &backlink
	keep = true
	return &FilterWorktreeBinding{source: g, worktree: child}, nil
}

func (b *FilterWorktreeBinding) Context(ctx context.Context) context.Context {
	if b == nil || b.worktree == nil {
		return (&FilterGitContext{closed: true}).Context(ctx)
	}
	return b.worktree.Context(ctx)
}
func (b *FilterWorktreeBinding) SourceSHA256() string {
	if b == nil || b.source == nil {
		return ""
	}
	return b.source.SHA256()
}
func (b *FilterWorktreeBinding) Verify(ctx context.Context) error {
	if b == nil || b.worktree == nil {
		return fail(CodeInvalidInput, "filter worktree binding", nil)
	}
	return b.worktree.Verify(ctx)
}
func (b *FilterWorktreeBinding) Close() error {
	if b == nil || b.worktree == nil {
		return nil
	}
	return b.worktree.Close()
}
