package gitops

import "context"

type FilterWorktreeBinding struct {
	source, worktree *FilterGitContext
}

func (g *FilterGitContext) BindWorktree(ctx context.Context, path string) (*FilterWorktreeBinding, error) {
	return nil, fail(CodeInvalidInput, "filter worktree not bound", nil)
}
func (b *FilterWorktreeBinding) Context(ctx context.Context) context.Context { return ctx }
func (b *FilterWorktreeBinding) SourceSHA256() string                        { return "" }
func (b *FilterWorktreeBinding) Verify(ctx context.Context) error            { return nil }
func (b *FilterWorktreeBinding) Close() error                                { return nil }
