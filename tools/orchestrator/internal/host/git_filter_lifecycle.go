package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Only the original execution owns this object. Its live process observations
// and cleanup result are neither journal input nor a resumable capability.
type filterRuntime struct {
	grant                                 contract.LaunchCommand
	manifest                              contract.GitFilterManifestV1
	broker                                gitfilter.ResourceBroker
	opened                                bool
	invalid                               bool
	providerAttempted                     bool
	provider                              *process.Handle
	providerStartedAt, providerTerminalAt time.Time
	cleanup                               *contract.GitFilterCleanup
	activeMS                              int64
}

func newFilterRuntime(g contract.LaunchCommand) (*filterRuntime, error) {
	if g.GitFilter == nil || contract.RequireGitFilter(g) != nil {
		return nil, contract.ErrGitFilterContract
	}
	m, err := contract.GitFilterManifestFromPayload(g.AdapterPayload)
	if err != nil || m == nil {
		return nil, contract.ErrGitFilterContract
	}
	g.AdapterPayload = append([]byte(nil), g.AdapterPayload...)
	a, f := *g.HostArtifacts, *g.GitFilter
	g.HostArtifacts, g.GitFilter = &a, &f
	if g.ValidationTool != nil {
		v := *g.ValidationTool
		g.ValidationTool = &v
	}
	return &filterRuntime{grant: g, manifest: m.Clone()}, nil
}

func (f *filterRuntime) open(ctx context.Context, sp *events.Spool) error {
	if f.opened {
		return errors.New("git_filter_runtime_reused")
	}
	f.opened = true
	var err error
	f.broker, err = gitfilter.OpenBroker(ctx, gitfilter.BrokerRequest{Launch: f.grant, Grant: *f.grant.GitFilter, Manifest: f.manifest, SegmentDirectory: sp.Directory(), Spool: sp})
	return err
}

func (f *filterRuntime) close() error {
	if f.broker == nil {
		return nil
	}
	return f.broker.Close()
}

func (f *filterRuntime) matches(meta launchMetadata, hostID, runID, taskID, attemptID, segmentID string) bool {
	g := f.grant
	if !meta.hasGitFilter || meta.artifactGrant == nil || g.HostID != hostID || g.RunID != runID || g.TaskID != taskID || g.AttemptID != attemptID || g.SegmentID != segmentID || meta.commandID != g.CommandID || meta.workRevision != g.WorkRevision || meta.executionEpoch != g.ExecutionEpoch {
		return false
	}
	a, ae := json.Marshal(g)
	b, be := json.Marshal(meta.artifactGrant)
	return ae == nil && be == nil && bytes.Equal(a, b)
}

func (f *filterRuntime) providerStarting() {
	if f.providerAttempted {
		f.invalid = true
	}
	f.providerAttempted = true
}

func (f *filterRuntime) providerStarted(p *process.Handle) error {
	if !f.providerAttempted || f.provider != nil || p == nil || p.OriginalSpawnedAt().IsZero() {
		f.invalid = true
		return errors.New("git_filter_provider_start_unproved")
	}
	f.provider, f.providerStartedAt = p, p.OriginalSpawnedAt()
	return nil
}

// Called only immediately after this same Handle's original Wait/tree or
// successful Stop proof. No process identity, journal or wall clock can call it.
func (f *filterRuntime) providerTerminal(p *process.Handle) {
	if f.provider != p || p == nil || f.providerStartedAt.IsZero() {
		f.invalid = true
		return
	}
	if f.providerTerminalAt.IsZero() {
		f.providerTerminalAt = time.Now()
	}
}

func (f *filterRuntime) settle() (int64, *contract.GitFilterCleanup, error) {
	unknown := errors.New("git_filter_original_lifetime_unproved")
	if f.invalid || f.broker == nil || !f.opened || f.providerAttempted && (f.provider == nil || f.providerStartedAt.IsZero() || f.providerTerminalAt.IsZero()) {
		return 0, nil, unknown
	}
	state := f.broker.State()
	if state.Unknown || state.EverStarted && (state.StartedAt.IsZero() || state.TerminalAt.IsZero() || state.TerminalAt.Before(state.StartedAt)) {
		return 0, nil, unknown
	}
	if f.cleanup != nil {
		copy := *f.cleanup
		return f.activeMS, &copy, nil
	}
	first, last := state.StartedAt, state.TerminalAt
	if !f.providerStartedAt.IsZero() && (first.IsZero() || f.providerStartedAt.Before(first)) {
		first = f.providerStartedAt
	}
	if f.providerTerminalAt.After(last) {
		last = f.providerTerminalAt
	}
	if !first.IsZero() {
		if last.IsZero() || last.Before(first) {
			return 0, nil, unknown
		}
		f.activeMS = last.Sub(first).Milliseconds()
		if f.activeMS < 1 {
			f.activeMS = 1
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), gracefulStopWindow)
	defer cancel()
	receipt, err := f.broker.Cleanup(cleanupCtx)
	if err != nil || receipt == nil || receipt.Validate() != nil {
		f.invalid = true
		return 0, nil, errors.Join(unknown, err)
	}
	copy := *receipt
	f.cleanup = &copy
	return f.activeMS, receipt, nil
}
