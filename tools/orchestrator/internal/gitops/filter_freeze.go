package gitops

import (
	"context"
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

func PrepareFilteredWorkspace(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, binding CandidateBinding, paths []string, ref string, journal Journal) (*FilterPreparedWorkspace, error) {
	return nil, fail(CodeInvalidInput, "filtered prepare not implemented", nil)
}
func (p *FilterPreparedWorkspace) Workspace() PreparedWorkspace { return PreparedWorkspace{} }
func (p *FilterPreparedWorkspace) Close() error                 { return nil }
func FreezeFilteredWorkspace(ctx context.Context, p *FilterPreparedWorkspace, journal CandidateJournal) (CandidateReceipt, error) {
	return CandidateReceipt{}, fail(CodeInvalidInput, "filtered freeze not implemented", nil)
}
func MaterializeFilteredCandidate(ctx context.Context, source *FilterGitContext, streams FilterStreams, req MaterializeRequest, accepted CandidateReceipt, journal Journal) (*FilterMaterialization, error) {
	return nil, fail(CodeInvalidInput, "filtered validator not implemented", nil)
}
