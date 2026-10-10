package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
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
	journal                               filterHostJournal
	source                                *gitops.FilterGitContext
}

type filterHostJournal interface {
	gitops.Journal
	gitops.CandidateJournal
	gitops.GitProcessJournal
	gitops.FilterMaterializeJournal
	RecordWorkspacePrepared(context.Context, gitops.PreparedWorkspace) error
	RecordCandidateAction(context.Context, string, any) error
	Close() error
}

func newFilterRuntime(g contract.LaunchCommand) (*filterRuntime, error) {
	if g.GitFilter == nil || contract.RequireGitFilter(g) != nil {
		return nil, contract.ErrGitFilterContract
	}
	m, err := contract.GitFilterManifestFromPayload(g.AdapterPayload)
	if err != nil || m == nil {
		return nil, contract.ErrGitFilterContract
	}
	// Clone accepted inputs and all nested authority before any invocation
	// callback. Preserve exact payload bytes: RawMessage marshaling compacts
	// JSON, but resource/input hashes are bound to the original byte sequence.
	original := g
	raw, err := json.Marshal(g)
	if err != nil || json.Unmarshal(raw, &g) != nil {
		return nil, contract.ErrGitFilterContract
	}
	g.AdapterPayload = append([]byte(nil), original.AdapterPayload...)
	if g.Input != nil {
		g.Input.AdapterPayload = append([]byte(nil), original.Input.AdapterPayload...)
	}
	if g.DependencyInputs != nil {
		for i := range g.DependencyInputs.Inputs {
			g.DependencyInputs.Inputs[i].AdapterPayload = append([]byte(nil), original.DependencyInputs.Inputs[i].AdapterPayload...)
		}
	}
	return &filterRuntime{grant: g, manifest: m.Clone()}, nil
}

func filterJournalKey(g contract.LaunchCommand) string {
	return hashText(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + g.SegmentID)
}

func (f *filterRuntime) freshJournal(spoolRoot string) error {
	if f.journal != nil || f.opened {
		return errors.New("git_filter_runtime_reused")
	}
	j, err := gitopsworker.OpenFreshJournal(filepath.Join(spoolRoot, ".gitops-journal", filterJournalKey(f.grant)))
	if err != nil {
		return err
	}
	f.journal = j
	return nil
}

func (f *filterRuntime) openSource(ctx context.Context, repo string) error {
	if f.journal == nil || f.source != nil || f.broker == nil || f.invalid || f.cleanup != nil {
		return errors.New("git_filter_runtime_reused")
	}
	var err error
	f.source, err = gitops.OpenFilterGit(gitops.WithCandidateJournal(ctx, f.journal), repo, f.manifest)
	return err
}

func (f *filterRuntime) requireOriginalTerminal() error {
	if f == nil || f.invalid || !f.opened || f.broker == nil || f.journal == nil || f.source == nil || f.cleanup != nil || !f.providerAttempted || f.provider == nil || f.providerStartedAt.IsZero() || f.providerTerminalAt.Before(f.providerStartedAt) || f.provider.OriginalSpawnedAt() != f.providerStartedAt {
		return errors.Join(process.ErrProcessTreeUnknown, errors.New("git_filter_original_terminal_required"))
	}
	if err := f.provider.ConfirmTreeExited(); err != nil {
		return errors.Join(process.ErrProcessTreeUnknown, err)
	}
	return nil
}

func (f *filterRuntime) open(ctx context.Context, sp *events.Spool) error {
	if f.opened {
		return errors.New("git_filter_runtime_reused")
	}
	if filepath.Base(sp.Directory()) != f.grant.SegmentID || filepath.Base(filepath.Dir(sp.Directory())) != f.grant.AttemptID {
		f.invalid = true
		return contract.ErrGitFilterContract
	}
	err := f.freshJournal(filepath.Dir(filepath.Dir(sp.Directory())))
	f.opened = true
	if err != nil {
		f.invalid = true
		return err
	}
	f.broker, err = gitfilter.OpenBroker(ctx, gitfilter.BrokerRequest{Launch: f.grant, Grant: *f.grant.GitFilter, Manifest: f.manifest, SegmentDirectory: sp.Directory(), Spool: sp})
	return err
}

func (f *filterRuntime) close() error {
	var err error
	if f.source != nil {
		err = errors.Join(err, f.source.Close())
	}
	if f.journal != nil {
		err = errors.Join(err, f.journal.Close())
	}
	if f.broker != nil {
		err = errors.Join(err, f.broker.Close())
	}
	return err
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
