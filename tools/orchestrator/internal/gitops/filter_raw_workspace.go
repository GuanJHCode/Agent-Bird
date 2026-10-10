package gitops

import (
	"context"
	"io"
	"os"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

// FilterStreams is supplied only by the original Host. Filesystem grants and
// cleanup remain with its authenticated Broker; this interface grants neither.
type FilterStreams interface {
	Load(context.Context, int, *os.File) error
	LoadStream(context.Context, int, func(io.Writer) error) (gitfilter.StreamLoadResult, error)
	Transform(context.Context, string, int, int) (gitfilter.Result, error)
	ReadSlot(context.Context, int, io.Writer) error
}

// FilterMaterialization has no exported fields and cannot be restored from a
// receipt. Its original source, stream owner and linked worktree stay live.
type FilterMaterialization struct {
	source  *FilterGitContext
	streams FilterStreams
	binding *FilterWorktreeBinding
	receipt MaterializeReceipt
}

func MaterializeFiltered(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, journal Journal) (*FilterMaterialization, error) {
	return nil, fail(CodeInvalidInput, "raw filter materialization not implemented", nil)
}
func (m *FilterMaterialization) Receipt() MaterializeReceipt { return MaterializeReceipt{} }
func (m *FilterMaterialization) Close() error                { return nil }
